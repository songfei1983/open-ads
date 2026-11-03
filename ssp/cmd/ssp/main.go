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
	adapterHTTP "github.com/songfei1983/open-ads/ssp/internal/ssp/adapter/http"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/config"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/control"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/dmp"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/ledger"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/netguard"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/observability"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/render"
	sspRuntime "github.com/songfei1983/open-ads/ssp/internal/ssp/runtime"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/valkey"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.ServiceName, cfg.LogLevel)
	logger.Info("initializing SSP application",
		slog.String("environment", cfg.Environment),
		slog.Int("port", cfg.Port),
	)

	server := adapterHTTP.NewServer(cfg, logger)
	var stopWorker context.CancelFunc

	if cfg.AdminTenantID != "" {
		db, err := openDatabase(cfg)
		if err != nil {
			logger.Error("failed to initialize SSP configuration database", slog.Any("error", err))
			os.Exit(1)
		}
		defer db.Close()
		store, err := control.NewSQLStore(db, cfg.AdminTenantID)
		if err != nil {
			logger.Error("failed to initialize SSP management store", slog.Any("error", err))
			os.Exit(1)
		}
		if err := store.EnsureTenant(context.Background(), "sandbox"); err != nil {
			logger.Error("failed to ensure SSP tenant", slog.Any("error", err))
			os.Exit(1)
		}
		ledgerStore, err := ledger.NewStore(db, cfg.AdminTenantID)
		if err != nil {
			logger.Error("failed to initialize the SSP ledger", slog.Any("error", err))
			os.Exit(1)
		}
		server.SetLedger(ledgerStore)
		billableOutbox := &ledger.Sender{
			Store:  ledgerStore,
			Logger: logger,
			Client: &http.Client{
				Timeout: 2 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
				Transport: &http.Transport{DialContext: netguard.DialContext(2*time.Second, cfg.AllowPrivateCallbacks)},
			},
		}
		valkeyClient := &valkey.Client{
			Address: cfg.ValkeyAddr, Username: cfg.ValkeyUser, Password: cfg.ValkeyPassword,
			TLS: cfg.ValkeyTLS, Timeout: 2 * time.Second,
		}

		var codec *token.Codec
		if cfg.TokenKey != "" {
			key, err := hex.DecodeString(cfg.TokenKey)
			if err != nil {
				logger.Error("failed to decode SSP token key", slog.Any("error", err))
				os.Exit(1)
			}
			codec, err = token.NewCodec(key, cfg.TokenKeyVersion)
			if err != nil {
				logger.Error("failed to create SSP token codec", slog.Any("error", err))
				os.Exit(1)
			}
			server.SetTokenCodec(codec)
			readStore, err := render.NewReadStore(cfg.AssetStoreRoot)
			if err != nil {
				logger.Error("failed to initialize the SSP creative asset store", slog.Any("error", err))
				os.Exit(1)
			}
			server.SetRenderer(&render.Renderer{Codec: codec, Store: readStore, TenantID: cfg.AdminTenantID, Metrics: server.Metrics()})
		} else {
			logger.Warn("SSP render tokens are disabled; configure SSP_TOKEN_KEY to serve filled ads")
		}

		if cfg.DMPURL != "" && cfg.SubjectHMACKey != "" {
			key, err := hex.DecodeString(cfg.SubjectHMACKey)
			if err != nil {
				logger.Error("failed to decode SSP subject HMAC key", slog.Any("error", err))
				os.Exit(1)
			}
			server.SetDMPAdapter(&dmp.Adapter{
				BaseURL: cfg.DMPURL, TenantID: cfg.DMPTenantID, Namespace: cfg.DMPNamespace,
				ProviderID: cfg.DMPProviderID, HMACKey: key, Timeout: cfg.DMPTimeout,
				Client: &http.Client{
					Timeout:   2 * time.Second,
					Transport: &http.Transport{DialContext: netguard.DialContext(2*time.Second, cfg.AllowPrivateCallbacks)},
				},
			})
		} else if cfg.DMPURL != "" {
			logger.Warn("SSP audience lookups are disabled; configure SSP_SUBJECT_HMAC_KEY")
		}

		if cfg.AdminToken != "" {
			runtimeReader := &sspRuntime.Reader{Client: valkeyClient, Tenant: cfg.AdminTenantID}
			adminMux := http.NewServeMux()
			adminMux.Handle("/admin/v1/billable-outbox", ledger.NewAdminHandler(ledgerStore, cfg.AdminToken))
			adminMux.Handle("/admin/v1/publisher-payable", ledger.NewAdminHandler(ledgerStore, cfg.AdminToken))
			adminMux.Handle("/", control.NewAdminHandler(store, cfg.AdminToken, runtimeReader, logger))
			server.SetAdminHandler(adminMux)
		} else {
			logger.Warn("SSP management API is disabled; SSP_ADMIN_TOKEN is not configured")
		}
		if cfg.InternalToken != "" {
			server.SetInternalHandler(control.NewInternalHandler(store, cfg.InternalToken, logger))
		} else {
			logger.Warn("SSP internal API is disabled; SSP_INTERNAL_TOKEN is not configured")
		}

		instanceID, err := domain.NewUUIDv7()
		if err != nil {
			logger.Error("failed to create SSP instance incarnation", slog.Any("error", err))
			os.Exit(1)
		}
		workerCtx, cancel := context.WithCancel(context.Background())
		stopWorker = cancel
		go refreshConfig(workerCtx, server, store, valkeyClient, cfg, instanceID, logger)
		go billableOutbox.Run(workerCtx, time.Second)
	} else {
		logger.Warn("SSP config refresh is disabled; configure SSP_ADMIN_TENANT_ID")
		server.SetReady(true)
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
	if stopWorker != nil {
		stopWorker()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("SSP service stopped cleanly")
}

func refreshConfig(workerCtx context.Context, server *adapterHTTP.Server, store *control.SQLStore, client *valkey.Client, cfg *config.Config, instanceID string, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	writer := &sspRuntime.Writer{Client: client, Tenant: cfg.AdminTenantID}
	manifestKey := "ssp:manifest:{" + cfg.AdminTenantID + "}"
	publisher := cfg.ConfigPublisher || cfg.AdminToken != ""
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
			if _, err := store.PublishPending(ctx, client); err != nil {
				publishErr = err
			}
		}
		loadStart := time.Now()
		value, readErr := client.Do(ctx, "GET", manifestKey)
		observedVersion := ""
		var generatedAt time.Time
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
					readErr = fmt.Errorf("unsupported SSP manifest schema version %d", envelope.SchemaVersion)
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
				if readErr == nil {
					generatedAt = envelope.GeneratedAt
				}
			}
		} else if value == nil {
			readErr = errors.New("Valkey manifest is not available")
		}
		localVersion := snapshot.CurrentVersion()
		synced := readErr == nil && localVersion == observedVersion && publishErr == nil
		heartbeat := sspRuntime.Heartbeat{
			InstanceID: instanceID, ManifestVersion: localVersion, ObservedVersion: observedVersion,
			Ready: synced, HeartbeatUnix: time.Now().UTC().Unix(),
		}
		if err := writer.Report(ctx, heartbeat); err != nil {
			if readErr == nil {
				readErr = err
			}
			synced = false
		}
		cancel()
		if synced {
			lastSuccess = time.Now()
			staleness := time.Since(generatedAt)
			if staleness < 0 {
				staleness = 0
			}
			server.Metrics().ObserveSnapshotLoad(true, staleness)
			server.Metrics().ObserveSnapshotLoadDuration(time.Since(loadStart))
			server.SetReady(true)
		} else {
			if readErr != nil {
				server.Metrics().ObserveSnapshotLoad(false, 0)
			}
			if !lastSuccess.IsZero() {
				server.Metrics().SetSnapshotStaleness(time.Since(lastSuccess))
			}
			if lastSuccess.IsZero() || time.Since(lastSuccess) > 10*time.Second {
				server.SetReady(false)
			}
		}
		if publishErr != nil {
			logger.Error("failed to publish SSP config outbox", slog.Any("error", publishErr))
		}
		if readErr != nil {
			logger.Warn("SSP instance has not synchronized its manifest", slog.Any("error", readErr))
		}
	}
}

func openDatabase(cfg *config.Config) (*sql.DB, error) {
	dbConfig := mysql.Config{
		User: cfg.DBUser, Passwd: cfg.DBPassword, Net: "tcp",
		Addr: net.JoinHostPort(cfg.DBHost, strconv.Itoa(cfg.DBPort)), DBName: cfg.DBName,
		ParseTime: true, Loc: time.UTC, AllowNativePasswords: true,
	}
	db, err := sql.Open("mysql", dbConfig.FormatDSN())
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
