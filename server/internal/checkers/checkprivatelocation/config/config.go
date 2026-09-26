// Package config holds the private-location liveness monitor's configuration:
// the struct, its map parsing and serialization, and its offline rule set
// (ValidateSpec).
//
// It is deliberately free of any database or region-service dependency, so
// `sp checks validate` can run the server's own validator against a
// config-as-code manifest. The rules that need the org's data (the region must
// be one of the org's OWN private locations) live in the checks service.
package config

import (
	"regexp"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ConfigKeyRegion is the single config key: the org-relative private region
// the monitor watches, `@<slug>`.
const ConfigKeyRegion = "region"

// NamePrefix / SlugPrefix name a monitor after its location: "Private
// location: <name>" and `private-location-<slug>`.
const (
	NamePrefix = "Private location: "
	SlugPrefix = "private-location-"
)

// privateRegionRe is the org-relative private region shape: the reserved `@`
// followed by a private-region slug. It mirrors regions.privateRegionSlugRe
// (duplicated rather than imported to keep this package light); the checks
// service tests pin that the two agree.
var privateRegionRe = regexp.MustCompile(`^@[a-z][a-z0-9-]{1,29}$`)

// PrivateLocationConfig is the configuration of a private-location liveness
// monitor (spec 2026-09-25-05).
type PrivateLocationConfig struct {
	// Region is the org-relative private region, `@<slug>`.
	Region string `json:"region"`
}

// FromMap populates the configuration from a map.
func (c *PrivateLocationConfig) FromMap(configMap map[string]any) error {
	raw, present := configMap[ConfigKeyRegion]
	if !present || raw == nil {
		c.Region = ""

		return nil
	}

	region, ok := raw.(string)
	if !ok {
		return checkerdef.NewConfigError(ConfigKeyRegion, "must be a string")
	}

	c.Region = strings.TrimSpace(region)

	return nil
}

// GetConfig returns the configuration as a map.
func (c *PrivateLocationConfig) GetConfig() map[string]any {
	return map[string]any{ConfigKeyRegion: c.Region}
}

// SecretFields declares which config keys carry secrets: none. The region is
// a label, not a credential.
func (c *PrivateLocationConfig) SecretFields() []string {
	return []string{}
}

// IsPrivateRegion reports whether region has the org-relative private region
// shape `@<slug>`.
func IsPrivateRegion(region string) bool {
	return privateRegionRe.MatchString(region)
}

// RegionSlug returns the raw slug of an `@<slug>` region, or "" when the
// region is malformed.
func RegionSlug(region string) string {
	if !IsPrivateRegion(region) {
		return ""
	}

	return strings.TrimPrefix(region, "@")
}

// ValidateSpec validates a check spec offline: the region must be present and
// be an org-relative private region. Fills in the name and slug the create
// path relies on.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &PrivateLocationConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.Region == "" {
		return checkerdef.NewConfigError(ConfigKeyRegion, "is required")
	}

	if !IsPrivateRegion(cfg.Region) {
		return checkerdef.NewConfigError(ConfigKeyRegion,
			"must be one of your private locations, written @<slug>")
	}

	if spec.Config == nil {
		spec.Config = make(map[string]any)
	}

	spec.Config[ConfigKeyRegion] = cfg.Region

	slug := RegionSlug(cfg.Region)

	if spec.Name == "" {
		spec.Name = NamePrefix + slug
	}

	if spec.Slug == "" {
		spec.Slug = SlugPrefix + slug
	}

	return nil
}
