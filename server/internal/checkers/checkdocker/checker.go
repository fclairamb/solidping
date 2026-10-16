// Package checkdocker provides Docker container health checks.
package checkdocker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdocker/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const microsecondsPerMilli = 1000.0

// errLocalSocketNotDialable is returned when a local-socket docker host would
// have to go through a tunnel dialer, which only carries TCP.
var errLocalSocketNotDialable = errors.New("a local docker socket cannot be reached through this check's dialer")

// DockerChecker implements the Checker interface for Docker container health checks.
type DockerChecker struct{}

// Type returns the check type identifier.
func (c *DockerChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeDocker
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *DockerChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the Docker container health check and returns the result.
func (c *DockerChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*DockerConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.ResolveTimeout()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{}

	cli, err := createClient(ctx, cfg)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: "failed to create Docker client: " + err.Error()},
		}, nil
	}

	defer func() { _ = cli.Close() }()

	inspected, err := cli.ContainerInspect(ctx, cfg.ResolveContainerRef(), client.ContainerInspectOptions{})
	if err != nil {
		return handleInspectError(ctx, err, start, metrics), nil
	}

	metrics["inspect_time_ms"] = durationMs(time.Since(start))

	return buildResult(cfg, &inspected.Container, start, metrics, output), nil
}

// createClient builds the Docker API client for cfg's endpoint.
//
// API-version negotiation is on by default in this client, so the endpoint
// host is the main thing worth configuring: an older daemon (or Podman's
// Docker-compatible socket) is negotiated down on the first request.
//
// The client dials through checkerdef.OutboundDialer like every other check
// type (spec 2026-10-16-09): under an enforcing egress guard a tcp:// host is
// dialed by the guard, so a private target is refused with an egress denial
// instead of becoming an internal connect oracle, and a local socket
// (unix://, npipe://) is refused outright. With no guard or tunnel the client
// keeps its own transport byte-for-byte.
func createClient(ctx context.Context, cfg *DockerConfig) (*client.Client, error) {
	host := cfg.ResolveHost()
	opts := []client.Opt{client.WithHost(host)}

	if dialer := checkerdef.OutboundDialer(ctx); dialer != nil {
		hostURL, err := client.ParseHostURL(host)
		if err != nil {
			return nil, err
		}

		if hostURL.Scheme != "tcp" {
			if egress.FromContext(ctx).Enforcing() {
				denied := &egress.DeniedError{Host: host}
				egress.RecordDenial(ctx, denied)

				return nil, denied
			}

			return nil, fmt.Errorf("%w: %s", errLocalSocketNotDialable, host)
		}

		// Pin the dialed address to the configured host rather than trusting
		// whatever the HTTP transport asks for.
		addr := hostURL.Host
		opts = append(opts, client.WithDialContext(
			func(dialCtx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(dialCtx, network, addr)
			},
		))
	}

	return client.New(opts...)
}

func handleInspectError(
	ctx context.Context,
	err error,
	start time.Time,
	metrics map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   map[string]any{checkerdef.OutputKeyError: "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   map[string]any{checkerdef.OutputKeyError: "container inspect failed: " + err.Error()},
	}
}

func buildResult(
	cfg *DockerConfig,
	info *container.InspectResponse,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	output["containerName"] = info.Name
	output["containerId"] = info.ID
	output["state"] = info.State.Status
	output["image"] = info.Config.Image
	output["startedAt"] = info.State.StartedAt

	if info.State.Health != nil {
		output["healthStatus"] = info.State.Health.Status
		output["healthLog"] = lastHealthLog(info.State.Health)
	}

	metrics["restartCount"] = info.RestartCount

	if !info.State.Running {
		output["error"] = "container is not running (state: " + info.State.Status + ")"

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	if info.State.Health != nil && info.State.Health.Status != "healthy" {
		output["error"] = "container health status: " + info.State.Health.Status

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	// Restart-loop heuristic (Approach A): a container that is running at
	// inspect time but (re)started very recently with a high lifetime restart
	// count is a likely crash-loop. Detected on a running container → Warning
	// (counts as up, does not page). secondsSinceStart is always emitted when
	// detection is enabled so the dashboard can show flap context even below
	// threshold.
	if cfg.RestartLoopEnabled() && info.State.Running {
		return detectRestartLoop(cfg, info, start, metrics, output)
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func detectRestartLoop(
	cfg *DockerConfig,
	info *container.InspectResponse,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	window := cfg.ResolveRestartLoopWindow()

	started, err := time.Parse(time.RFC3339Nano, info.State.StartedAt)
	if err != nil {
		// StartedAt unparseable: cannot apply the recency guard, so the loop
		// signal can't be trusted. Fall back to Up rather than risk a false
		// Warning.
		return &checkerdef.Result{
			Status:   checkerdef.StatusUp,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	sinceStart := time.Since(started)
	output["secondsSinceStart"] = sinceStart.Seconds()

	if info.RestartCount >= cfg.RestartLoopMinRestarts && sinceStart <= window {
		output["restartLoop"] = true
		output["error"] = fmt.Sprintf(
			"restart loop suspected: %d restarts, last start %s ago",
			info.RestartCount, sinceStart.Round(time.Second),
		)

		return &checkerdef.Result{
			Status:   checkerdef.StatusWarning,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func lastHealthLog(health *container.Health) string {
	if len(health.Log) == 0 {
		return ""
	}

	last := health.Log[len(health.Log)-1]

	return last.Output
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
