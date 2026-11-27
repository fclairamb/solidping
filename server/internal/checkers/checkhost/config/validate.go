package config

import (
	"path/filepath"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const maxPercent = 100

// ValidateSpec validates a check spec offline: it parses the config, applies
// every rule, and fills in the spec defaults (slug).
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &HostConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Slug == "" {
		spec.Slug = "host"
	}

	return nil
}

// Validate applies every rule to an already-parsed config.
func (c *HostConfig) Validate() error {
	if err := ValidateDiskPath(c.DiskPath); err != nil {
		return err
	}

	pairs := []struct {
		warnKey, critKey string
		warn, crit       *float64
	}{
		{KeyDiskWarningPercent, KeyDiskCriticalPercent, c.DiskWarningPercent, c.DiskCriticalPercent},
		{KeyMemWarningPercent, KeyMemCriticalPercent, c.MemWarningPercent, c.MemCriticalPercent},
		{KeyCPUWarningPercent, KeyCPUCriticalPercent, c.CPUWarningPercent, c.CPUCriticalPercent},
	}

	for idx := range pairs {
		pair := &pairs[idx]

		if err := validatePercent(pair.warnKey, pair.warn); err != nil {
			return err
		}

		if err := validatePercent(pair.critKey, pair.crit); err != nil {
			return err
		}

		if pair.warn != nil && pair.crit != nil && *pair.warn >= *pair.crit {
			return checkerdef.NewConfigErrorf(pair.warnKey, "must be below %s", pair.critKey)
		}
	}

	return nil
}

func validatePercent(key string, value *float64) error {
	if value == nil {
		return nil
	}

	// The negated form also rejects NaN.
	if !(*value > 0 && *value <= maxPercent) {
		return checkerdef.NewConfigErrorf(key, "must be in (0, 100], got %g", *value)
	}

	return nil
}

// ValidateDiskPath checks the disk path: empty (default), or absolute and
// clean for the system that validates, with no NUL byte.
func ValidateDiskPath(path string) error {
	switch {
	case path == "":
		return nil
	case strings.ContainsRune(path, 0):
		return checkerdef.NewConfigError(KeyDiskPath, "must not contain a NUL byte")
	case !filepath.IsAbs(path):
		return checkerdef.NewConfigError(KeyDiskPath, "must be an absolute path")
	case filepath.Clean(path) != path:
		return checkerdef.NewConfigError(KeyDiskPath, "must be a clean path (no `..`, `.` or double separators)")
	}

	return nil
}
