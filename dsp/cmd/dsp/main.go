package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	adapterHTTP "github.com/songfei1983/open-ads/dsp/internal/dsp/adapter/http"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/assets"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/config"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/control"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/decision"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/observability"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/valkey"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.ServiceName, cfg.LogLevel)
	logger.Info("initializing DSP application",
		slog.String("environment", cfg.Environment),
		slog.Int("port", cfg.Port),
	)

	server := adapterHTTP.NewServer(cfg, logger)
	server.SetDecisioner(decision.FixtureDecisioner{Whitelist: cfg.CreativeWhitelist})
	var stopConfigWorker context.CancelFunc
	tenantID := strings.ToLower(strings.ReplaceAll(cfg.AdminTenantID, "-", ""))
	var store *control.SQLStore
	if cfg.AdminToken != "" || cfg.ConfigPublisher {
		db, err := openDatabase(cfg)
		if err != nil {
			logger.Error("failed to initialize DSP configuration database", slog.Any("error", err))
			os.Exit(1)
		}
		defer db.Close()
		store, err = control.NewSQLStore(db, tenantID)
		if err != nil {
			logger.Error("failed to initialize DSP management store", slog.Any("error", err))
			os.Exit(1)
		}
		if cfg.AdminToken != "" {
			assetStore, err := assets.NewFileStore(cfg.AssetStoreRoot)
			if err != nil {
				logger.Error("failed to initialize DSP asset store", slog.Any("error", err))
				os.Exit(1)
			}
			defer assetStore.Close()
			store.SetAssetStore(assetStore)
			server.SetAdminHandler(control.NewHandler(store, cfg.AdminToken))
		}
	}
	if tenantID != "" {
		if cfg.AdminToken == "" {
			logger.Warn("DSP management API is disabled; DSP_ADMIN_TOKEN is not configured")
		}
		instanceID, err := domain.NewUUIDv7()
		if err != nil {
			logger.Error("failed to create DSP instance incarnation", slog.Any("error", err))
			os.Exit(1)
		}
		valkeyClient := &valkey.Client{Address: cfg.ValkeyAddr, Username: cfg.ValkeyUser, Password: cfg.ValkeyPassword, TLS: cfg.ValkeyTLS, Timeout: 2 * time.Second}
		workerCtx, cancel := context.WithCancel(context.Background())
		stopConfigWorker = cancel
		go refreshPublishedConfig(workerCtx, server, store, valkeyClient, tenantID, instanceID, cfg.ConfigPublisher || cfg.AdminToken != "", logger)
	} else {
		logger.Warn("DSP config refresh is disabled; configure DSP_ADMIN_TENANT_ID")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server fatal error", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	sig := <-stop
	logger.Info("shutdown signal received", slog.String("signal", sig.String()))
	if stopConfigWorker != nil {
		stopConfigWorker()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("DSP service stopped cleanly")
}

func refreshPublishedConfig(workerCtx context.Context, server *adapterHTTP.Server, store *control.SQLStore, client *valkey.Client, tenantID, instanceID string, publisher bool, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastSuccess time.Time
	for {
		select {
		case <-workerCtx.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(workerCtx, 2*time.Second)
		var publishErr error
		if publisher {
			if store == nil {
				publishErr = errors.New("DSP config publisher has no database store")
			} else if _, err := store.PublishPending(ctx, client); err != nil {
				publishErr = err
			}
		}
		tenantHex := strings.ToLower(strings.ReplaceAll(tenantID, "-", ""))
		manifestKey := "dsp:manifest:{" + tenantHex + "}"
		value, readErr := client.Do(ctx, "GET", manifestKey)
		observedVersion := ""
		var envelope struct {
			SchemaVersion int             `json:"schema_version"`
			Version       string          `json:"version"`
			GeneratedAt   time.Time       `json:"generated_at"`
			PayloadHash   string          `json:"payload_hash"`
			Manifest      json.RawMessage `json:"manifest"`
		}
		if readErr == nil {
			serialized, ok := value.(string)
			if !ok {
				readErr = fmt.Errorf("unexpected Valkey manifest response %T", value)
			} else if newline := strings.IndexByte(serialized, '\n'); newline < 0 {
				readErr = errors.New("invalid Valkey manifest envelope")
			} else {
				observedVersion = serialized[:newline]
				readErr = json.Unmarshal([]byte(serialized[newline+1:]), &envelope)
				if readErr == nil && envelope.SchemaVersion != 1 {
					readErr = fmt.Errorf("unsupported DSP manifest schema version %d", envelope.SchemaVersion)
				}
				if readErr == nil && (envelope.Version != observedVersion || len(envelope.Manifest) == 0 || envelope.GeneratedAt.IsZero()) {
					readErr = errors.New("Valkey manifest version does not match its payload")
				}
				if readErr == nil {
					digest := sha256.Sum256(envelope.Manifest)
					if hex.EncodeToString(digest[:]) != envelope.PayloadHash {
						readErr = errors.New("Valkey manifest payload hash mismatch")
					}
				}
				var manifest *snapshot.Manifest
				if readErr == nil {
					manifest = &snapshot.Manifest{}
					readErr = json.Unmarshal(envelope.Manifest, manifest)
				}
				if readErr == nil && manifest.Version != observedVersion {
					readErr = errors.New("manifest version does not match envelope version")
				}
				if readErr == nil && snapshot.CurrentVersion() != observedVersion {
					readErr = snapshot.Activate(manifest)
				}
			}
		} else if value == nil {
			readErr = errors.New("Valkey manifest is not available")
		}
		localVersion := snapshot.CurrentVersion()
		synced := readErr == nil && localVersion == observedVersion
		heartbeat := struct {
			InstanceID      string `json:"instance_id"`
			ManifestVersion string `json:"manifest_version"`
			ObservedVersion string `json:"observed_version"`
			Ready           bool   `json:"ready"`
			HeartbeatUnix   int64  `json:"heartbeat_unix"`
		}{instanceID, localVersion, observedVersion, synced && publishErr == nil, time.Now().UTC().Unix()}
		statusPayload, _ := json.Marshal(heartbeat)
		statusKey := "dsp:runtime:{" + tenantHex + "}:" + instanceID
		if _, err := client.Do(ctx, "SET", statusKey, string(statusPayload), "EX", "30"); err != nil {
			if readErr == nil {
				readErr = err
			}
			synced = false
		} else {
			indexKey := "dsp:runtime-index:{" + tenantHex + "}"
			now := strconv.FormatInt(time.Now().UTC().Unix(), 10)
			_, err := client.Do(ctx, "ZADD", indexKey, now, instanceID)
			if err == nil {
				cutoff := strconv.FormatInt(time.Now().UTC().Add(-30*time.Second).Unix(), 10)
				_, err = client.Do(ctx, "ZREMRANGEBYSCORE", indexKey, "-inf", cutoff)
			}
			if err != nil {
				if readErr == nil {
					readErr = err
				}
				synced = false
			}
		}
		cancel()
		if synced && publishErr == nil && readErr == nil {
			lastSuccess = time.Now()
			server.SetReady(true)
		} else if lastSuccess.IsZero() || time.Since(lastSuccess) > 10*time.Second {
			server.SetReady(false)
		}
		if publishErr != nil {
			logger.Error("failed to publish DSP config outbox", slog.Any("error", publishErr))
		}
		if readErr != nil {
			logger.Warn("DSP instance has not synchronized its manifest", slog.Any("error", readErr))
		}
	}
}

func openDatabase(cfg *config.Config) (*sql.DB, error) {
	dbConfig := mysql.Config{
		User:      cfg.DBUser,
		Passwd:    cfg.DBPassword,
		Net:       "tcp",
		Addr:      net.JoinHostPort(cfg.DBHost, strconv.Itoa(cfg.DBPort)),
		DBName:    cfg.DBName,
		ParseTime: true,
		Loc:       time.UTC,
	}
	dsn := dbConfig.FormatDSN()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
