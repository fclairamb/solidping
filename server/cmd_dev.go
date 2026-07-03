package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/devtools/browsertosleep"
)

// devCommand groups guarded developer/operational data helpers. These mutate
// data in the running instance and must never touch production.
func devCommand() *cli.Command {
	return &cli.Command{
		Name:  "dev",
		Usage: "Developer/operational data helpers (guarded; never run against production)",
		Commands: []*cli.Command{
			{
				Name: "browser-to-sleep",
				Usage: "Convert every browser check into a synthetic sleep check, " +
					"seeding sleep_ms from each check's cost_ewma_ms (dev/test only)",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "yes",
						Usage: "Confirm the mutation (required — it rewrites check definitions)",
					},
				},
				Action: browserToSleep,
			},
		},
	}
}

// browserToSleep is the guarded action behind `solidping dev browser-to-sleep`.
func browserToSleep(ctx context.Context, cmd *cli.Command) error {
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

	// Guard 1: never against a SaaS (production multi-tenant) deployment. This
	// is an operational data mutation, not a schema migration.
	if cfg.Deployment.Mode == config.DeploymentModeSaaS {
		return cli.Exit(
			"refusing to run browser-to-sleep against a SaaS deployment "+
				"(this is a dev/test-only data helper)", 1)
	}

	// Guard 2: require an explicit --yes so it cannot fire by accident.
	if !cmd.Bool("yes") {
		return cli.Exit(
			"refusing to run without --yes: this rewrites every browser check "+
				"into a synthetic sleep check (reversible only via DB reset in dev)", 1)
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

	conv := browsertosleep.New(dbSvc, slog.Default())

	res, err := conv.Run(ctx)
	if err != nil {
		return fmt.Errorf("browser-to-sleep failed: %w", err)
	}

	slog.InfoContext(ctx, "browser-to-sleep done",
		"checksConverted", res.ChecksConverted,
		"jobsConverted", res.JobsConverted)

	return nil
}
