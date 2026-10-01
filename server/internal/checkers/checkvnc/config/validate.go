package config

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the protocol code.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &VNCConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "VNC: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "vnc-" + strings.NewReplacer(".", "-", ":", "-").Replace(cfg.Host)
	}

	return nil
}
