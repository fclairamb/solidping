package config

import (
	"maps"
	"regexp"
	"slices"

	dnsblconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	if spec.Config == nil {
		spec.Config = make(map[string]any)
	}

	if err := validateAuth(spec.Config); err != nil {
		return err
	}

	// The token is server-managed (spec 2026-11-14-01): the checks service drops
	// a caller's value before this runs, and a stored value that is not the
	// minted 48-hex shape is replaced, since the inbound pattern could never
	// match it anyway.
	if token, _ := spec.Config["token"].(string); !IsValidToken(token) {
		token, err := generateToken()
		if err != nil {
			return checkerdef.NewConfigError("token", "failed to generate token")
		}

		spec.Config["token"] = token
	}

	if spec.Name == "" {
		spec.Name = "email"
	}

	if spec.Slug == "" {
		spec.Slug = "email"
	}

	return nil
}

// zonePattern is a DNS name: dot-separated labels of letters, digits and hyphens.
var zonePattern = regexp.MustCompile(
	`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validateAuth applies the rules of the optional sender-authentication options.
func validateAuth(configMap map[string]any) error {
	// The token is regenerated when malformed (see ValidateSpec), so it is not
	// part of the rules applied here.
	authMap := maps.Clone(configMap)
	delete(authMap, "token")

	cfg := &EmailConfig{}
	if err := cfg.FromMap(authMap); err != nil {
		return err
	}

	if !slices.Contains([]string{"", SPFNone, SPFSoftfail, SPFPass}, cfg.RequireSPF) {
		return checkerdef.NewConfigErrorf(KeyRequireSPF, "must be one of none, softfail, pass, got %q", cfg.RequireSPF)
	}

	if !slices.Contains([]string{"", DMARCPass}, cfg.RequireDMARC) {
		return checkerdef.NewConfigErrorf(KeyRequireDMARC, "must be %q, got %q", DMARCPass, cfg.RequireDMARC)
	}

	if len(cfg.Blocklists) > dnsblconfig.MaxBlocklists {
		return checkerdef.NewConfigErrorf(KeyBlocklists, "at most %d zones, got %d",
			dnsblconfig.MaxBlocklists, len(cfg.Blocklists))
	}

	for _, zone := range cfg.Blocklists {
		if !zonePattern.MatchString(zone) {
			return checkerdef.NewConfigErrorf(KeyBlocklists, "%q is not a valid DNS zone name", zone)
		}
	}

	return nil
}
