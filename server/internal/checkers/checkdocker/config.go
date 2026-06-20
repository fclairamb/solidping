package checkdocker

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultHost    = "unix:///var/run/docker.sock"
	defaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second

	// defaultRestartLoopWindow is the recency window applied when restart-loop
	// detection is enabled (RestartLoopMinRestarts > 0) but no window is set.
	defaultRestartLoopWindow = 120 * time.Second
	// maxRestartLoopWindow bounds the configurable recency window. A loop is a
	// rate signal; an unbounded window would let a single restart far in the
	// past trip the heuristic, defeating the recency guard.
	maxRestartLoopWindow = 24 * time.Hour
)

// DockerConfig holds the configuration for Docker container health checks.
type DockerConfig struct {
	Host          string        `json:"host,omitempty"`
	ContainerName string        `json:"containerName,omitempty"`
	ContainerID   string        `json:"containerId,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	// Restart-loop detection (opt-in). RestartLoopMinRestarts == 0 → disabled.
	// When enabled, a running container whose lifetime RestartCount is at least
	// RestartLoopMinRestarts and that (re)started within RestartLoopWindow is
	// flagged as a suspected crash-loop and reported as StatusWarning.
	RestartLoopMinRestarts int           `json:"restartLoopMinRestarts,omitempty"`
	RestartLoopWindow      time.Duration `json:"restartLoopWindow,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *DockerConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	} else if configMap["host"] != nil {
		return checkerdef.NewConfigError("host", "must be a string")
	}

	if containerName, ok := configMap["containerName"].(string); ok {
		c.ContainerName = containerName
	} else if configMap["containerName"] != nil {
		return checkerdef.NewConfigError("containerName", "must be a string")
	}

	if containerID, ok := configMap["containerId"].(string); ok {
		c.ContainerID = containerID
	} else if configMap["containerId"] != nil {
		return checkerdef.NewConfigError("containerId", "must be a string")
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	if err := c.parseRestartLoop(configMap); err != nil {
		return err
	}

	return nil
}

// parseRestartLoop reads the opt-in restart-loop detection fields. JSON numbers
// decode to float64 through a map[string]any, so minRestarts accepts the int and
// float64 shapes; the window is a duration string like timeout.
func (c *DockerConfig) parseRestartLoop(configMap map[string]any) error {
	if raw := configMap["restartLoopMinRestarts"]; raw != nil {
		switch v := raw.(type) {
		case float64:
			c.RestartLoopMinRestarts = int(v)
		case int:
			c.RestartLoopMinRestarts = v
		default:
			return checkerdef.NewConfigError("restartLoopMinRestarts", "must be a number")
		}
	}

	if window, ok := configMap["restartLoopWindow"].(string); ok {
		duration, err := time.ParseDuration(window)
		if err != nil {
			return checkerdef.NewConfigError("restartLoopWindow", "must be a valid duration string")
		}

		c.RestartLoopWindow = duration
	} else if configMap["restartLoopWindow"] != nil {
		return checkerdef.NewConfigError("restartLoopWindow", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *DockerConfig) GetConfig() map[string]any {
	cfg := map[string]any{}

	if c.Host != "" && c.Host != defaultHost {
		cfg["host"] = c.Host
	}

	if c.ContainerName != "" {
		cfg["containerName"] = c.ContainerName
	}

	if c.ContainerID != "" {
		cfg["containerId"] = c.ContainerID
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.RestartLoopMinRestarts != 0 {
		cfg["restartLoopMinRestarts"] = c.RestartLoopMinRestarts
	}

	if c.RestartLoopWindow != 0 {
		cfg["restartLoopWindow"] = c.RestartLoopWindow.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *DockerConfig) Validate() error {
	if c.ContainerName == "" && c.ContainerID == "" {
		return checkerdef.NewConfigError("containerName", "at least one of containerName or containerID is required")
	}

	host := c.resolveHost()
	if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "tcp://") {
		return checkerdef.NewConfigError("host", "must start with unix:// or tcp://")
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	if c.RestartLoopMinRestarts < 0 {
		return checkerdef.NewConfigErrorf(
			"restartLoopMinRestarts", "must be >= 0, got %d", c.RestartLoopMinRestarts,
		)
	}

	if c.RestartLoopWindow != 0 && (c.RestartLoopWindow <= 0 || c.RestartLoopWindow > maxRestartLoopWindow) {
		return checkerdef.NewConfigErrorf(
			"restartLoopWindow", "must be > 0 and <= 24h, got %s", c.RestartLoopWindow.String(),
		)
	}

	return nil
}

// restartLoopEnabled reports whether opt-in restart-loop detection is active.
func (c *DockerConfig) restartLoopEnabled() bool {
	return c.RestartLoopMinRestarts > 0
}

// resolveRestartLoopWindow returns the recency window to apply, defaulting to
// 120s when detection is enabled but no window was configured.
func (c *DockerConfig) resolveRestartLoopWindow() time.Duration {
	if c.RestartLoopWindow != 0 {
		return c.RestartLoopWindow
	}

	return defaultRestartLoopWindow
}

func (c *DockerConfig) resolveHost() string {
	if c.Host != "" {
		return c.Host
	}

	return defaultHost
}

func (c *DockerConfig) resolveTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

func (c *DockerConfig) resolveContainerRef() string {
	if c.ContainerID != "" {
		return c.ContainerID
	}

	return c.ContainerName
}
