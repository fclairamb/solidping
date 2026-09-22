package config

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &DomainConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyDomain, err.Error())
	}

	// Auto-generate name and slug from domain if not provided
	if spec.Name == "" {
		spec.Name = "Domain: " + cfg.Domain
	}

	if spec.Slug == "" {
		spec.Slug = "domain-" + cfg.Domain
	}

	return nil
}
