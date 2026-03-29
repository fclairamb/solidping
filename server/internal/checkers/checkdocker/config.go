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
)

// DockerConfig holds the configuration for Docker container health checks.
type DockerConfig struct {
	Host          string        `json:"host,omitempty"`
	ContainerName string        `json:"containerName,omitempty"`
	ContainerID   string        `json:"containerId,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
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

	return nil
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
