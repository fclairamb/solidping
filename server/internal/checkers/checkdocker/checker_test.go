package checkdocker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// inspectOpts configures a synthetic container.InspectResponse for buildResult.
type inspectOpts struct {
	running      bool
	state        string
	restartCount int
	startedAt    string
	healthStatus string // "" → no Health block
}

func makeInspect(o inspectOpts) container.InspectResponse {
	state := &container.State{
		Status:    container.ContainerState(o.state),
		Running:   o.running,
		StartedAt: o.startedAt,
	}

	if o.healthStatus != "" {
		state.Health = &container.Health{Status: o.healthStatus}
	}

	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:           "abc123",
			Name:         "/test-container",
			State:        state,
			RestartCount: o.restartCount,
		},
		Config: &container.Config{Image: "test:latest"},
	}
}

func TestBuildResult_RestartLoopDetection(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	recentStart := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	oldStart := now.Add(-10 * time.Minute).Format(time.RFC3339Nano)

	tests := []struct {
		name            string
		cfg             *DockerConfig
		inspect         inspectOpts
		wantStatus      checkerdef.Status
		wantRestartLoop bool // expect output["restartLoop"] == true
		wantSecondsKey  bool // expect output["secondsSinceStart"] present
	}{
		{
			name: "loop: running, count >= min, started within window → Warning",
			cfg:  &DockerConfig{RestartLoopMinRestarts: 3, RestartLoopWindow: 120 * time.Second},
			inspect: inspectOpts{
				running: true, state: "running", restartCount: 5, startedAt: recentStart,
			},
			wantStatus:      checkerdef.StatusWarning,
			wantRestartLoop: true,
			wantSecondsKey:  true,
		},
		{
			name: "recency guard: high count but started older than window → Up",
			cfg:  &DockerConfig{RestartLoopMinRestarts: 3, RestartLoopWindow: 120 * time.Second},
			inspect: inspectOpts{
				running: true, state: "running", restartCount: 99, startedAt: oldStart,
			},
			wantStatus:      checkerdef.StatusUp,
			wantRestartLoop: false,
			wantSecondsKey:  true,
		},
		{
			name: "count floor: recent start but count < min → Up",
			cfg:  &DockerConfig{RestartLoopMinRestarts: 3, RestartLoopWindow: 120 * time.Second},
			inspect: inspectOpts{
				running: true, state: "running", restartCount: 1, startedAt: recentStart,
			},
			wantStatus:      checkerdef.StatusUp,
			wantRestartLoop: false,
			wantSecondsKey:  true,
		},
		{
			name: "default window applied when enabled and window unset",
			cfg:  &DockerConfig{RestartLoopMinRestarts: 3},
			inspect: inspectOpts{
				running: true, state: "running", restartCount: 5, startedAt: recentStart,
			},
			wantStatus:      checkerdef.StatusWarning,
			wantRestartLoop: true,
			wantSecondsKey:  true,
		},
		{
			name: "disabled (minRestarts == 0): no detection, no secondsSinceStart → Up",
			cfg:  &DockerConfig{},
			inspect: inspectOpts{
				running: true, state: "running", restartCount: 99, startedAt: recentStart,
			},
			wantStatus:      checkerdef.StatusUp,
			wantRestartLoop: false,
			wantSecondsKey:  false,
		},
		{
			name: "not running short-circuits to Down before loop branch",
			cfg:  &DockerConfig{RestartLoopMinRestarts: 3, RestartLoopWindow: 120 * time.Second},
			inspect: inspectOpts{
				running: false, state: "exited", restartCount: 99, startedAt: recentStart,
			},
			wantStatus:      checkerdef.StatusDown,
			wantRestartLoop: false,
			wantSecondsKey:  false,
		},
		{
			name: "unhealthy short-circuits to Down before loop branch",
			cfg:  &DockerConfig{RestartLoopMinRestarts: 3, RestartLoopWindow: 120 * time.Second},
			inspect: inspectOpts{
				running: true, state: "running", restartCount: 99, startedAt: recentStart,
				healthStatus: "unhealthy",
			},
			wantStatus:      checkerdef.StatusDown,
			wantRestartLoop: false,
			wantSecondsKey:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			info := makeInspect(tt.inspect)
			metrics := map[string]any{}
			output := map[string]any{}

			res := buildResult(tt.cfg, info, time.Now(), metrics, output)

			r.NotNil(res)
			r.Equal(tt.wantStatus, res.Status)

			// restartCount metric is always emitted, unchanged from today.
			r.Equal(tt.inspect.restartCount, res.Metrics["restartCount"])

			if tt.wantRestartLoop {
				r.Equal(true, res.Output["restartLoop"])
				r.Contains(res.Output, checkerdef.OutputKeyError)
			} else {
				r.NotContains(res.Output, "restartLoop")
			}

			if tt.wantSecondsKey {
				r.Contains(res.Output, "secondsSinceStart")
				r.IsType(float64(0), res.Output["secondsSinceStart"])
			} else {
				r.NotContains(res.Output, "secondsSinceStart")
			}
		})
	}
}

// TestBuildResult_DisabledByteForByte asserts that with detection disabled the
// output/metrics maps are exactly what they were before this feature: no
// restartLoop, no secondsSinceStart, status Up for a healthy running container.
func TestBuildResult_DisabledByteForByte(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	info := makeInspect(inspectOpts{
		running: true, state: "running", restartCount: 42,
		startedAt: time.Now().Format(time.RFC3339Nano),
	})
	output := map[string]any{}

	res := buildResult(&DockerConfig{}, info, time.Now(), map[string]any{}, output)

	r.Equal(checkerdef.StatusUp, res.Status)
	r.NotContains(res.Output, "restartLoop")
	r.NotContains(res.Output, "secondsSinceStart")
	r.NotContains(res.Output, checkerdef.OutputKeyError)
	r.Equal("/test-container", res.Output["containerName"])
	r.Equal(42, res.Metrics["restartCount"])
}
