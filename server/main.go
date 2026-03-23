// Package main provides the CLI entry point for the SolidPing monitoring service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"
	"go.opentelemetry.io/contrib/bridges/otelslog"

	"github.com/fclairamb/solidping/server/internal/app"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/otelsetup"
	slogutil "github.com/fclairamb/solidping/server/internal/utils/slog"
	"github.com/fclairamb/solidping/server/internal/version"
	spCli "github.com/fclairamb/solidping/server/pkg/cli"
)

const (
	// embeddedPostgresPort is the default port for embedded PostgreSQL.
	embeddedPostgresPort = 5434
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

	if validationErr := cfg.Validate(); validationErr != nil {
		slog.ErrorContext(ctx, "Invalid configuration", "error", validationErr)
		return cli.Exit(validationErr.Error(), 1)
	}

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

func runMigrations(ctx context.Context, cfg *config.Config) error {
	var (
		svc db.Service
		err error
	)

	switch cfg.Database.Type {
	case "postgres":
		svc, err = postgres.New(ctx, postgres.Config{
			DSN:      cfg.Database.URL,
			Embedded: false,
		})
	case "postgres-embedded":
		svc, err = postgres.New(ctx, postgres.Config{
			Embedded:    true,
			EmbeddedDir: "/tmp/solidping-postgres-test",
			Port:        embeddedPostgresPort,
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
