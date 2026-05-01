package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/pkg/cli/apihelper"
	"github.com/fclairamb/solidping/server/pkg/cli/config"
	"github.com/fclairamb/solidping/server/pkg/cli/output"
)

// errStatusError is returned when an API call returns an unexpected HTTP status code.
var errStatusError = errors.New("unexpected status")

// Context holds the context for CLI commands.
type Context struct {
	Config       *config.Config
	APIHelper    *apihelper.Helper
	Outputter    output.Outputter
	OutputFormat output.Format
	Verbose      bool
}

// NewCLIContext creates a new CLI context from command flags.
func NewCLIContext(cmd *cli.Command) (*Context, error) {
	// Get config path
	configPath := cmd.String("config")
	if configPath == "" {
		var err error
		configPath, err = config.DefaultConfigPath()
		if err != nil {
			return nil, fmt.Errorf("failed to get default config path: %w", err)
		}
	}

	// Load configuration - use defaults if loading fails
	cfg, err := config.Load(configPath)
	if err != nil {
		// If config loading fails, create default config in memory
		cfg = &config.Config{
			URL: defaults.ServerURL,
			Org: defaults.Organization,
			Auth: config.Auth{
				Email:    defaults.Email,
				Password: defaults.Password,
			},
		}
	}

	// Override URL if provided via flag
	if url := cmd.String(flagURL); url != "" {
		cfg.URL = url
	}

	// Override org if provided via flag
	if org := cmd.String("org"); org != "" {
		cfg.Org = org
	}

	// Get token path
	tokenPath, err := config.TokenPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get token path: %w", err)
	}

	// Get verbose flag
	// Check both the command flag and environment variable
	verbose := cmd.Bool("verbose")
	if !verbose {
		// Fallback to environment variable if flag is not set
		envVal := os.Getenv("SOLIDPING_VERBOSE")
		verbose = envVal == "1" || envVal == "true"
	}

	// Create API helper
	helper := apihelper.NewHelper(cfg, tokenPath, verbose)

	// Resolve output format from -o flag or --json alias
	outputFormat := output.Format(cmd.String("output"))
	if cmd.Bool("json") && outputFormat == output.FormatText {
		outputFormat = output.FormatJSON
	}

	// Create outputter
	outputter := output.NewOutputter(outputFormat, os.Stdout)

	return &Context{
		Config:       cfg,
		APIHelper:    helper,
		Outputter:    outputter,
		OutputFormat: outputFormat,
		Verbose:      verbose,
	}, nil
}

// IsText returns true if the output format is text (human-readable).
func (c *Context) IsText() bool {
	return c.OutputFormat == output.FormatText
}

// HandleAuthError handles authentication errors consistently across output formats.
func (c *Context) HandleAuthError(err error) error {
	if !c.IsText() {
		return c.Outputter.PrintError(err)
	}
	output.PrintError(os.Stdout, err.Error())
	return cli.Exit("", 3)
}

// HandleError handles general errors consistently across output formats.
func (c *Context) HandleError(msg string, err error) error {
	if !c.IsText() {
		return c.Outputter.PrintError(fmt.Errorf("%s: %w", msg, err))
	}
	output.PrintError(os.Stdout, fmt.Sprintf("%s: %v", msg, err))
	return cli.Exit("", 1)
}

// HandleStatusError handles unexpected HTTP status code errors.
func (c *Context) HandleStatusError(msg string, statusCode int) error {
	if !c.IsText() {
		return c.Outputter.PrintError(fmt.Errorf("%s: %w (status: %d)", msg, errStatusError, statusCode))
	}
	output.PrintError(os.Stdout, fmt.Sprintf("%s (status: %d)", msg, statusCode))
	return cli.Exit("", 1)
}

// GetOrg returns the organization from config or flag override.
func (c *Context) GetOrg() string {
	if c.Config.Org == "" {
		return defaults.Organization
	}
	return c.Config.Org
}

// GetClient returns an authenticated API client.
func (c *Context) GetClient(_ context.Context) (*apihelper.Helper, error) {
	return c.APIHelper, nil
}
