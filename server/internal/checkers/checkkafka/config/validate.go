package config

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &KafkaConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" && len(cfg.Brokers) > 0 {
		spec.Name = cfg.Brokers[0]
	}

	if spec.Slug == "" && len(cfg.Brokers) > 0 {
		host := cfg.Brokers[0]
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}

		spec.Slug = "kafka-" + strings.ReplaceAll(host, ".", "-")
	}

	return nil
}
