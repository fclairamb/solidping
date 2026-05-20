// Package main provides the scenario driver CLI for SolidPing.
// It runs named YAML scenarios against a live SolidPing instance
// to validate end-to-end notification delivery.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	clilib "github.com/urfave/cli/v3"
)

func main() {
	app := &clilib.Command{
		Name:    "solidping-scenario",
		Usage:   "Run end-to-end scenarios against a live SolidPing instance",
		Version: "1.0.0",
		Commands: []*clilib.Command{
			runCommand(),
			listCommand(),
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

func runCommand() *clilib.Command {
	return &clilib.Command{
		Name:  "run",
		Usage: "Run a scenario against a live SolidPing instance",
		Flags: []clilib.Flag{
			&clilib.StringFlag{
				Name:    "server",
				Usage:   "SolidPing server URL",
				Value:   "http://localhost:4000",
				Sources: clilib.EnvVars("SP_SERVER"),
			},
			&clilib.StringFlag{
				Name:    "org",
				Usage:   "Organization slug",
				Value:   "default",
				Sources: clilib.EnvVars("SP_ORG"),
			},
			&clilib.StringFlag{
				Name:    "token",
				Usage:   "Personal Access Token",
				Sources: clilib.EnvVars("SP_TOKEN"),
			},
			&clilib.StringFlag{
				Name:  "scenario",
				Usage: "Path to scenario YAML file",
			},
			&clilib.StringFlag{
				Name:  "listen",
				Usage: "Webhook receiver listen address",
				Value: ":9876",
			},
			&clilib.StringFlag{
				Name:  "public-url",
				Usage: "Public URL of the webhook receiver (required when --server is not localhost)",
			},
			&clilib.DurationFlag{
				Name:  "timeout",
				Usage: "Global scenario timeout",
				Value: 0,
			},
			&clilib.StringFlag{
				Name:  "junit",
				Usage: "Path to write JUnit XML results (optional)",
			},
		},
		Action: func(ctx context.Context, cmd *clilib.Command) error {
			serverURL := cmd.String("server")
			org := cmd.String("org")
			token := cmd.String("token")
			scenarioFile := cmd.String("scenario")
			listen := cmd.String("listen")
			publicURL := cmd.String("public-url")
			globalTimeout := cmd.Duration("timeout")
			junitPath := cmd.String("junit")

			if token == "" {
				return fmt.Errorf("--token is required")
			}

			if scenarioFile == "" {
				return fmt.Errorf("--scenario is required")
			}

			// Default public URL to server when on localhost.
			if publicURL == "" {
				publicURL = "http://localhost" + listen
			}

			cfg := RunConfig{
				ServerURL:     serverURL,
				Org:           org,
				Token:         token,
				ScenarioFile:  scenarioFile,
				Listen:        listen,
				PublicURL:     publicURL,
				GlobalTimeout: globalTimeout,
				JUnitPath:     junitPath,
			}

			return RunScenario(ctx, cfg)
		},
	}
}

func listCommand() *clilib.Command {
	return &clilib.Command{
		Name:  "list",
		Usage: "List available built-in scenario files",
		Action: func(_ context.Context, _ *clilib.Command) error {
			scenarios := []struct {
				name string
				desc string
			}{
				{
					name: "incident-open-close.yaml",
					desc: "Heartbeat goes down → webhook received → heartbeat recovers → resolved webhook received",
				},
				{
					name: "oncall-rotation.yaml",
					desc: "On-call schedule target receives notification when check goes down",
				},
				{
					name: "multi-step-repeat.yaml",
					desc: "Multi-step escalation policy with repeat — asserts three webhook events",
				},
			}

			fmt.Println("Available built-in scenarios:")
			fmt.Println()

			for _, s := range scenarios {
				fmt.Printf("  %-35s %s\n", s.name, s.desc)
			}

			return nil
		},
	}
}
