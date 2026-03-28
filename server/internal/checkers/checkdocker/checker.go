package checkdocker

import (
	"context"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// DockerChecker implements the Checker interface for Docker container health checks.
type DockerChecker struct{}

// Type returns the check type identifier.
func (c *DockerChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeDocker
}

// Validate checks if the configuration is valid.
func (c *DockerChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &DockerConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = resolveSpecName(cfg)
	}

	if spec.Slug == "" {
		spec.Slug = resolveSpecSlug(cfg)
	}

	return nil
}

func resolveSpecName(cfg *DockerConfig) string {
	if cfg.ContainerName != "" {
		return cfg.ContainerName
	}

	return cfg.ContainerID
}

func resolveSpecSlug(cfg *DockerConfig) string {
	if cfg.ContainerName != "" {
		return "docker-" + strings.ReplaceAll(cfg.ContainerName, ".", "-")
	}

	short := cfg.ContainerID
	if len(short) > 12 {
		short = short[:12]
	}

	return "docker-" + short
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

	timeout := cfg.resolveTimeout()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{}

	cli, err := createClient(cfg)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "failed to create Docker client: " + err.Error()},
		}, nil
	}

	defer func() { _ = cli.Close() }()

	info, err := cli.ContainerInspect(ctx, cfg.resolveContainerRef())
	if err != nil {
		return handleInspectError(ctx, err, start, metrics), nil
	}

	metrics["inspect_time_ms"] = durationMs(time.Since(start))

	return buildResult(info, start, metrics, output), nil
}

func createClient(cfg *DockerConfig) (*client.Client, error) {
	return client.NewClientWithOpts(
		client.WithHost(cfg.resolveHost()),
		client.WithAPIVersionNegotiation(),
	)
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
			Output:   map[string]any{"error": "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   map[string]any{"error": "container inspect failed: " + err.Error()},
	}
}

func buildResult(
	info container.InspectResponse,
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
