package config

import (
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/catalog"
)

// NormalizeURL strips a trailing slash and any /api/v2/... suffix from a status
// page URL, leaving the page root.
func NormalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if index := strings.Index(trimmed, "/api/v2"); index >= 0 {
		trimmed = trimmed[:index]
	}

	return strings.TrimRight(trimmed, "/")
}

// ValidateSpec validates a vendor-status check spec offline and fills in the
// spec defaults (name, slug). It rewrites config.url to its normalized form.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &VendorStatusConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	cfg.URL = NormalizeURL(cfg.URL)

	if err := cfg.Validate(); err != nil {
		return err
	}

	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Hostname() == "" {
		return checkerdef.NewConfigError(keyURL, "invalid URL format")
	}

	spec.Config[keyURL] = cfg.URL

	host := parsed.Hostname()

	if spec.Name == "" {
		label := host
		if entry, ok := catalog.Find(cfg.Provider); ok {
			label = entry.Name
		}

		spec.Name = label + " status"
	}

	if spec.Slug == "" {
		spec.Slug = "vendor-" + strings.ReplaceAll(host, ".", "-")
	}

	return nil
}
