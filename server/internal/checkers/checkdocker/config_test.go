package checkdocker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDockerConfig_FromMap_RestartLoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		in          map[string]any
		wantMin     int
		wantWindow  time.Duration
		wantErr     bool
		errContains string
	}{
		{
			name:       "parses min restarts (float64 from JSON) and window string",
			in:         map[string]any{"containerName": "c", "restartLoopMinRestarts": float64(3), "restartLoopWindow": "90s"},
			wantMin:    3,
			wantWindow: 90 * time.Second,
		},
		{
			name:    "accepts int min restarts",
			in:      map[string]any{"containerName": "c", "restartLoopMinRestarts": 5},
			wantMin: 5,
		},
		{
			name:        "rejects non-number min restarts",
			in:          map[string]any{"containerName": "c", "restartLoopMinRestarts": "three"},
			wantErr:     true,
			errContains: "restartLoopMinRestarts",
		},
		{
			name:        "rejects non-string window",
			in:          map[string]any{"containerName": "c", "restartLoopWindow": 120},
			wantErr:     true,
			errContains: "restartLoopWindow",
		},
		{
			name:        "rejects invalid window duration",
			in:          map[string]any{"containerName": "c", "restartLoopWindow": "notaduration"},
			wantErr:     true,
			errContains: "restartLoopWindow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &DockerConfig{}
			err := cfg.FromMap(tt.in)

			if tt.wantErr {
				r.Error(err)
				r.Contains(err.Error(), tt.errContains)
				return
			}

			r.NoError(err)
			r.Equal(tt.wantMin, cfg.RestartLoopMinRestarts)
			r.Equal(tt.wantWindow, cfg.RestartLoopWindow)
		})
	}
}

func TestDockerConfig_Validate_RestartLoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *DockerConfig
		wantErr bool
	}{
		{
			name: "valid enabled config",
			cfg:  &DockerConfig{ContainerName: "c", RestartLoopMinRestarts: 3, RestartLoopWindow: 120 * time.Second},
		},
		{
			name: "disabled is valid (zero values)",
			cfg:  &DockerConfig{ContainerName: "c"},
		},
		{
			name:    "negative min restarts is invalid",
			cfg:     &DockerConfig{ContainerName: "c", RestartLoopMinRestarts: -1},
			wantErr: true,
		},
		{
			name:    "window over bound is invalid",
			cfg:     &DockerConfig{ContainerName: "c", RestartLoopWindow: 48 * time.Hour},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := tt.cfg.Validate()
			if tt.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestDockerConfig_GetConfig_RestartLoopRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	original := &DockerConfig{
		ContainerName:          "c",
		RestartLoopMinRestarts: 4,
		RestartLoopWindow:      90 * time.Second,
	}

	got := original.GetConfig()
	r.Equal(4, got["restartLoopMinRestarts"])
	r.Equal("1m30s", got["restartLoopWindow"])

	// Round-trip: GetConfig() output must parse back into an equivalent config.
	parsed := &DockerConfig{}
	r.NoError(parsed.FromMap(got))
	r.Equal(original.RestartLoopMinRestarts, parsed.RestartLoopMinRestarts)
	r.Equal(original.RestartLoopWindow, parsed.RestartLoopWindow)
}

func TestDockerConfig_ResolveRestartLoopWindow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(defaultRestartLoopWindow, (&DockerConfig{RestartLoopMinRestarts: 3}).resolveRestartLoopWindow())
	r.Equal(90*time.Second, (&DockerConfig{RestartLoopMinRestarts: 3, RestartLoopWindow: 90 * time.Second}).resolveRestartLoopWindow())
}
