package config

import (
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	httpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"
)

// refusedKeys are the http options a health check rejects: the health document
// is the only thing that judges the response, so there is one way to judge it.
func refusedKeys() []string {
	return []string{
		"body_expect", "bodyExpect",
		"body_reject", "bodyReject",
		"body_pattern", "bodyPattern",
		"body_pattern_reject", "bodyPatternReject",
		"json_path_assertions", "jsonPathAssertions",
		"body_assertions", "bodyAssertions",
		"expected_status", "expectedStatus",
		"expected_status_codes", "expectedStatusCodes",
	}
}

// ValidateSpec validates a health check spec offline and fills in the spec
// defaults (name, slug).
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	for _, key := range refusedKeys() {
		if value, ok := spec.Config[key]; ok && value != nil {
			return checkerdef.NewConfigError(key, "is not supported on a health check: the health document judges the response")
		}
	}

	// Every request rule is the http check's. It runs on a copy so its
	// "http-" slug default never lands on this spec.
	probe := &checkerdef.CheckSpec{Name: "health", Slug: "health", Config: spec.Config}
	if err := httpconfig.ValidateSpec(probe); err != nil {
		return err
	}

	cfg := &HealthConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	host := cfg.URL
	if parsed, err := url.Parse(cfg.URL); err == nil {
		host = parsed.Hostname()
	}

	if spec.Name == "" {
		spec.Name = "Health: " + host
	}

	if spec.Slug == "" {
		spec.Slug = "health-" + strings.ReplaceAll(host, ".", "-")
	}

	return nil
}
