package paramkeys_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// TestReservedRegistryCoversEveryPlatformOwnedKey is the guard against the one
// way this package fails silently: a new per-org parameter the server writes
// for itself, not added to the reserved registry, which an org admin can then
// overwrite through PUT /orgs/:org/parameters/:key — or read out of the
// instance through `${param:…}` in a check body.
//
// The keys are spelled out as literals rather than imported, because most of
// them are unexported constants in the packages that own them. Each line names
// where it comes from; adding a platform key means adding it here and to the
// registry, and forgetting is what this test is for.
func TestReservedRegistryCoversEveryPlatformOwnedKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	platformKeys := []string{
		"encryption.dek",                         // crypto/credentials/param_store.go
		"default_regions",                        // regions/regions.go
		"custom_regions",                         // regions/regions.go
		"demo.enabled",                           // jobs/jobtypes/job_startup_demo.go
		"demo.backfilled",                        // jobs/jobtypes/job_startup_demo.go
		"demo.seeded_checks",                     // jobs/jobtypes/job_startup_demo.go
		"samples.loaded",                         // jobs/jobtypes/job_startup_demo.go
		"registration.email_pattern",             // handlers/auth/join_policy.go
		"registration.slack_workspace_auto_join", // handlers/auth/join_policy.go
		"status_page.publication_notify_cap",     // handlers/incidentpublications/service.go
		"diagnostics.traceroute.enabled",         // db/models/parameter.go
		"auth.password.algorithm",                // config/config.go
		"auth.session.max_duration",              // handlers/auth/service.go
		"email.host",                             // systemconfig — reachable via the system fallback
		"sp.whatever.comes.next",                 // the forward-looking namespace
	}

	for _, key := range platformKeys {
		r.Truef(paramkeys.IsReserved(key), "%q is platform-owned and must be reserved", key)
		r.ErrorIsf(paramkeys.Validate(key), paramkeys.ErrReservedKey,
			"%q must be refused over the org parameters API", key)
	}
}

// TestReservedRegistryIsNotAWall is the positive control: the registry must
// still let an ordinary org-managed key through, or "everything is reserved"
// would satisfy the test above while making the feature useless.
func TestReservedRegistryIsNotAWall(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, key := range []string{
		"sso-authtest-password", "api_token", "acme.token", "k8s-probe-bearer", "a",
	} {
		r.NoErrorf(paramkeys.Validate(key), "%q is an ordinary org key and must be accepted", key)
		r.Falsef(paramkeys.IsReserved(key), "%q must not read as reserved", key)
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
