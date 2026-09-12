package paramkeys_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// TestPlatformKeysAreOutsideTheOrgNamespace is what replaced
// TestReservedRegistryCoversEveryPlatformOwnedKey.
//
// That test asserted that a hand-typed list of 15 platform keys was refused,
// and its comment claimed it "fails when a new platform key escapes it". It did
// not — it could only ever check the keys somebody remembered to type, and it
// was already missing `msteams.app_secret`, `posthog.personal_api_key`,
// `posthog.project_api_key` and `telegram.webhook_secret` on the day it was
// written. A list that has to be kept in sync by hand is not a security
// boundary.
//
// What is asserted now needs no list: an org-managed parameter is STORED under
// paramkeys.OrgKeyPrefix, so a platform key is out of reach because it is in a
// different namespace. The property is checked over keys the platform actually
// uses — including the four the old list missed — but the test would hold for
// any key at all, which is the point.
func TestPlatformKeysAreOutsideTheOrgNamespace(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	platformKeys := []string{
		"encryption.dek",                         // crypto/credentials/param_store.go — the org's wrapped DEK
		"msteams.app_secret",                     // systemconfig — missed by the old denylist
		"posthog.personal_api_key",               // systemconfig — missed by the old denylist
		"posthog.project_api_key",                // systemconfig — missed by the old denylist
		"telegram.webhook_secret",                // app/telegram_resolve.go — missed by the old denylist
		"regions",                                // the FLAT key; the old denylist only had "regions."
		"operator_notifications",                 // systemconfig
		"platform_watchdog",                      // systemconfig
		"default_regions",                        // regions/regions.go
		"custom_regions",                         // regions/regions.go
		"registration.email_pattern",             // handlers/auth/join_policy.go
		"registration.slack_workspace_auto_join", // handlers/auth/join_policy.go
		"auth.session.max_duration",              // handlers/auth/service.go
		"diagnostics.traceroute.enabled",         // db/models/parameter.go
		"status_page.publication_notify_cap",     // handlers/incidentpublications
		"demo.enabled",                           // jobs/jobtypes/job_startup_demo.go
		"email.password",                         // systemconfig
		"server.base_url",                        // systemconfig
	}

	for _, key := range platformKeys {
		// Nothing an org admin can name is stored where this key lives…
		r.NotEqualf(key, paramkeys.StorageKey(key),
			"StorageKey must move an org key out of the platform namespace")

		// …and reading this row back through the org surface is not possible,
		// because it is not in the org namespace at all.
		_, owned := paramkeys.PublicKey(key)
		r.Falsef(owned, "%q is a platform key and must not read as org-managed", key)
	}
}

// TestTheOrgNamespaceRoundTrips is the positive control for the test above: the
// namespace must actually let an ordinary org key through and hand back exactly
// what the operator wrote. "Nothing is org-managed" would satisfy the property
// above while breaking the feature entirely.
func TestTheOrgNamespaceRoundTrips(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, key := range []string{
		"sso-authtest-password", "api_token", "acme.token", "k8s-probe-bearer", "a",
		// A user key that LOOKS like a platform key is fine — it is stored
		// somewhere else entirely, so it collides with nothing.
		"encryption.dek", "msteams.app_secret",
	} {
		r.NoErrorf(paramkeys.Validate(key), "%q is an ordinary org key and must be accepted", key)

		back, owned := paramkeys.PublicKey(paramkeys.StorageKey(key))
		r.Truef(owned, "a stored org key must read back as org-managed: %q", key)
		r.Equal(key, back, "the API must show exactly what the operator wrote")
	}
}

// TestReservedPrefixIsRefused pins the one reservation spec 2026-09-11-03 names
// by hand. It no longer carries any safety weight — the namespace does that —
// but `sp.` stays refused so the name is available to the platform later.
func TestReservedPrefixIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, key := range []string{"sp.anything", "sp.", "sp.deeply.nested"} {
		r.ErrorIsf(paramkeys.Validate(key), paramkeys.ErrReservedKey, "%q must be refused", key)
	}
}

// TestKeyPatternRefusesWhatItSays pins the shape spec 2026-09-11-03 specifies.
func TestKeyPatternRefusesWhatItSays(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bad := []string{
		"",                       // empty
		"9lives",                 // must start with a letter
		"Uppercase",              // lowercase only
		"has space",              // no spaces
		"has/slash",              // no path separators
		"_leading",               // must start with a letter
		string(make([]byte, 80)), // way over 64, and not letters either
	}

	for _, key := range bad {
		r.ErrorIsf(paramkeys.Validate(key), paramkeys.ErrInvalidKey, "%q must be refused", key)
	}
}
