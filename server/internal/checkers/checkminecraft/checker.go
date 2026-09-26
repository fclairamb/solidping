// Package checkminecraft provides Minecraft server health monitoring (Java + Bedrock).
package checkminecraft

import (
	"context"
	"net"
	"time"

	"github.com/dreamscached/minequery/v2"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkminecraft/config"
)

const microsecondsPerMilli = 1000.0

// MinecraftChecker implements the Checker interface for Minecraft server health checks.
type MinecraftChecker struct{}

// Type returns the check type identifier.
func (c *MinecraftChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMinecraft
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *MinecraftChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the Minecraft server health check and returns the result.
func (c *MinecraftChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*MinecraftConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: cfg.ResolvePort(),
		"edition":                cfg.ResolveEdition(),
	}

	metrics := map[string]any{}

	var queryErr error
	if cfg.ResolveEdition() == EditionBedrock {
		queryErr = pingBedrock(ctx, cfg, metrics, output)
	} else {
		queryErr = pingJava(ctx, cfg, metrics, output)
	}

	if queryErr != nil {
		status := checkerdef.StatusDown
		errMsg := "minecraft query failed: " + queryErr.Error()

		if ctx.Err() != nil {
			status = checkerdef.StatusTimeout
			errMsg = "query timeout"
		}

		return &checkerdef.Result{
			Status:   status,
			Duration: time.Since(start),
			Output:   map[string]any{"error": errMsg},
		}, nil
	}

	metrics["query_time_ms"] = durationMs(time.Since(start))

	return applyThresholds(cfg, start, metrics, output), nil
}

func pingJava(ctx context.Context, cfg *MinecraftConfig, metrics, output map[string]any) error {
	// minequery resolves (and follows SRV records) by itself, so the egress
	// guard (spec 2026-09-25-19) rides its *net.Dialer as a Control hook that
	// judges the literal address of the connect.
	pinger := minequery.NewPinger(minequery.WithDialer(
		checkerdef.GuardedNetDialer(ctx, &net.Dialer{Timeout: cfg.ResolveTimeout()}),
	))

	status, err := pinger.Ping17(cfg.Host, cfg.ResolvePort())
	if err != nil {
		return err
	}

	metrics["players"] = status.OnlinePlayers
	metrics["maxPlayers"] = status.MaxPlayers
	metrics["protocol"] = status.ProtocolVersion

	output["motd"] = status.Description.String()
	output["version"] = status.VersionName
	output["protocol"] = status.ProtocolVersion
	output["players"] = status.OnlinePlayers
	output["maxPlayers"] = status.MaxPlayers

	if len(status.SamplePlayers) > 0 {
		names := make([]string, 0, len(status.SamplePlayers))
		for i := range status.SamplePlayers {
			names = append(names, status.SamplePlayers[i].Nickname)
		}

		output["samplePlayers"] = names
	}

	return nil
}

func pingBedrock(ctx context.Context, cfg *MinecraftConfig, metrics, output map[string]any) error {
	status, err := bedrockUnconnectedPing(ctx, cfg.Host, cfg.ResolvePort(), cfg.ResolveTimeout())
	if err != nil {
		return err
	}

	metrics["players"] = status.OnlinePlayers
	metrics["maxPlayers"] = status.MaxPlayers
	metrics["protocol"] = status.ProtocolVersion

	output["motd"] = status.MOTD
	output["serverName"] = status.ServerName
	output["version"] = status.MinecraftVersion
	output["protocol"] = status.ProtocolVersion
	output["players"] = status.OnlinePlayers
	output["maxPlayers"] = status.MaxPlayers

	if status.GameMode != "" {
		output["gameMode"] = status.GameMode
	}

	return nil
}

func applyThresholds(
	cfg *MinecraftConfig,
	start time.Time,
	metrics, output map[string]any,
) *checkerdef.Result {
	players, _ := metrics["players"].(int)

	if cfg.MinPlayers > 0 && players < cfg.MinPlayers {
		output["error"] = "player count below minimum"

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	if cfg.MaxPlayers > 0 && players > cfg.MaxPlayers {
		output["error"] = "player count above maximum"

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
