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
	cfg := &ClickHouseConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.ResolvedPort(), cfg.ResolvedDatabase())
	}

	if spec.Slug == "" {
		spec.Slug = "clickhouse-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}
