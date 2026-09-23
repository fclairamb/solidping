package config

import (
	"regexp"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &WebSocketConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.URL == "" {
		return checkerdef.NewConfigError(ConfigKeyURL, "URL is required")
	}

	if !strings.HasPrefix(cfg.URL, "ws://") && !strings.HasPrefix(cfg.URL, "wss://") {
		return checkerdef.NewConfigError(ConfigKeyURL, "must start with ws:// or wss://")
	}

	if cfg.Expect != "" {
		if _, err := regexp.Compile(cfg.Expect); err != nil {
			return checkerdef.NewConfigErrorf("expect", "invalid regex pattern: %s", err.Error())
		}
	}

	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String(),
		)
	}

	if spec.Name == "" {
		host := hostFromURL(cfg.URL)
		spec.Name = "WebSocket: " + host
	}

	if spec.Slug == "" {
		host := hostFromURL(cfg.URL)
		spec.Slug = "ws-" + strings.ReplaceAll(host, ".", "-")
	}

	return nil
}
