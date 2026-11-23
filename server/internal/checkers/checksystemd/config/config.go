// Package config holds the systemd check's configuration: the struct, its map
// parsing and serialization, and the whole offline rule set (ValidateSpec).
//
// It is free of the execution code the parent checksystemd package links, so
// `sp checks validate` can run the server's own validators against a
// config-as-code manifest. The parent keeps a type alias.
package config

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config keys.
const (
	KeyUnit          = "unit"
	KeyExpectedState = "expected_state"
	KeyTimeout       = "timeout"
)

// Expected states.
const (
	StateActive   = "active"
	StateInactive = "inactive"
)

// defaultUnitSuffix is appended to a bare unit name.
const defaultUnitSuffix = ".service"

// unitSuffixes are the systemd unit types: a name ending with one of them is
// taken as is, any other name gets `.service` appended.
var unitSuffixes = []string{ //nolint:gochecknoglobals // constant lookup table
	".service", ".socket", ".device", ".mount", ".automount", ".swap",
	".target", ".path", ".timer", ".slice", ".scope",
}

// SystemdConfig holds the configuration of a systemd unit check.
type SystemdConfig struct {
	Unit          string        `json:"unit"`
	ExpectedState string        `json:"expected_state,omitempty" jsonschema:"enum=active,enum=inactive"` //nolint:tagliatelle // API uses snake_case
	Timeout       time.Duration `json:"timeout,omitempty"`
}

// UnitName returns the full unit name: a bare name gets `.service` appended.
func (c *SystemdConfig) UnitName() string {
	for _, suffix := range unitSuffixes {
		if strings.HasSuffix(c.Unit, suffix) {
			return c.Unit
		}
	}

	return c.Unit + defaultUnitSuffix
}

// WantActive reports whether the unit is expected to run (the default).
func (c *SystemdConfig) WantActive() bool {
	return c.ExpectedState != StateInactive
}

// FromMap populates the configuration from a map.
func (c *SystemdConfig) FromMap(configMap map[string]any) error {
	if err := readString(configMap, KeyUnit, &c.Unit); err != nil {
		return err
	}

	if err := readString(configMap, KeyExpectedState, &c.ExpectedState); err != nil {
		return err
	}

	if raw, ok := configMap[KeyTimeout].(string); ok {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return checkerdef.NewConfigError(KeyTimeout, "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap[KeyTimeout] != nil {
		return checkerdef.NewConfigError(KeyTimeout, "must be a string")
	}

	return nil
}

func readString(configMap map[string]any, key string, target *string) error {
	switch value := configMap[key].(type) {
	case nil:
		return nil
	case string:
		*target = value

		return nil
	default:
		return checkerdef.NewConfigError(key, "must be a string")
	}
}

// GetConfig returns the configuration as a map.
func (c *SystemdConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		KeyUnit: c.Unit,
	}

	if c.ExpectedState != "" {
		cfg[KeyExpectedState] = c.ExpectedState
	}

	if c.Timeout != 0 {
		cfg[KeyTimeout] = c.Timeout.String()
	}

	return cfg
}

// SchemaNotes implements checkerdef.SchemaNoter: the rules Validate() enforces
// that a schema reflected from this struct cannot state.
func (c *SystemdConfig) SchemaNotes() []string {
	return []string{
		"`unit` only accepts letters, digits and `:_.@-`, at most 256 characters, and must not " +
			"start with `-`. A name without a unit-type suffix gets `.service` appended.",
		"`expected_state` is `active` (default: active, activating and reloading are up) or " +
			"`inactive` (the unit must not run).",
		"A SaaS shared region never runs this type: it must be pinned to a private location.",
	}
}
