package config

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &DNSConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate Host (domain to query)
	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	// Validate Timeout if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	// Validate RecordType if set
	recordType := cfg.RecordType
	if recordType == "" {
		recordType = DefaultRecordType
	}

	recordType = strings.ToUpper(recordType)
	if !IsValidRecordType(recordType) {
		return checkerdef.NewConfigErrorf(
			"record_type", "must be one of A, AAAA, CNAME, MX, NS, TXT, SOA, got %s", cfg.RecordType,
		)
	}

	// Validate Nameserver format if set
	if cfg.Nameserver != "" {
		if !strings.Contains(cfg.Nameserver, ":") {
			return checkerdef.NewConfigErrorf("nameserver", "must be in format host:port, got %s", cfg.Nameserver)
		}
	}

	// Cannot specify both expected_ips and expected_values
	if len(cfg.ExpectedIPs) > 0 && len(cfg.ExpectedValues) > 0 {
		return checkerdef.NewConfigError("expected_values", "cannot specify both expected_ips and expected_values")
	}

	if spec.Slug == "" {
		spec.Slug = "dns-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}
