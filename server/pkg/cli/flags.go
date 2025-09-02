package cli

import (
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/defaults"
)

// GetGlobalFlags returns the global flags available for all commands.
func GetGlobalFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "config",
			Usage:   "Path to config file",
			Value:   "~/.config/solidping/settings.json",
			Sources: cli.EnvVars("SOLIDPING_CONFIG"),
		},
		&cli.StringFlag{
			Name:    "url",
			Usage:   "Override server URL from config",
			Value:   defaults.ServerURL,
			Sources: cli.EnvVars("SOLIDPING_URL"),
		},
		&cli.StringFlag{
			Name:    "org",
			Usage:   "Organization name (overrides config value)",
			Value:   defaults.Organization,
			Sources: cli.EnvVars("SOLIDPING_ORG"),
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Output format: text, json, jsonl",
			Value:   "text",
			Sources: cli.EnvVars("SOLIDPING_OUTPUT"),
		},
		&cli.BoolFlag{
			Name:   "json",
			Usage:  "Output in JSON format (alias for -o json)",
			Hidden: true,
		},
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "Enable verbose logging (shows HTTP requests/responses)",
			Sources: cli.EnvVars("SOLIDPING_VERBOSE"),
		},
	}
}
