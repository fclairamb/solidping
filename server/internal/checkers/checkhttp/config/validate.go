package config

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
//
//nolint:cyclop,funlen,gocognit // Config validation requires checking many fields
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &HTTPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate URL
	if cfg.URL == "" {
		return checkerdef.NewConfigError("url", "is required")
	}

	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return checkerdef.NewConfigError("url", "must start with http:// or https://")
	}

	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return checkerdef.NewConfigError("url", "invalid URL format")
	}

	// Auto-generate name and slug from URL if not provided
	if spec.Name == "" || spec.Slug == "" {
		// Extract hostname (without port)
		hostname := parsedURL.Hostname()

		// Set name to hostname if empty
		if spec.Name == "" {
			spec.Name = hostname
		}

		// Set slug to hostname with dots replaced by hyphens if empty
		if spec.Slug == "" {
			spec.Slug = "http-" + strings.ReplaceAll(hostname, ".", "-")
		}
	}

	// Validate HTTP method
	if cfg.Method != "" {
		validMethods := map[string]bool{
			http.MethodGet:     true,
			http.MethodPost:    true,
			http.MethodPut:     true,
			http.MethodDelete:  true,
			http.MethodHead:    true,
			http.MethodOptions: true,
			http.MethodPatch:   true,
			methodQuery:        true,
		}

		method := strings.ToUpper(cfg.Method)
		if !validMethods[method] {
			return checkerdef.NewConfigErrorf("method", "invalid HTTP method: %s", cfg.Method)
		}
	}

	// Validate expected status (deprecated, but still supported)
	if cfg.ExpectedStatus != 0 && (cfg.ExpectedStatus < 100 || cfg.ExpectedStatus > 599) {
		return checkerdef.NewConfigErrorf("expected_status", "must be between 100 and 599, got %d", cfg.ExpectedStatus)
	}

	// Validate expected status codes patterns
	for i, pattern := range cfg.ExpectedStatusCodes {
		if err := validateStatusPattern(pattern); err != nil {
			return checkerdef.NewConfigErrorf("expected_status_codes", "element %d: %v", i, err)
		}
	}

	// Compile and validate regex patterns
	if cfg.BodyPattern != "" {
		regex, err := regexp.Compile(cfg.BodyPattern)
		if err != nil {
			return checkerdef.NewConfigErrorf("body_pattern", "invalid regex pattern: %v", err)
		}
		cfg.BodyPatternRegex = regex
	}

	if cfg.BodyPatternReject != "" {
		regex, err := regexp.Compile(cfg.BodyPatternReject)
		if err != nil {
			return checkerdef.NewConfigErrorf("body_pattern_reject", "invalid regex pattern: %v", err)
		}
		cfg.BodyPatternRejectRegex = regex
	}

	if len(cfg.HeadersPattern) > 0 {
		cfg.HeadersPatternRegex = make(map[string]*regexp.Regexp, len(cfg.HeadersPattern))
		for headerName, pattern := range cfg.HeadersPattern {
			regex, err := regexp.Compile(pattern)
			if err != nil {
				return checkerdef.NewConfigErrorf("headers_pattern", "invalid regex pattern for header %q: %v", headerName, err)
			}
			cfg.HeadersPatternRegex[headerName] = regex
		}
	}

	// Validate JSONPath assertions
	if cfg.JSONPathAssertions != nil {
		if err := cfg.JSONPathAssertions.Validate(); err != nil {
			return checkerdef.NewConfigError("json_path_assertions", err.Error())
		}
	}

	// Validate body assertions. ValidateBody is deliberately NOT Validate:
	// it requires no Path and rejects the operators that cannot fail against
	// a raw body (exists/not_exists and the numeric comparisons).
	if cfg.BodyAssertions != nil {
		if err := cfg.BodyAssertions.ValidateBody(); err != nil {
			return checkerdef.NewConfigError("bodyAssertions", err.Error())
		}
	}

	// Validate SecretHeaders names
	for k := range cfg.SecretHeaders {
		if k == "" {
			return checkerdef.NewConfigError("secretHeaders", "header name must not be empty")
		}
	}

	return nil
}
