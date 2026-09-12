// Package paramkeys owns what an ORG-MANAGED parameter key may be called.
//
// It exists because two very different things share one `parameters` table: the
// platform's own per-org configuration (the wrapped encryption DEK, the
// registration policy, the session ceiling, the demo-seed bookkeeping) and,
// since spec 2026-09-11-03, arbitrary values an org admin creates over the API
// to be referenced from a check config as `${param:KEY}`.
//
// Those two sets must not meet. An org admin who could PUT `encryption.dek`
// would lock every credential the org owns out of its own envelopes; one who
// could reference `${param:encryption.dek}` from an HTTP check's body would
// post the wrapped DEK to a URL of their choosing. So the reserved registry
// below gates BOTH directions — the write API and the reference resolver — and
// lives in a leaf package so neither side can drift from the other.
package paramkeys

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// KeyPattern is the shape of an org-managed parameter key: lowercase, starting
// with a letter, up to 64 characters of letters, digits, underscore, dot and
// dash.
//
//nolint:gochecknoglobals // the rule itself, compiled once
var KeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

var (
	// ErrInvalidKey is returned for a key that does not match KeyPattern.
	ErrInvalidKey = errors.New("invalid parameter key")
	// ErrReservedKey is returned for a key the platform owns.
	ErrReservedKey = errors.New("reserved parameter key")
)

// ReservedPrefix is the namespace spec 2026-09-11-03 reserves for everything
// SolidPing itself stores per-org from here on. New internal keys go under it;
// the ones below predate it and are listed one by one because renaming a key
// that is already written to customer databases is a migration, not a rule.
const ReservedPrefix = "sp."

// reservedPrefixes are the key namespaces the platform writes for its own
// purposes. An org-managed key may not fall inside any of them.
//
// Keep this in sync with whatever the server itself calls SetOrgParameter with
// — TestReservedRegistryCoversEveryPlatformOwnedKey pins the current set and
// fails when a new platform key escapes it.
//
//nolint:gochecknoglobals // lookup table
var reservedPrefixes = []string{
	ReservedPrefix,
	"aggregation.",   // internal/systemconfig
	"auth.",          // session ceiling, password algorithm
	"demo.",          // internal/jobs/jobtypes/job_startup_demo.go
	"diagnostics.",   // models.ParamKeyTracerouteEnabled
	"email.",         // internal/systemconfig
	"encryption.",    // credentials.ParamStore — the wrapped org DEK
	"entitlements.",  // SaaS plan material
	"notifications.", // operator notification routing
	"registration.",  // internal/handlers/auth/join_policy.go
	"samples.",       // internal/jobs/jobtypes/job_startup_demo.go
	"status_page.",   // internal/handlers/incidentpublications
	"regions.",       // reserved alongside the two flat region keys below
}

// reservedKeys are the platform-owned keys that carry no dotted namespace at
// all. They predate the convention; they are still ours.
//
//nolint:gochecknoglobals // lookup table
var reservedKeys = []string{
	"default_regions", // internal/regions/regions.go
	"custom_regions",  // internal/regions/regions.go
}

// IsReserved reports whether a key belongs to the platform rather than to the
// organization. Both the write API and the `${param:}` resolver consult it, so
// a reserved key can neither be created nor read through a check config.
func IsReserved(key string) bool {
	lower := strings.ToLower(key)

	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	for _, reserved := range reservedKeys {
		if lower == reserved {
			return true
		}
	}

	return false
}

// Validate checks a key an org admin supplied. It returns ErrInvalidKey for a
// malformed key and ErrReservedKey for a platform-owned one; both map to a 400.
func Validate(key string) error {
	if !KeyPattern.MatchString(key) {
		return fmt.Errorf("%w: %q must match %s", ErrInvalidKey, key, KeyPattern.String())
	}

	if IsReserved(key) {
		return fmt.Errorf(
			"%w: %q is reserved for SolidPing's own per-organization configuration", ErrReservedKey, key)
	}

	return nil
}
