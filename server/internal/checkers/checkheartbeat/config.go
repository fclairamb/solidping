package checkheartbeat

import "github.com/fclairamb/solidping/server/internal/checkers/checkerdef"

// HeartbeatConfig holds the configuration for heartbeat checks.
type HeartbeatConfig struct {
	Token string `json:"token,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *HeartbeatConfig) FromMap(configMap map[string]any) error {
	if token, ok := configMap["token"].(string); ok {
		c.Token = token
	} else if configMap["token"] != nil {
		return checkerdef.NewConfigError("token", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *HeartbeatConfig) GetConfig() map[string]any {
	cfg := make(map[string]any)

	if c.Token != "" {
		cfg["token"] = c.Token
	}

	return cfg
}
