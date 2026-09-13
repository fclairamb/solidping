// Package paramkeys owns what an ORG-MANAGED parameter key may be called, and
// where it is stored.
//
// It exists because two very different things share one `parameters` table: the
// platform's own configuration (the wrapped encryption DEK, the registration
// policy, the session ceiling, the Teams app secret, the PostHog API keys) and,
// since spec 2026-09-11-03, arbitrary values an org admin creates over the API
// to be referenced from a check config as `${param:KEY}`.
//
// Those two sets must not meet — in either direction. An org admin who could
// PUT `encryption.dek` would lock every credential the org owns out of its own
// envelopes; one who could write `body: "x=${param:msteams.app_secret}"` on an
// HTTP check pointed at a URL of their choosing would exfiltrate an instance
// credential.
//
// # Why a namespace and not a denylist
//
// The first cut of this package was a denylist of platform key prefixes. It was
// incomplete on the day it was written — `msteams.app_secret`,
// `posthog.personal_api_key`, `posthog.project_api_key` and
// `telegram.webhook_secret` all sailed through it — and it could only ever stay
// correct if every future contributor adding a system parameter remembered to
// come here. That is the wrong shape for a security boundary.
//
// So the boundary is structural instead: every org-managed parameter is stored
// under the OrgKeyPrefix namespace, which the platform never writes to, and
// `${param:KEY}` resolves nothing else — no system-parameter fallback, no
// denylist to keep in sync. A platform key is unreachable because it is not in
// the namespace, not because somebody listed it.
package paramkeys

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// KeyPattern is the shape of an org-managed parameter key as the operator
// writes it: lowercase, starting with a letter, up to 64 characters of letters,
// digits, underscore, dot and dash.
var KeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

var (
	// ErrInvalidKey is returned for a key that does not match KeyPattern.
	ErrInvalidKey = errors.New("invalid parameter key")
	// ErrReservedKey is returned for a key inside the reserved SolidPing
	// namespace.
	ErrReservedKey = errors.New("reserved parameter key")
)

// ReservedPrefix is the namespace spec 2026-09-11-03 reserves for SolidPing's
// own keys. Nothing depends on it for safety any more — OrgKeyPrefix does that
// — but it is refused over the API as specified, so the name stays available
// for whatever the platform wants to publish to organizations later.
const ReservedPrefix = "sp."

// OrgKeyPrefix is the storage namespace every org-managed parameter lives
// under, and the reason no denylist is needed. It never appears in the API, the
// CLI or a `${param:}` reference: the operator writes `api_token` and the row is
// `usr.api_token`.
//
// Nothing in SolidPing writes a parameter under this prefix for its own
// purposes, and nothing may start: it is the one namespace whose contents are
// fully controlled by an organization's admins and readable by any check config
// that organization owns.
const OrgKeyPrefix = "usr."

// StorageKey maps the key an operator wrote onto the row it is stored in.
func StorageKey(key string) string {
	return OrgKeyPrefix + key
}

// PublicKey maps a stored row's key back to what the operator wrote. The bool
// is false for a row outside the org-managed namespace — a platform key sharing
// the table — which callers listing an org's parameters must skip.
func PublicKey(storageKey string) (string, bool) {
	rest, ok := strings.CutPrefix(storageKey, OrgKeyPrefix)

	return rest, ok
}

// Validate checks a key an org admin supplied. It returns ErrInvalidKey for a
// malformed key and ErrReservedKey for one inside the reserved SolidPing
// namespace; both map to a 400.
func Validate(key string) error {
	if !KeyPattern.MatchString(key) {
		return fmt.Errorf("%w: %q must match %s", ErrInvalidKey, key, KeyPattern.String())
	}

	if strings.HasPrefix(strings.ToLower(key), ReservedPrefix) {
		return fmt.Errorf(
			"%w: the %q prefix is reserved for SolidPing", ErrReservedKey, ReservedPrefix)
	}

	return nil
}
