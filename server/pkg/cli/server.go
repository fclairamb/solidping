package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

var (
	// ErrServerUnhealthy is returned when the server is unhealthy.
	ErrServerUnhealthy = errors.New("server is unhealthy")
	// ErrFailedToGetVersion is returned when getting version fails.
	ErrFailedToGetVersion = errors.New("failed to get version")
)

// serverHealthAction handles the health check command.
func serverHealthAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Create API client without authentication (health check doesn't require auth)
	cfg := client.Config{
		BaseURL: cliCtx.Config.URL,
		Verbose: cliCtx.Verbose,
	}
	apiClient, err := client.New(cfg)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 1)
	}

	// Call health endpoint
	resp, err := apiClient.GetHealthWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to check server health", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Server is unhealthy", resp.StatusCode())
	}

	// Output health status
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			"status": resp.JSON200.Status,
		})
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Status: %s", *resp.JSON200.Status))
	return nil
}

// serverVersionAction handles the version command.
func serverVersionAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Create API client without authentication (version doesn't require auth)
	cfg := client.Config{
		BaseURL: cliCtx.Config.URL,
		Verbose: cliCtx.Verbose,
	}
	apiClient, err := client.New(cfg)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 1)
	}

	// Call version endpoint
	resp, err := apiClient.GetVersionWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to get server version", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get version", resp.StatusCode())
	}

	// Output version
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			"version": resp.JSON200.Version,
		})
	}

	output.PrintMessage(os.Stdout, "Version: "+*resp.JSON200.Version)
	return nil
}
