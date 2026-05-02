// Package checkminecraft provides Minecraft server health monitoring (Java + Bedrock).
package checkminecraft

import (
	"context"
	"time"

	"github.com/dreamscached/minequery/v2"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// MinecraftChecker implements the Checker interface for Minecraft server health checks.
type MinecraftChecker struct{}

// Type returns the check type identifier.
func (c *MinecraftChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMinecraft
}

// Validate checks if the configuration is valid.
func (c *MinecraftChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &MinecraftConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = cfg.resolveTarget()
	}

	if spec.Slug == "" {
		spec.Slug = cfg.resolveSlug()
	}

	return nil
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
		checkerdef.OutputKeyPort: cfg.resolvePort(),
		"edition":                cfg.resolveEdition(),
	}

	metrics := map[string]any{}

	var queryErr error
	if cfg.resolveEdition() == EditionBedrock {
		queryErr = pingBedrock(ctx, cfg, metrics, output)
	} else {
		queryErr = pingJava(cfg, metrics, output)
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

func pingJava(cfg *MinecraftConfig, metrics, output map[string]any) error {
	pinger := minequery.NewPinger(minequery.WithTimeout(cfg.resolveTimeout()))

	status, err := pinger.Ping17(cfg.Host, cfg.resolvePort())
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
	status, err := bedrockUnconnectedPing(ctx, cfg.Host, cfg.resolvePort(), cfg.resolveTimeout())
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
