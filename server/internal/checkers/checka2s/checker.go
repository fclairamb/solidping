// Package checka2s provides Source engine A2S protocol monitoring.
package checka2s

import (
	"context"
	"time"

	"github.com/rumblefrog/go-a2s"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checka2s/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// A2SChecker implements the Checker interface for Source engine A2S query checks.
type A2SChecker struct{}

// Type returns the check type identifier.
func (c *A2SChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeA2S
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *A2SChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the A2S query and returns the result.
func (c *A2SChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*A2SConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: cfg.ResolvePort(),
	}

	info, queryErr := queryServer(cfg)
	if queryErr != nil {
		status := checkerdef.StatusDown
		errMsg := "A2S query failed: " + queryErr.Error()

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

	return buildResult(cfg, info, start, metrics, output), nil
}

func queryServer(cfg *A2SConfig) (*a2s.ServerInfo, error) {
	client, err := a2s.NewClient(
		cfg.ResolveTarget(),
		a2s.SetMaxPacketSize(14000),
		a2s.TimeoutOption(cfg.ResolveTimeout()),
	)
	if err != nil {
		return nil, err
	}

	defer func() { _ = client.Close() }()

	return client.QueryInfo()
}

func buildResult(
	cfg *A2SConfig,
	info *a2s.ServerInfo,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	metrics["players"] = int(info.Players)
	metrics["maxPlayers"] = int(info.MaxPlayers)
	metrics["bots"] = int(info.Bots)

	output["serverName"] = info.Name
	output["map"] = info.Map
	output["game"] = info.Game
	output["players"] = int(info.Players)
	output["maxPlayers"] = int(info.MaxPlayers)
	output["bots"] = int(info.Bots)
	output["passwordProtected"] = info.Visibility
	output["vac"] = info.VAC

	// Check player count thresholds
	if cfg.MinPlayers > 0 && int(info.Players) < cfg.MinPlayers {
		output["error"] = "player count below minimum"

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	if cfg.MaxPlayers > 0 && int(info.Players) > cfg.MaxPlayers {
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
