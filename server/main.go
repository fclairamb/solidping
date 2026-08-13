// Package main provides the CLI entry point for the SolidPing monitoring service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"
	"go.opentelemetry.io/contrib/bridges/otelslog"

	"github.com/fclairamb/solidping/server/internal/agentmode"
	"github.com/fclairamb/solidping/server/internal/app"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/envcheck"
	"github.com/fclairamb/solidping/server/internal/memlimit"
	"github.com/fclairamb/solidping/server/internal/otelsetup"
	"github.com/fclairamb/solidping/server/internal/procwatch"
	slogutil "github.com/fclairamb/solidping/server/internal/utils/slog"
	"github.com/fclairamb/solidping/server/internal/version"
	spCli "github.com/fclairamb/solidping/server/pkg/cli"
)

const (
	// embeddedPostgresPort is the default port for embedded PostgreSQL.
	embeddedPostgresPort = 5433
)

func main() {
	// Set up logger early (before config load to ensure it's always configured)
	// Read LOG_LEVEL env var directly to configure logger before config load
	logLevel := config.ParseLogLevel(os.Getenv("LOG_LEVEL"))
	setupLogger(logLevel)

	cmd := &cli.Command{
		Name:           "solidping",
		Usage:          "SolidPing monitoring service",
		DefaultCommand: "serve",
		Commands: []*cli.Command{
			{
				Name:   "serve",
				Usage:  "Start the HTTP server",
				Action: serve,
			},
			{
				Name:   "migrate",
				Usage:  "Run database migrations",
				Action: migrate,
			},
			{
				Name:     "client",
				Usage:    "Client commands for managing SolidPing remotely",
				Flags:    spCli.GetGlobalFlags(),
				Commands: spCli.GetCommands(),
			},
			{
				Name:  "encrypt-credentials",
				Usage: "Encrypt plaintext secret fields in checks and connections (idempotent)",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "Report what would be encrypted without writing",
					},
				},
				Action: encryptCredentials,
			},
			devCommand(),
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		slog.Error("Application failed", "error", err)
		os.Exit(1)
	}
}

// setupLogger configures the default slog logger with the given level.
func setupLogger(level slog.Level) {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

//nolint:funlen // OTel initialization adds statements
func serve(ctx context.Context, _ *cli.Command) error {
	cfg, err := config.Load()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to load configuration", "error", err)
		return err
	}

	// Re-configure logger with the log level from config
	setupLogger(cfg.LogLevel)

	// Warn about unrecognized SP_* environment variables before validating: a
	// typo'd var is often *why* validation fails, so the hint must print before
	// a possible fatal exit. WARN-only — never fails startup, since unknown SP_*
	// names are legitimate in mixed fleets and check config-as-code.
	envcheck.WarnUnrecognizedEnv(ctx)

	if validationErr := cfg.Validate(); validationErr != nil {
		slog.ErrorContext(ctx, "Invalid configuration", "error", validationErr)
		return cli.Exit(validationErr.Error(), 1)
	}

	// Apply Go runtime memory guardrails (GOMEMLIMIT soft cap, GOGC) as early as
	// possible so the GC honors them for the whole process lifetime. On a
	// container with a memory limit this auto-derives a soft cap that keeps RSS
	// below the cgroup limit; off-container it is a no-op unless configured.
	memGuard := memlimit.Apply(memlimit.Config{
		MemoryLimit: cfg.Runtime.MemoryLimit,
		Auto:        cfg.Runtime.AutoMemoryLimit,
		Ratio:       cfg.Runtime.MemoryLimitRatio,
		GCPercent:   cfg.Runtime.GCPercent,
	})
	slog.InfoContext(ctx, "Runtime memory guardrails applied",
		"memoryLimit", memGuard.MemoryLimitHuman(),
		"memoryLimitSource", memGuard.MemoryLimitSource,
		"gcPercent", memGuard.GCPercent,
		"gcPercentSource", memGuard.GCPercentSource)

	// Apply user-agent from config, or use default with version
	if cfg.UserAgent != "" {
		version.UserAgent = cfg.UserAgent
	} else {
		version.UserAgent = version.DefaultUserAgent()
	}

	slog.InfoContext(ctx, "User-Agent identity", "userAgent", version.UserAgent)

	// Initialize OpenTelemetry
	otelProvider := otelsetup.NewProvider(cfg.OTel)

	logProvider, otelErr := otelProvider.Start(ctx)
	if otelErr != nil {
		slog.ErrorContext(ctx, "Failed to start OTel", "error", otelErr)
		return otelErr
	}

	defer otelProvider.Shutdown(ctx)

	// If OTel logs are enabled, add otelslog bridge via fanout
	if logProvider != nil {
		textHandler := slog.NewTextHandler(
			os.Stdout,
			&slog.HandlerOptions{Level: cfg.LogLevel},
		)
		otelHandler := otelslog.NewHandler(
			"solidping",
			otelslog.WithLoggerProvider(logProvider),
		)
		fanout := slogutil.NewFanoutHandler(
			textHandler, otelHandler,
		)
		slog.SetDefault(slog.New(fanout))
	}

	slog.InfoContext(ctx, "Configuration loaded",
		"runMode", cfg.RunMode,
		"dbType", cfg.Database.Type,
		"logSQL", cfg.Database.LogSQL)

	// Deported-agent mode (SP_NODE_ROLE=agent, spec 2026-07-16-02): the agent
	// has no database and runs no migrations — branch BEFORE any DB init. It
	// enrolls (or reconnects) over WebSocket and runs the check worker loop.
	if cfg.IsAgentMode() {
		return runAgentMode(ctx, cfg)
	}

	server, err := app.NewServer(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create server", "error", err)
		return err
	}

	// Run database migrations on startup
	slog.InfoContext(ctx, "Running database migrations...",
		"type", cfg.Database.Type)

	if initErr := server.Initialize(ctx); initErr != nil {
		slog.ErrorContext(ctx, "Failed to run migrations", "error", initErr)
		return initErr
	}

	slog.InfoContext(ctx, "Migrations completed successfully",
		"type", cfg.Database.Type)

	// Initialize system configuration from database
	if sysConfigErr := server.InitializeSystemConfig(
		ctx, cfg,
	); sysConfigErr != nil {
		slog.ErrorContext(ctx,
			"Failed to initialize system config",
			"error", sysConfigErr)
		return sysConfigErr
	}

	// Seed startup data driven by env/deployment (SaaS entitlements, named
	// regions, the first-party CLI OAuth client). All idempotent no-ops when the
	// relevant env/state is absent.
	if seedErr := seedStartupData(ctx, server); seedErr != nil {
		return seedErr
	}

	// Routes are constructed after InitializeSystemConfig so handlers see
	// the post-overlay config — e.g. PasskeyService picks up the
	// system-parameter override of server.base_url and uses it to derive
	// the WebAuthn RP ID.
	server.SetupRoutes(ctx)

	// Idempotent one-shot startup data fixups (credential encryption sweep,
	// multi-region check-job re-leveling). No-ops once the DB is already in the
	// current shape.
	if fixupErr := runStartupDataFixups(ctx, server); fixupErr != nil {
		return fixupErr
	}

	// Initialize test data if in test mode
	if testDataErr := server.InitializeTestData(ctx); testDataErr != nil {
		slog.ErrorContext(ctx,
			"Failed to initialize test data", "error", testDataErr)
		return testDataErr
	}

	// Create context that cancels on shutdown signals
	ctx, stop := signal.NotifyContext(
		ctx, syscall.SIGTERM, syscall.SIGINT,
	)
	defer stop()

	// Opt-in: die with whoever started us instead of being adopted by PID 1.
	ctx, stopParentWatch := watchParent(ctx, cfg)
	defer stopParentWatch()

	// Start server (blocks until context is canceled)
	err = server.Start(ctx)

	// Cleanup resources
	if closeErr := server.Close(ctx); closeErr != nil {
		slog.ErrorContext(ctx, "Error closing server", "error", closeErr)
	}

	// If the error is context.Canceled, it means graceful shutdown
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}

// watchParent wires the opt-in parent-death watch (SP_EXIT_WITH_PARENT): the
// returned context is canceled when the process that started this one
// disappears, so a server spawned by a test harness or an ad-hoc wrapper shuts
// down with it instead of being adopted by PID 1 and outliving its session
// (spec 2026-08-12-05). Disabled, it returns ctx untouched — a normal
// deployment is started BY a supervisor whose death is not a reason to stop.
func watchParent(ctx context.Context, cfg *config.Config) (context.Context, context.CancelFunc) {
	if !cfg.Server.ExitWithParent {
		return ctx, func() {}
	}

	watchCtx, orphaned := context.WithCancel(ctx)

	go procwatch.ParentWatcher{}.Run(watchCtx, orphaned)

	return watchCtx, orphaned
}

// runStartupDataFixups runs the idempotent one-shot data migrations that must
// happen after routes are set up but before the server accepts traffic:
//   - auto-encrypt plaintext secrets so existing self-hosted installs pick up
//     encryption transparently when the operator first sets the master key
//     (no-op when encryption is disabled or AutoMigrate=false);
//   - re-level any multi-region check_jobs still carrying the old split period
//     (basePeriod × region_count) onto the per-region full period and
//     inter-region spread (spec 2026-07-20-05).
//
// Both are idempotent no-ops once the database is already in the current shape.
func runStartupDataFixups(ctx context.Context, server *app.Server) error {
	if migrateErr := server.MaybeAutoMigrateEncryption(ctx); migrateErr != nil {
		slog.ErrorContext(ctx, "Failed to auto-migrate credentials", "error", migrateErr)

		return migrateErr
	}

	if recErr := server.ReconcileCheckJobSchedules(ctx); recErr != nil {
		slog.ErrorContext(ctx, "Failed to reconcile check job schedules", "error", recErr)

		return recErr
	}

	return nil
}

// runAgentMode runs the deported-agent loop until interrupted (spec
// 2026-07-16-02). No database, no migrations, no HTTP server.
func runAgentMode(ctx context.Context, cfg *config.Config) error {
	agentCtx, stopAgent := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stopAgent()

	if agentErr := agentmode.Run(agentCtx, cfg); agentErr != nil && !errors.Is(agentErr, context.Canceled) {
		slog.ErrorContext(ctx, "Agent stopped with error", "error", agentErr)

		return agentErr
	}

	return nil
}

// seedStartupData runs the env/deployment-driven seeds after migrations: the
// SaaS entitlements parameters, the named-regions parameter, the platform
// (system) agent enrollment tokens, and the first-party CLI OAuth client. Each
// is idempotent and a no-op when its inputs are absent.
func seedStartupData(ctx context.Context, server *app.Server) error {
	// In SaaS mode, seed the entitlements system parameters (service token +
	// upgrade URL template) from env so the billing service can authenticate
	// and the dashboard renders the upgrade link. No-op when self-hosted.
	if err := server.SeedSaaSEntitlements(ctx); err != nil {
		slog.ErrorContext(ctx, "Failed to seed SaaS entitlements parameters", "error", err)
		return err
	}

	// Seed the `regions` system parameter from SP_REGIONS so a deployment can
	// name its regions declaratively (e.g. "🇪🇺 EU1 (default)" instead of the
	// raw "default" slug). No-op when unset.
	if err := server.SeedRegionsFromEnv(ctx); err != nil {
		slog.ErrorContext(ctx, "Failed to seed regions parameter", "error", err)
		return err
	}

	// Reconcile the platform (kind='system') agent enrollment tokens from
	// SP_SYSTEM_AGENT_ENROLLMENT_TOKENS so SolidPing-operated check workers
	// running outside the cluster (fly.io) can enroll on boot. Must run AFTER
	// the regions seed: each token's region is validated against the `regions`
	// parameter. No-op when the variable is unset.
	if err := server.SeedSystemAgentEnrollmentTokens(ctx); err != nil {
		slog.ErrorContext(ctx, "Failed to seed system agent enrollment tokens", "error", err)
		return err
	}

	// Register the first-party `solidping-cli` OAuth client so `sp auth login`
	// can drive the browser authorization-code flow. Idempotent — a no-op once
	// the client exists.
	if err := server.SeedCLIOAuthClient(ctx); err != nil {
		slog.ErrorContext(ctx, "Failed to seed CLI OAuth client", "error", err)
		return err
	}

	return nil
}

func migrate(ctx context.Context, _ *cli.Command) error {
	cfg, err := config.Load()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to load configuration", "error", err)
		return err
	}

	// Re-configure logger with the log level from config
	setupLogger(cfg.LogLevel)

	if validationErr := cfg.Validate(); validationErr != nil {
		slog.ErrorContext(ctx, "Invalid configuration", "error", validationErr)
		return cli.Exit(validationErr.Error(), 1)
	}

	return runMigrations(ctx, cfg)
}

func encryptCredentials(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.Load()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to load configuration", "error", err)
		return err
	}

	setupLogger(cfg.LogLevel)

	if validationErr := cfg.Validate(); validationErr != nil {
		slog.ErrorContext(ctx, "Invalid configuration", "error", validationErr)
		return cli.Exit(validationErr.Error(), 1)
	}

	dbSvc, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := dbSvc.Close(); closeErr != nil {
			slog.ErrorContext(ctx, "Failed to close database service", "error", closeErr)
		}
	}()

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		return fmt.Errorf("init db: %w", initErr)
	}

	creds, err := app.BuildCredentialsService(cfg, dbSvc)
	if err != nil {
		return fmt.Errorf("build credentials service: %w", err)
	}

	if !creds.Enabled() {
		return cli.Exit("encryption disabled — set SP_ENCRYPTION_MASTER_KEY first", 1)
	}

	dryRun := cmd.Bool("dry-run")

	stats, err := credmigrate.Run(ctx, dbSvc, creds, credmigrate.Options{
		DryRun: dryRun,
		Logger: slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("encrypt-credentials failed: %w", err)
	}

	// Reconcile URL fields to public settings (idempotent one-shot backfill).
	recStats, recErr := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{
		DryRun: dryRun,
		Logger: slog.Default(),
	})
	if recErr != nil {
		return fmt.Errorf("encrypt-credentials reconcile failed: %w", recErr)
	}

	slog.InfoContext(ctx, "encrypt-credentials done",
		"dryRun", dryRun,
		"checksScanned", stats.ChecksScanned,
		"checksMigrated", stats.ChecksMigrated,
		"connectionsScanned", stats.ConnectionsScanned,
		"connectionsMigrated", stats.ConnectionsMigrated,
		"connectionsReconciled", recStats.ConnectionsReconciled)

	return nil
}

func openDB(ctx context.Context, cfg *config.Config) (db.Service, error) {
	switch cfg.Database.Type {
	case "postgres":
		return postgres.New(ctx, &postgres.Config{DSN: cfg.Database.URL, Embedded: false})
	case "postgres-embedded":
		return postgres.New(ctx, &postgres.Config{
			Embedded: true,
			Port:     embeddedPostgresPort,
		})
	case "sqlite":
		return sqlite.New(ctx, sqlite.Config{DataDir: cfg.Database.Dir, InMemory: false})
	case "sqlite-memory":
		return sqlite.New(ctx, sqlite.Config{InMemory: true})
	default:
		return nil, cli.Exit("Unsupported database type: "+cfg.Database.Type, 1)
	}
}

func runMigrations(ctx context.Context, cfg *config.Config) error {
	var (
		svc db.Service
		err error
	)

	switch cfg.Database.Type {
	case "postgres":
		svc, err = postgres.New(ctx, &postgres.Config{
			DSN:      cfg.Database.URL,
			Embedded: false,
		})
	case "postgres-embedded":
		svc, err = postgres.New(ctx, &postgres.Config{
			Embedded: true,
			Port:     embeddedPostgresPort,
		})
	case "sqlite":
		svc, err = sqlite.New(ctx, sqlite.Config{
			DataDir:  cfg.Database.Dir,
			InMemory: false,
		})
	case "sqlite-memory":
		svc, err = sqlite.New(ctx, sqlite.Config{
			InMemory: true,
		})
	default:
		return cli.Exit("Unsupported database type: "+cfg.Database.Type, 1)
	}

	if err != nil {
		slog.ErrorContext(ctx, "Failed to create database service", "error", err, "type", cfg.Database.Type)
		return err
	}

	defer func() {
		if closeErr := svc.Close(); closeErr != nil {
			slog.ErrorContext(ctx, "Failed to close database service", "error", closeErr)
		}
	}()

	slog.InfoContext(ctx, "Running migrations...", "type", cfg.Database.Type)

	if err := svc.Initialize(ctx); err != nil {
		slog.ErrorContext(ctx, "Failed to run migrations", "error", err)
		return err
	}

	slog.InfoContext(ctx, "Migrations completed successfully", "type", cfg.Database.Type)

	return nil
}
