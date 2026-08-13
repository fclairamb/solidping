package checks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// sealTestRegion is the ORG-RELATIVE private region string (spec
// 2026-08-13-01): no org slug inside it, so an org rename cannot desync the
// agent's bound region from the check's.
const sealTestRegion = "@dc1"

// enrollSealAgent enrolls a fresh agent into the given private region and
// returns its keys (so tests can prove/unprove decryptability).
func enrollSealAgent(t *testing.T, dbSvc db.Service, orgUID, name string) *agentcrypto.AgentKeys {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	_, hash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(dbSvc.CreateAgentEnrollmentToken(
		ctx, models.NewAgentEnrollmentToken(orgUID, sealTestRegion, hash, time.Now().Add(time.Hour), nil),
	))

	_, err = dbSvc.EnrollAgent(
		ctx, hash, name, keys.Ed25519PublicKey, keys.X25519Recipient,
		agentcrypto.KeyFingerprint(keys.Ed25519PublicKey),
	)
	r.NoError(err)

	return keys
}

// TestSealedOnlyCheckStoresNoServerDecryptableCopy is the phase-2 headline: a
// check targeting ONLY a private region stores its secrets sealed to the
// region's agents — config_private stays NULL, so the server holds nothing it
// could decrypt, while the agent's identity opens the blob.
func TestSealedOnlyCheckStoresNoServerDecryptableCopy(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-http",
		Slug:    "sealed-http",
		Type:    "http",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url":      "https://internal.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)
	r.NotContains(created.Config, "password")
	r.NotContains(created.Config, "username")
	r.Contains(created.ConfigPrivateKeys, "basicAuth")

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Nil(row.ConfigPrivate, "sealed-only: the server must hold NO org-DEK copy")
	r.NotNil(row.ConfigSealed, "the sealed blob must be stored")
	r.NotContains(row.Config, "password", "no plaintext secret in the public column")
	r.NotContains(row.Config, "username", "the username half is inside the sealed credential")

	// Only the agent's X25519 identity opens it.
	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:hunter2", secrets["basicAuth"])

	// The private-region job row carries the sealed blob for dispatch.
	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, created.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NotNil(jobs[0].ConfigSealed)
}

// TestMixedRegionsDualStore: a check targeting both a private and a cloud
// region stores the v1 envelope (cloud dispatch) AND the sealed blob (agents).
func TestMixedRegionsDualStore(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "mixed-http",
		Slug:    "mixed-http",
		Type:    "http",
		Regions: []string{sealTestRegion, "default"},
		Config: map[string]any{
			"url":      "https://example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(row.ConfigPrivate, "mixed: the v1 envelope must exist for cloud dispatch")
	r.NotNil(row.ConfigSealed, "mixed: the sealed blob must exist for agents")
	r.NotContains(row.Config, "password")

	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:hunter2", secrets["basicAuth"])
}

// TestSealedOnlyPatchKeepsBlobWhenSecretsAbsent pins the PATCH contract: the
// server cannot partially merge what it cannot read, so a PATCH without secret
// values keeps the previous sealed blob byte-for-byte.
func TestSealedOnlyPatchKeepsBlobWhenSecretsAbsent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-patch",
		Slug:    "sealed-patch",
		Type:    "http",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url":      "https://internal.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed)
	originalBlob := *before.ConfigSealed

	// PATCH the URL only — no secrets in the request.
	patched := map[string]any{"url": "https://internal2.example.com"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patched})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(after.ConfigSealed)
	r.Equal(originalBlob, *after.ConfigSealed, "the sealed blob must be kept AS-IS when secrets are absent")
	r.Equal("https://internal2.example.com", after.Config["url"])

	// PATCH WITH a new secret — the blob must be replaced by a fresh one the
	// agent can still open.
	patched2 := map[string]any{"url": "https://internal2.example.com", "username": "alice", "password": "new-secret"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patched2})
	r.NoError(err)

	final, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(final.ConfigSealed)
	r.NotEqual(originalBlob, *final.ConfigSealed, "providing secrets must mint a fresh blob")

	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *final.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:new-secret", secrets["basicAuth"])
}

// TestNeedsResealSurfacedOnMembershipChange: after a second agent enrolls, the
// sealed-only check reports needsReseal until its credentials are re-saved.
func TestNeedsResealSurfacedOnMembershipChange(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	enrollSealAgent(t, dbSvc, org.UID, "agent-1")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "reseal-check",
		Slug:    "reseal-check",
		Type:    "http",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url":      "https://internal.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	// In step with the (single) agent: no re-seal needed.
	got, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(got.NeedsReseal)
	r.False(*got.NeedsReseal)

	// A second agent joins the region: the blob no longer covers the active set.
	enrollSealAgent(t, dbSvc, org.UID, "agent-2")

	got, err = svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(got.NeedsReseal)
	r.True(*got.NeedsReseal, "a new agent in the region must flag needs-re-seal")

	// Re-saving the credentials re-seals to both agents and clears the flag.
	patched := map[string]any{"url": "https://internal.example.com", "username": "alice", "password": "hunter2"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patched})
	r.NoError(err)

	got, err = svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(got.NeedsReseal)
	r.False(*got.NeedsReseal, "re-saving credentials must clear the flag")
}

// TestPrivateOnlyCheckWithoutAgentsIsRejected pins Decision 4's unconditional
// invariant: a check targeting ONLY private regions stores its secrets
// sealed-only. With no agent to seal to there is no lawful way to store them,
// so the WRITE IS REFUSED — the server must never fall back to a
// server-decryptable copy (and a check with no sealed blob could not run on an
// agent anyway).
func TestPrivateOnlyCheckWithoutAgentsIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "early-check",
		Slug:    "early-check",
		Type:    "http",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url":      "https://internal.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.ErrorIs(err, checks.ErrNoAgentsToSealTo)
	// The message names the region the operator must enroll an agent into.
	r.Contains(err.Error(), sealTestRegion)

	// Nothing was persisted — in particular no server-decryptable envelope.
	_, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "early-check")
	r.Error(err, "the rejected check must not exist")
}

// TestPrivateOnlyPatchWithoutAgentsIsRejected: the same invariant on the PATCH
// path — re-saving credentials for a private-only check with no active agent
// (e.g. after the only agent was revoked) is refused rather than silently
// writing a v1 envelope.
func TestPrivateOnlyPatchWithoutAgentsIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-check",
		Slug:    "sealed-check",
		Type:    "http",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url":      "https://internal.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed)
	r.Nil(before.ConfigPrivate)
	_ = keys

	// The region's only agent is revoked -> no recipients left.
	agents, err := dbSvc.ListActiveAgentsByRegion(ctx, org.UID, sealTestRegion)
	r.NoError(err)
	r.Len(agents, 1)
	r.NoError(dbSvc.RevokeAgent(ctx, org.UID, agents[0].UID))

	// Re-saving the credentials is refused...
	patch := map[string]any{"url": "https://internal.example.com", "username": "alice", "password": "new-secret"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.ErrorIs(err, checks.ErrNoAgentsToSealTo)

	// ...and the stored row is untouched: still sealed-only, no v1 envelope.
	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Nil(after.ConfigPrivate, "a refused write must never leave a server-decryptable copy")
	r.NotNil(after.ConfigSealed)
	r.Equal(*before.ConfigSealed, *after.ConfigSealed)
}

// TestMixedRegionsWithoutAgentsStillWrites: the invariant is specific to
// private-ONLY checks. A mixed private+cloud check has a legitimate
// server-side consumer (the cloud workers), so it still writes its v1
// envelope and is simply flagged needs-re-seal until an agent enrolls.
func TestMixedRegionsWithoutAgentsStillWrites(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "mixed-early",
		Slug:    "mixed-early",
		Type:    "http",
		Regions: []string{sealTestRegion, "default"},
		Config: map[string]any{
			"url":      "https://example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(row.ConfigPrivate, "the cloud side still needs the org-DEK envelope")
	r.Nil(row.ConfigSealed, "nothing to seal to yet")

	got, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(got.NeedsReseal)
	r.True(*got.NeedsReseal, "agents cannot run it until re-sealed — flag it")
}
