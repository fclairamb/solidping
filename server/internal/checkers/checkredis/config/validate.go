package config

import (
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &RedisConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}

	target, slugTarget := fmt.Sprintf("%s:%d", cfg.Host, port), cfg.Host
	if cfg.Socket != "" {
		target = cfg.Socket
		slugTarget = strings.Trim(strings.NewReplacer("/", "-", ".", "-").Replace(cfg.Socket), "-")
	}

	if spec.Name == "" {
		spec.Name = target
	}

	if spec.Slug == "" {
		spec.Slug = "redis-" + strings.ReplaceAll(slugTarget, ".", "-")
	}

	return nil
}
