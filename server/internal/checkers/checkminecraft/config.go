package checkminecraft

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// EditionJava represents Java Edition (TCP/25565).
	EditionJava = "java"
	// EditionBedrock represents Bedrock Edition (RakNet UDP/19132).
	EditionBedrock = "bedrock"
)

const (
	defaultJavaPort    = 25565
	defaultBedrockPort = 19132
	defaultTimeout     = 10 * time.Second
	maxTimeout         = 30 * time.Second
)

// MinecraftConfig holds the configuration for Minecraft server health checks.
type MinecraftConfig struct {
	Host       string        `json:"host"`
	Port       int           `json:"port,omitempty"`
	Edition    string        `json:"edition,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	MinPlayers int           `json:"minPlayers,omitempty"`
	MaxPlayers int           `json:"maxPlayers,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *MinecraftConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	} else if configMap[checkerdef.OutputKeyHost] != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
	}

	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if edition, ok := configMap["edition"].(string); ok {
		c.Edition = edition
	} else if configMap["edition"] != nil {
		return checkerdef.NewConfigError("edition", "must be a string")
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

	if minPlayers, ok := configMap["minPlayers"].(float64); ok {
		c.MinPlayers = int(minPlayers)
	} else if minPlayersInt, ok := configMap["minPlayers"].(int); ok {
		c.MinPlayers = minPlayersInt
	}

	if maxPlayers, ok := configMap["maxPlayers"].(float64); ok {
		c.MaxPlayers = int(maxPlayers)
	} else if maxPlayersInt, ok := configMap["maxPlayers"].(int); ok {
		c.MaxPlayers = maxPlayersInt
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *MinecraftConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 && c.Port != c.defaultPort() {
		cfg["port"] = c.Port
	}

	if c.Edition != "" && c.Edition != EditionJava {
		cfg["edition"] = c.Edition
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.MinPlayers > 0 {
		cfg["minPlayers"] = c.MinPlayers
	}

	if c.MaxPlayers > 0 {
		cfg["maxPlayers"] = c.MaxPlayers
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *MinecraftConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 0 and 65535, got %d", c.Port)
	}

	if c.Edition != "" && c.Edition != EditionJava && c.Edition != EditionBedrock {
		return checkerdef.NewConfigErrorf(
			"edition", "must be %q or %q, got %q", EditionJava, EditionBedrock, c.Edition,
		)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String(),
		)
	}

	if c.MinPlayers < 0 {
		return checkerdef.NewConfigError("minPlayers", "must be >= 0")
	}

	if c.MaxPlayers < 0 {
		return checkerdef.NewConfigError("maxPlayers", "must be >= 0")
	}

	return nil
}

func (c *MinecraftConfig) resolveEdition() string {
	if c.Edition == EditionBedrock {
		return EditionBedrock
	}

	return EditionJava
}

func (c *MinecraftConfig) defaultPort() int {
	if c.resolveEdition() == EditionBedrock {
		return defaultBedrockPort
	}

	return defaultJavaPort
}

func (c *MinecraftConfig) resolvePort() int {
	if c.Port != 0 {
		return c.Port
	}

	return c.defaultPort()
}

func (c *MinecraftConfig) resolveTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

func (c *MinecraftConfig) resolveTarget() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.resolvePort()))
}

func (c *MinecraftConfig) resolveSlug() string {
	return "mc-" + strings.ReplaceAll(c.Host, ".", "-")
}
