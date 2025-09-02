package checkjs

import (
	"fmt"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	maxScriptSize  = 64 * 1024 // 64KB max script size
	defaultTimeout = 30 * time.Second
	maxTimeout     = 60 * time.Second
	maxEnvEntries  = 50
)

// JSConfig holds the configuration for JavaScript checks.
type JSConfig struct {
	Script  string            `json:"script"`
	Timeout time.Duration     `json:"timeout,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *JSConfig) FromMap(configMap map[string]any) error {
	// Extract Script (required)
	if script, ok := configMap["script"].(string); ok {
		c.Script = script
	} else if configMap["script"] != nil {
		return checkerdef.NewConfigError("script", "must be a string")
	}

	// Extract Timeout (optional, duration string)
	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	// Extract Env (optional, map[string]string)
	if envRaw, ok := configMap["env"]; ok && envRaw != nil {
		envMap, ok := envRaw.(map[string]any)
		if !ok {
			return checkerdef.NewConfigError("env", "must be a map of string key-value pairs")
		}

		c.Env = make(map[string]string, len(envMap))

		for envKey, envVal := range envMap {
			strVal, ok := envVal.(string)
			if !ok {
				return checkerdef.NewConfigError("env",
					fmt.Sprintf("value for key %q must be a string", envKey))
			}

			c.Env[envKey] = strVal
		}
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *JSConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"script": c.Script,
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if len(c.Env) > 0 {
		env := make(map[string]any, len(c.Env))
		for k, v := range c.Env {
			env[k] = v
		}

		cfg["env"] = env
	}

	return cfg
}

// Validate checks that the configuration fields are within acceptable bounds.
func (c *JSConfig) Validate() error {
	if c.Script == "" {
		return checkerdef.NewConfigError("script", "is required")
	}

	if len(c.Script) > maxScriptSize {
		return checkerdef.NewConfigErrorf("script",
			"must be at most %d bytes, got %d", maxScriptSize, len(c.Script))
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout",
			"must be > 0 and <= %s, got %s", maxTimeout, c.Timeout)
	}

	if len(c.Env) > maxEnvEntries {
		return checkerdef.NewConfigErrorf("env",
			"must have at most %d entries, got %d", maxEnvEntries, len(c.Env))
	}

	// Check for JavaScript syntax errors via Goja compilation
	wrapped := "(function() {\n" + c.Script + "\n})()"
	if _, err := goja.Compile("script", wrapped, true); err != nil {
		return checkerdef.NewConfigError("script",
			"syntax error: "+err.Error())
	}

	return nil
}
