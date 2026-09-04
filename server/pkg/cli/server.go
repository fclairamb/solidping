package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

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

// serverLimitsAction shows the server's rate-limit / concurrency configuration
// and the caller's live counters. The endpoint is public (no auth required).
func serverLimitsAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

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

	resp, err := apiClient.GetLimitsWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to get server limits", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get server limits", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderLimits(resp.JSON200)
	return nil
}

// renderLimits prints the limits response in human-readable form.
func renderLimits(limits *client.LimitsResponse) {
	output.PrintMessage(os.Stdout, "Rate limit:")
	output.PrintMessage(os.Stdout, "  Enabled:            "+strconv.FormatBool(limits.RateLimit.Enabled))

	if limits.RateLimit.RequestsPerMinute != nil {
		output.PrintMessage(os.Stdout, "  Requests/minute:    "+strconv.Itoa(*limits.RateLimit.RequestsPerMinute))
	}

	if limits.RateLimit.Burst != nil {
		output.PrintMessage(os.Stdout, "  Burst:              "+strconv.Itoa(*limits.RateLimit.Burst))
	}

	if limits.RateLimit.CallerRemaining != nil {
		remaining := strconv.FormatFloat(*limits.RateLimit.CallerRemaining, 'f', 1, 64)
		output.PrintMessage(os.Stdout, "  Caller remaining:   "+remaining)
	}

	output.PrintMessage(os.Stdout, "Concurrency:")
	output.PrintMessage(os.Stdout, "  Enabled:            "+strconv.FormatBool(limits.Concurrency.Enabled))

	if limits.Concurrency.Max != nil {
		output.PrintMessage(os.Stdout, "  Max:                "+strconv.Itoa(*limits.Concurrency.Max))
	}

	if limits.Concurrency.CallerInFlight != nil {
		output.PrintMessage(os.Stdout, "  Caller in-flight:   "+strconv.Itoa(*limits.Concurrency.CallerInFlight))
	}

	output.PrintMessage(os.Stdout, "Caller IP:            "+limits.CallerIp)
	output.PrintMessage(os.Stdout, "Caller bucket:        "+limits.CallerBucket)
}

// serverMemoryAction shows a runtime memory snapshot (super-admin only).
func serverMemoryAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetMemoryWithResponse(ctx, nil)
	if err != nil {
		return cliCtx.HandleError("Failed to get memory snapshot", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get memory snapshot", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderMemory(&resp.JSON200.Data)
	return nil
}

// renderMemory prints a memory snapshot in human-readable form.
func renderMemory(snap *client.MemorySnapshot) {
	if stats := snap.Runtime; stats != nil {
		output.PrintMessage(os.Stdout, "Runtime:")
		output.PrintMessage(os.Stdout, "  Heap alloc bytes:   "+optInt64(stats.HeapAllocBytes))
		output.PrintMessage(os.Stdout, "  Heap inuse bytes:   "+optInt64(stats.HeapInuseBytes))
		output.PrintMessage(os.Stdout, "  Heap objects:       "+optInt64(stats.HeapObjects))
		output.PrintMessage(os.Stdout, "  Stack inuse bytes:  "+optInt64(stats.StackInuseBytes))
		output.PrintMessage(os.Stdout, "  Sys bytes:          "+optInt64(stats.SysBytes))
		output.PrintMessage(os.Stdout, "  Goroutines:         "+optIntPtr(stats.NumGoroutine))
		output.PrintMessage(os.Stdout, "  NumGC:              "+optInt64(stats.NumGc))
		output.PrintMessage(os.Stdout, "  GOMEMLIMIT bytes:   "+optInt64(stats.GoMemLimitBytes))
		output.PrintMessage(os.Stdout, "  GOMAXPROCS:         "+optIntPtr(stats.GoMaxProcs))
	}

	if proc := snap.Process; proc != nil {
		output.PrintMessage(os.Stdout, "Process:")
		output.PrintMessage(os.Stdout, "  RSS bytes:          "+optInt64(proc.RssBytes))
	}

	if sub := snap.Subsystems; sub != nil {
		output.PrintMessage(os.Stdout, "Subsystems:")
		output.PrintMessage(os.Stdout, "  DEK cache entries:  "+optIntPtr(sub.DekCacheEntries))
		output.PrintMessage(os.Stdout, "  Rate-limit entries: "+optIntPtr(sub.RateLimitEntries))
		output.PrintMessage(os.Stdout, "  Event listeners:    "+optIntPtr(sub.EventListeners))
	}

	if build := snap.Build; build != nil {
		output.PrintMessage(os.Stdout, "Build:")
		if build.CgoEnabled != nil {
			output.PrintMessage(os.Stdout, "  CGO enabled:        "+strconv.FormatBool(*build.CgoEnabled))
		}
		output.PrintMessage(os.Stdout, "  SQLite driver:      "+safeStr(build.SqliteDriver))
		output.PrintMessage(os.Stdout, "  Go version:         "+safeStr(build.GoVersion))
	}
}

// serverCostDistributionAction shows the scheduler cost/delay distribution
// (super-admin only).
func serverCostDistributionAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.GetCostDistributionParams{}
	if cmd.IsSet(flagThresholdMs) {
		threshold := cmd.Int(flagThresholdMs)
		params.ThresholdMs = &threshold
	}

	resp, err := apiClient.GetCostDistributionWithResponse(ctx, params)
	if err != nil {
		return cliCtx.HandleError("Failed to get cost distribution", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get cost distribution", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil {
		output.PrintMessage(os.Stdout, "No cost distribution data")
		return nil
	}

	renderCostDistribution(resp.JSON200.Data)
	return nil
}

// renderCostDistribution prints the cost/delay distribution in text form.
func renderCostDistribution(dist *client.CostDistribution) {
	output.PrintMessage(os.Stdout, "Total jobs:  "+optIntPtr(dist.TotalJobs))
	output.PrintMessage(os.Stdout, "Threshold:   "+optIntPtr(dist.ThresholdMs)+" ms")
	output.PrintMessage(os.Stdout, "Fast jobs:   "+optIntPtr(dist.FastJobs))
	output.PrintMessage(os.Stdout, "Slow jobs:   "+optIntPtr(dist.SlowJobs))
	renderPercentiles("cost_ewma_ms", dist.CostEwmaMs)
	renderPercentiles("delay_ewma_ms", dist.DelayEwmaMs)
}

// renderPercentiles prints a labeled p50/p90/p99/max row.
func renderPercentiles(label string, pct *client.CostDistributionPercentiles) {
	if pct == nil {
		return
	}

	output.PrintMessage(os.Stdout, label+" (ms):")
	output.PrintMessage(os.Stdout, "  p50: "+optFloat(pct.P50))
	output.PrintMessage(os.Stdout, "  p90: "+optFloat(pct.P90))
	output.PrintMessage(os.Stdout, "  p99: "+optFloat(pct.P99))
	output.PrintMessage(os.Stdout, "  max: "+optFloat(pct.Max))
}

// optInt64 renders an optional int64 pointer, using "-" when nil.
func optInt64(value *int64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatInt(*value, 10)
}

// optIntPtr renders an optional int pointer, using "-" when nil.
func optIntPtr(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}

// optFloat renders an optional float64 pointer, using "-" when nil.
func optFloat(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'f', 1, 64)
}
