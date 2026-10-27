package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	adapterHTTP "github.com/songfei1983/open-ads/dmp/internal/dmp/adapter/http"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/gate"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/retention"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/revocation"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/valkey"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.ServiceName, cfg.LogLevel)
	logger.Info("initializing DMP application",
		slog.String("environment", cfg.Environment),
		slog.Int("port", cfg.Port),
	)

	options, closeControl, err := controlPlaneOptions(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize the DMP control plane", slog.Any("error", err))
		os.Exit(1)
	}
	defer closeControl()

	server := adapterHTTP.NewServer(cfg, logger, options...)

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("DMP service stopped cleanly")
}

// controlPlaneOptions binds the management API to the configured tenant. When no
// admin token is configured the API stays disabled (503) instead of exposing
// unauthenticated writes.
func controlPlaneOptions(cfg *config.Config, logger *slog.Logger) ([]adapterHTTP.Option, func(), error) {
	if cfg.AdminToken == "" {
		logger.Warn("DMP management API is disabled; DMP_ADMIN_TOKEN is not configured")
	}
	if cfg.AdminToken == "" && cfg.InternalToken == "" {
		return nil, func() {}, nil
	}
	tenantID, err := uuid.Parse(cfg.AdminTenantID)
	if err != nil {
		return nil, func() {}, fmt.Errorf("parse DMP_ADMIN_TENANT_ID: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := control.OpenClickHouse(ctx, cfg.ClickHouseAddr, cfg.ClickHouseUser, cfg.ClickHousePassword, cfg.ClickHouseDatabase)
	if err != nil {
		logger.Warn("DMP definition stores unavailable; management and internal APIs answer 503",
			slog.String("clickhouse_addr", cfg.ClickHouseAddr), slog.Any("error", err))
		return nil, func() {}, nil
	}
	controlStore := control.NewClickHouseStore(conn, tenantID)
	snapshotStore := snapshot.NewStore(conn, controlStore, tenantID)
	revocationStore := revocation.NewStore(conn, tenantID)

	loadTombstones := func(loadCtx context.Context) ([]gate.DenyEntry, error) {
		tombstones, err := revocationStore.Tombstones(loadCtx)
		if err != nil {
			return nil, err
		}
		entries := make([]gate.DenyEntry, 0, len(tombstones))
		for _, tombstone := range tombstones {
			entries = append(entries, gate.DenyEntry{
				Namespace: tombstone.Namespace, Token: tombstone.Token, ReasonCode: tombstone.ReasonCode,
			})
		}
		return entries, nil
	}

	var denyGate *gate.Gate
	businessMetrics := observability.NewDMPMetrics(nil)
	options := []adapterHTTP.Option{
		adapterHTTP.WithBusinessMetrics(businessMetrics),
		adapterHTTP.WithControlStore(controlStore),
		adapterHTTP.WithSnapshotStore(snapshotStore),
		adapterHTTP.WithRevocationStore(revocationStore),
	}
	if cfg.InternalToken != "" {
		gateClient := &valkey.Client{Address: cfg.ValkeyAddr, Username: cfg.ValkeyUser, Password: cfg.ValkeyPassword, Timeout: 2 * time.Second}
		denyGate = gate.New(gateClient, tenantID.String())
		epoch, gateErr := denyGate.Recover(ctx, loadTombstones)
		if gateErr != nil {
			logger.Warn("deny gate is not ready; audience lookups fail closed",
				slog.String("valkey_addr", cfg.ValkeyAddr), slog.Any("error", gateErr))
		} else {
			businessMetrics.GateEpoch(epoch)
			logger.Info("deny gate ready", slog.String("valkey_addr", cfg.ValkeyAddr), slog.Uint64("epoch", epoch))
		}
		options = append(options, adapterHTTP.WithDenyGate(denyGate))
	}

	retentionWorker := retention.NewWorker(snapshotStore, revocationStore, retention.Policy{
		MembershipRetention: time.Duration(cfg.MembershipRetentionDays) * 24 * time.Hour,
		EventRetention:      time.Duration(cfg.EventRetentionDays) * 24 * time.Hour,
		PhysicalDeleteGrace: time.Duration(cfg.PhysicalDeleteGraceMins) * time.Minute,
		PhysicalDeleteSLA:   time.Duration(cfg.PhysicalDeleteSLADays) * 24 * time.Hour,
	}, logger)
	retentionLocker := &valkey.Client{Address: cfg.ValkeyAddr, Username: cfg.ValkeyUser, Password: cfg.ValkeyPassword, Timeout: 2 * time.Second}
	retentionWorker.WithLocker(retention.LockerValkey(retentionLocker), "dmp:"+tenantID.String()+":retention:lease")
	options = append(options, adapterHTTP.WithRetentionService(retentionWorker))
	go retentionWorker.Run(context.Background(), cfg.RetentionInterval)

	// Readiness fails while the store is unreachable and while the deny gate is
	// rebuilding, so traffic is never routed to an instance that could answer
	// with an unverified audience.
	options = append(options, adapterHTTP.WithReadinessCheck(func(checkCtx context.Context) error {
		if err := conn.Ping(checkCtx); err != nil {
			return fmt.Errorf("clickhouse unreachable: %w", err)
		}
		if denyGate != nil {
			epoch, err := denyGate.Recover(checkCtx, loadTombstones)
			if err != nil {
				return fmt.Errorf("deny gate not ready: %w", err)
			}
			businessMetrics.GateEpoch(epoch)
		}
		return nil
	}))

	return options, func() { _ = controlStore.Close() }, nil
}
