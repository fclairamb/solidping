package config

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &PrometheusConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	subject := cfg.Metric
	if cfg.EffectiveMode() == ModePromQL {
		subject = cfg.Query
	}

	if spec.Name == "" {
		spec.Name = "Prometheus: " + subject
	}

	// A default slug is part of "done" for a check type — leaving it empty
	// regresses the checkdnsbl/checksip fix.
	if spec.Slug == "" {
		spec.Slug = defaultSlug(cfg.URL)
	}

	return nil
}
