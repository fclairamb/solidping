package config

import (
	"regexp"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	maxTimeout    = 30 * time.Second
	maxUnitLength = 256
)

// unitPattern is the character set of a unit name. It excludes spaces, shell
// metacharacters and `=`, and with the leading `-` rule below a unit can
// never be read as a systemctl flag.
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9:_.@-]+$`)

// ValidateSpec validates a check spec offline: it parses the config, applies
// every rule, and fills in the spec defaults (slug) the create path relies on.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &SystemdConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Slug == "" {
		spec.Slug = slugFor(cfg.UnitName())
	}

	return nil
}

// Validate applies every rule to an already-parsed config.
func (c *SystemdConfig) Validate() error {
	if err := ValidateUnit(c.Unit); err != nil {
		return err
	}

	switch c.ExpectedState {
	case "", StateActive, StateInactive:
	default:
		return checkerdef.NewConfigErrorf(KeyExpectedState, "must be %q or %q", StateActive, StateInactive)
	}

	if c.Timeout != 0 && (c.Timeout < 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(KeyTimeout, "must be > 0 and <= 30s, got %s", c.Timeout.String())
	}

	return nil
}

// ValidateUnit checks a unit name. It is the security boundary of the check:
// the name ends up in a systemctl argv.
func ValidateUnit(unit string) error {
	switch {
	case unit == "":
		return checkerdef.NewRequiredConfigError(KeyUnit)
	case len(unit) > maxUnitLength:
		return checkerdef.NewConfigErrorf(KeyUnit, "must be at most %d characters", maxUnitLength)
	case strings.HasPrefix(unit, "-"):
		return checkerdef.NewConfigError(KeyUnit, "must not start with -")
	case !unitPattern.MatchString(unit):
		return checkerdef.NewConfigError(KeyUnit, "may only contain letters, digits and :_.@-")
	}

	return nil
}

// slugFor derives a slug from the unit name: nginx.service gives
// systemd-nginx-service.
func slugFor(unit string) string {
	var builder strings.Builder

	for _, r := range strings.ToLower(unit) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('-')
		}
	}

	name := strings.Trim(builder.String(), "-")
	if name == "" {
		return "systemd"
	}

	return "systemd-" + name
}
