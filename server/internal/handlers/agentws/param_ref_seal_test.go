package agentws_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// The agent half of spec 2026-09-11-03, which the audit found was code-only.
//
// The design is: `${param:}` is resolved on the API (it reads the org's
// parameters) and the resolved value is folded into the SEALED envelope, where
// the agent's own merge lays it over the reference still sitting in the public
// wire config. Three things must hold, and each would fail silently — or leak
// silently — if it regressed:
//
//  1. the value is inside ConfigSealed and NOT in the wire Config;
//  2. the merge order puts the resolved value over the reference, not under it;
//  3. a check with no credential envelope at all still gets one, because the
//     resolved value has nowhere else to travel.

const paramRefBody = "grant_type=password&password=${param:sso-authtest-password}"

// seedOrgParam writes an org parameter exactly where the parameters API writes
// it — through paramkeys.StorageKey, never a hand-written prefix, so the test
// cannot drift from the namespace the resolver reads.
func (e *sysEnv) seedOrgParam(org *models.Organization, key, value string) {
	e.t.Helper()

	require.NoError(e.t, e.dbSvc.SetOrgParameter(
		e.t.Context(), org.UID, paramkeys.StorageKey(key), value, true))
}

// createRefCheck is createCloudCheck with a caller-supplied public config, so a
// reference can sit in a key that is NOT a secret field — the case the old
// apply-time resolution leaked into the public config column.
func (e *sysEnv) createRefCheck(org *models.Organization, slug string, config models.JSONMap) *models.Check {
	e.t.Helper()

	check := models.NewCheck(org.UID, slug, "http")
	check.Config = config
	check.Regions = []string{systemRegion}
	check.ConfirmationPeriodSeconds = 0

	require.NoError(e.t, e.dbSvc.CreateCheck(e.t.Context(), check))

	return check
}

// TestParamRefTravelsSealedToASystemAgent covers (1) and (2): a reference in a
// check's PUBLIC config is resolved at dispatch and the value ships inside the
// sealed envelope, while the wire config keeps the reference.
//
// This is the property that makes the resolve-at-execution design safe for
// deported agents. Resolving into the wire config would have been far simpler
// and would have put a password in cleartext in every claim frame, in the
// agent's logs and in any proxy in between.
func TestParamRefTravelsSealedToASystemAgent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	e.seedOrgParam(e.org, "sso-authtest-password", "hunter2")

	// No secret fields at all: `body` is a plain public key, which is exactly
	// the case the old apply-time resolution leaked into the config column.
	e.createRefCheck(e.org, "sso-login",
		models.JSONMap{"url": "https://sso.acme.com/token", "body": paramRefBody})

	conn, keys, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Len(jobsResp.Jobs, 1)

	job := jobsResp.Jobs[0]

	// (1a) The wire config still holds the REFERENCE — that is what a dump of
	// the frame, or the agent's own job log line, should show.
	r.Equal(paramRefBody, job.Config["body"], "the wire config must carry the reference")

	// (1b) …and the resolved value is nowhere in the clear.
	r.NotNil(job.ConfigSealed, "a resolved parameter needs an envelope even with no secret fields")
	r.NotContains(*job.ConfigSealed, "hunter2", "the envelope is ciphertext, not a wrapper")

	for key, value := range job.Config {
		if str, ok := value.(string); ok {
			r.NotContainsf(str, "hunter2", "wire config key %q carries the resolved value", key)
		}
	}

	// (2) The agent opens the envelope and merges it over its config — the
	// exact operation backend.WSBackend performs — and the resolved value wins.
	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *job.ConfigSealed)
	r.NoError(err)
	r.Equal("grant_type=password&password=hunter2", secrets["body"],
		"the resolved value must be the one the agent merges in")

	merged := credentials.MergeConfig(job.Config, secrets)
	r.Equal("grant_type=password&password=hunter2", merged["body"],
		"MergeConfig must put the overlay OVER the reference, not under it")
}

// TestParamRefInsideASecretFieldIsResolvedToo covers the other half: a
// reference can live in a key that IS a secret field, so it arrives at the
// server already inside the credential envelope and is only seen when that
// envelope is opened for re-sealing. Resolving only the public half would have
// shipped `${param:…}` to the agent as the password.
func TestParamRefInsideASecretFieldIsResolvedToo(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	e.seedOrgParam(e.org, "sso-authtest-password", "hunter2")

	check := e.createCloudCheck(e.org, "sealed-ref", map[string]any{
		"password": "${param:sso-authtest-password}",
	})
	r.NotNil(check.ConfigPrivate)

	conn, keys, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Len(jobsResp.Jobs, 1)

	job := jobsResp.Jobs[0]
	r.NotNil(job.ConfigSealed)

	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *job.ConfigSealed)
	r.NoError(err)
	r.Equal("hunter2", secrets["password"],
		"a reference inside the credential envelope must be resolved when it is opened")
}

// TestUnresolvableParamRefDropsTheJobForAnAgent is the failure half. A deleted
// parameter must not reach an agent at all: the job is dropped from the batch
// with an explicit error result, exactly as an unopenable credential envelope
// is. Dispatching it would send the literal `${param:…}` to the target.
func TestUnresolvableParamRefDropsTheJobForAnAgent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	// Deliberately NOT seeded.
	check := e.createRefCheck(e.org, "orphan-ref",
		models.JSONMap{"url": "https://sso.acme.com/token", "body": paramRefBody})

	conn, _, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Empty(jobsResp.Jobs, "a job whose reference cannot be resolved is never dispatched")

	results, err := e.dbSvc.GetLastResultForChecks(t.Context(), e.org.UID, []string{check.UID})
	r.NoError(err)

	got, ok := results[check.UID]
	r.True(ok, "the drop must be visible in the check's history, not silent")
	r.NotNil(got.Status)
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.Contains(fmt.Sprint(got.Output), "unresolved secret reference: param:sso-authtest-password")
}
