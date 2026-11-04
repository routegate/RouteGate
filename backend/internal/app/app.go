package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/config"
	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/db"
	"github.com/ikaevus/routegate/backend/internal/delivery"
	"github.com/ikaevus/routegate/backend/internal/geoip"
	routegatehttp "github.com/ikaevus/routegate/backend/internal/http"
	"github.com/ikaevus/routegate/backend/internal/maintenance"
	"github.com/ikaevus/routegate/backend/internal/observability"
	"github.com/ikaevus/routegate/backend/internal/servers"
	"github.com/ikaevus/routegate/backend/internal/updates"
)

type App struct {
	cfg    config.Config
	logger *slog.Logger
	pool   *pgxpool.Pool
	server *routegatehttp.Server
}

func New(cfg config.Config, logger *slog.Logger) *App {
	return &App{
		cfg:    cfg,
		logger: logger,
	}
}

func (a *App) Start(ctx context.Context) error {
	if err := checkStartupLatch("/var/lib/routegate-credential-mode"); err != nil {
		return fmt.Errorf("Manager startup withheld: %w", err)
	}
	a.logger.Info("starting RouteGate Manager", "env", a.cfg.Env, "addr", a.cfg.HTTPAddr)

	pool, err := db.Connect(ctx, a.cfg.DatabaseURL, a.logger)
	if err != nil {
		return err
	}

	a.pool = pool

	if err := db.PrepareSchema(ctx, pool, "migrations", a.cfg.DatabaseMigrationMode, a.logger); err != nil {
		pool.Close()
		return err
	}
	a.backfillClientSettings(ctx, pool)
	if err := updates.RecoverInterruptedJobs(ctx, a.logger, pool); err != nil {
		return err
	}
	if err := maintenance.RecoverInterruptedPlans(ctx, a.logger, pool); err != nil {
		return err
	}
	if err := delivery.EnsureProviderSecretStore(ctx, pool, a.cfg, a.logger); err != nil {
		return err
	}

	authRepo := auth.NewRepository(pool)
	if err := authRepo.EnsureBuiltIns(ctx); err != nil {
		return err
	}
	hasSuperAdmin, err := authRepo.HasSuperAdmin(ctx)
	if err != nil {
		return err
	}
	if !hasSuperAdmin {
		if a.cfg.BootstrapAdminEmail == "" || a.cfg.BootstrapAdminPassword == "" {
			a.logger.Warn("no super_admin user exists; set ROUTEGATE_BOOTSTRAP_ADMIN_EMAIL and ROUTEGATE_BOOTSTRAP_ADMIN_PASSWORD to bootstrap the first SuperAdmin")
		} else if err := authRepo.CreateBootstrapSuperAdmin(ctx, a.cfg.BootstrapAdminEmail, a.cfg.BootstrapAdminUsername, a.cfg.BootstrapAdminPassword, a.cfg.BootstrapAdminDisplayName); err != nil {
			return err
		} else {
			a.logger.Info("bootstrapped first SuperAdmin", "email", a.cfg.BootstrapAdminEmail)
		}
	}

	a.server = routegatehttp.NewServer(a.cfg, a.logger, pool)
	deliveryWorker := delivery.NewConfiguredWorker(a.cfg, a.logger, pool)
	healthWorker := observability.NewHealthWorker(a.logger, pool)
	alertWorker := observability.NewAlertEngine(a.logger, pool)
	notificationWorker := observability.NewNotificationWorker(
		observability.NewNotificationRepository(pool),
		delivery.NewConfiguredSystemNotificationCreator(a.logger, pool),
		a.logger,
	)
	diagnosticWorker := observability.NewDiagnosticWorker(a.logger, pool)
	var geoIPWorker *geoip.Worker
	if a.cfg.GeoIP.Enabled {
		geoIPWorker = geoip.NewWorker(a.logger, servers.NewRepository(pool), geoip.NewIPWhoisResolver(nil))
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 7)
	go func() { errCh <- a.server.Start(runtimeCtx) }()
	go func() { errCh <- deliveryWorker.Run(runtimeCtx) }()
	go func() { errCh <- healthWorker.Run(runtimeCtx) }()
	go func() { errCh <- alertWorker.Run(runtimeCtx) }()
	go func() { errCh <- notificationWorker.Run(runtimeCtx) }()
	go func() { errCh <- diagnosticWorker.Run(runtimeCtx) }()
	if geoIPWorker != nil {
		go func() { errCh <- geoIPWorker.Run(runtimeCtx) }()
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		cancel()
		return err
	}
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("stopping RouteGate Manager")

	if a.server != nil {
		if err := a.server.Stop(ctx); err != nil {
			return err
		}
	}

	if a.pool != nil {
		a.pool.Close()
	}

	return nil
}

// backfillClientSettings gives config versions rendered before client
// snapshots existed a snapshot derived from their own rendered config, so
// subscriptions for those nodes follow what was actually applied. It only
// fills empty snapshots and never blocks startup: a version that cannot be
// derived keeps serving its node's saved settings, as before this change.
func (a *App) backfillClientSettings(ctx context.Context, pool *pgxpool.Pool) {
	filled, failures, err := configs.NewRepository(pool).BackfillClientSettings(ctx)
	if err != nil {
		a.logger.Error("backfill applied client settings failed", "filled", filled, "error", err)
		return
	}
	if filled > 0 {
		a.logger.Info("backfilled applied client settings", "config_versions", filled)
	}
	for _, failure := range failures {
		level := slog.LevelWarn
		if failure.Active {
			level = slog.LevelError
		}
		a.logger.Log(ctx, level, "config version has no derivable client settings; its node keeps serving saved settings to clients",
			"server_id", failure.ServerID, "config_version_id", failure.VersionID, "version", failure.Version,
			"active", failure.Active, "reason", failure.Reason)
	}
}
