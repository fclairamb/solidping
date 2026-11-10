package checks_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// Browser-sealed check secrets (spec 2026-11-08-01).

// defineSealRegions declares @dc1 (and @dc2) as private regions of the org.
func defineSealRegions(t *testing.T, dbSvc db.Service, orgUID string) {
	t.Helper()
	require.NoError(t, regions.NewService(dbSvc).SetOrgCustomRegions(t.Context(), orgUID, []regions.RegionDefinition{
		{Slug: "dc1", Name: "DC1"}, {Slug: "dc2", Name: "DC2"},
	}))
}

// clientSeal plays the browser: seal the secrets to the recipients, keep the
// age file and the fingerprints only.
func clientSeal(t *testing.T, secrets map[string]any, recipients ...string) *checks.SealedSecretsRequest {
	t.Helper()
	r := require.New(t)

	envelope, err := credentials.SealForRecipients(recipients, secrets)
	r.NoError(err)

	var parsed struct {
		Fingerprints []string `json:"fingerprints"`
		CT           string   `json:"ct"`
	}
	r.NoError(json.Unmarshal([]byte(envelope), &parsed))

	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}

	return &checks.SealedSecretsRequest{Keys: keys, Fingerprints: parsed.Fingerprints, CT: parsed.CT}
}

func testNow() time.Time { return time.Now() }

// enrollAgentIn enrolls a fresh agent into any private region.
func enrollAgentIn(
	t *testing.T, dbSvc db.Service, orgUID, region string,
) (*models.Agent, *agentcrypto.AgentKeys) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	_, hash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(dbSvc.CreateAgentEnrollmentToken(
		ctx, models.NewAgentEnrollmentToken(orgUID, region, hash, time.Now().Add(time.Hour), nil),
	))

	agent, err := dbSvc.EnrollAgent(
		ctx, hash, "agent-"+region, keys.Ed25519PublicKey, keys.X25519Recipient,
		agentcrypto.KeyFingerprint(keys.Ed25519PublicKey),
	)
	r.NoError(err)

	return agent, keys
}

func sealedCheckRequest(
	slug string, sealed *checks.SealedSecretsRequest, regionList ...string,
) checks.CreateCheckRequest {
	return checks.CreateCheckRequest{
		Name: slug, Slug: slug, Type: "http", Regions: regionList,
		Config:        map[string]any{"url": "https://internal.example.com"},
		SealedSecrets: sealed,
	}
}

// sealingEnv builds the checks service, its database and a fresh org: SQLite
// for the plain tests, a shared embedded Postgres for TestBrowserSealing_Postgres.
type sealingEnv func(t *testing.T) (*checks.Service, db.Service, *models.Organization)

func sqliteSealingEnv(t *testing.T) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()

	return setupEncryptedChecksService(t)
}

func setupBrowserSealing(
	t *testing.T, env sealingEnv,
) (*checks.Service, db.Service, *models.Organization, *agentcrypto.AgentKeys) {
	t.Helper()

	svc, dbSvc, org := env(t)
	defineSealRegions(t, dbSvc, org.UID)
	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	return svc, dbSvc, org, keys
}

func TestBrowserSealedCreateStoresClientBlob(t *testing.T) {
	t.Parallel()
	testBrowserSealedCreateStoresClientBlob(t, sqliteSealingEnv)
}

func testBrowserSealedCreateStoresClientBlob(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	sealed := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("browser-http", sealed, sealTestRegion))
	r.NoError(err)
	r.NotNil(created.SecretsEncryption)
	r.Equal("browser", *created.SecretsEncryption)
	r.Equal([]string{"basicAuth"}, created.ConfigPrivateKeys)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Nil(row.ConfigPrivate)
	r.NotNil(row.ConfigSealed)
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(*row.ConfigSealed))
	r.Equal([]string{"basicAuth"}, privateKeys(t, row))
	r.NotContains(row.Config, "basicAuth")

	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:hunter2", secrets["basicAuth"])

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, created.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NotNil(jobs[0].ConfigSealed)
	r.Equal(*row.ConfigSealed, *jobs[0].ConfigSealed)
}

func TestBrowserSealedCreateRefusals(t *testing.T) {
	t.Parallel()
	testBrowserSealedCreateRefusals(t, sqliteSealingEnv)
}

func testBrowserSealedCreateRefusals(t *testing.T, env sealingEnv) {
	t.Helper()

	svc, _, org, keys := setupBrowserSealing(t, env)

	good := func() *checks.SealedSecretsRequest {
		return clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)
	}

	_, otherRecipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	tests := []struct {
		name    string
		mutate  func(req *checks.CreateCheckRequest)
		wantErr error
	}{
		{name: "positive control", mutate: func(*checks.CreateCheckRequest) {}},
		{
			name:    "shared region",
			mutate:  func(req *checks.CreateCheckRequest) { req.Regions = []string{"default"} },
			wantErr: checks.ErrSealedSecretsRequirePrivateRegions,
		},
		{
			name:    "mixed regions",
			mutate:  func(req *checks.CreateCheckRequest) { req.Regions = []string{sealTestRegion, "default"} },
			wantErr: checks.ErrSealedSecretsRequirePrivateRegions,
		},
		{
			// Spec §4 order: the regions are checked before the blob.
			name: "shared region and a bad blob",
			mutate: func(req *checks.CreateCheckRequest) {
				req.Regions = []string{"default"}
				req.SealedSecrets.CT = "%%%"
			},
			wantErr: checks.ErrSealedSecretsRequirePrivateRegions,
		},
		{
			name:    "undefined private region",
			mutate:  func(req *checks.CreateCheckRequest) { req.Regions = []string{"@nowhere"} },
			wantErr: checks.ErrSealedSecretsRequirePrivateRegions,
		},
		{
			name:    "key outside SecretFields",
			mutate:  func(req *checks.CreateCheckRequest) { req.SealedSecrets.Keys = []string{"url"} },
			wantErr: checks.ErrSealedSecretsInvalid,
		},
		{
			name: "key also in config",
			mutate: func(req *checks.CreateCheckRequest) {
				req.Config["basicAuth"] = "alice:hunter2"
			},
			wantErr: checks.ErrSealedSecretsInvalid,
		},
		{
			name:    "bad base64",
			mutate:  func(req *checks.CreateCheckRequest) { req.SealedSecrets.CT = "%%%" },
			wantErr: checks.ErrSealedSecretsInvalid,
		},
		{
			name: "oversized blob",
			mutate: func(req *checks.CreateCheckRequest) {
				req.SealedSecrets.CT = base64.StdEncoding.EncodeToString(make([]byte, 70*1024))
			},
			wantErr: checks.ErrSealedSecretsInvalid,
		},
		{
			name: "stanza count differs from fingerprints",
			mutate: func(req *checks.CreateCheckRequest) {
				req.SealedSecrets.Fingerprints = append(req.SealedSecrets.Fingerprints,
					credentials.RecipientFingerprint(otherRecipient))
			},
			wantErr: checks.ErrSealedSecretsInvalid,
		},
		{
			name: "stale fingerprints",
			mutate: func(req *checks.CreateCheckRequest) {
				req.SealedSecrets = clientSeal(t, map[string]any{"basicAuth": "a:b"}, otherRecipient)
			},
			wantErr: checks.ErrSealingRecipientsChanged,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := sealedCheckRequest("refusal-"+strings.Repeat("x", i+1), good(), sealTestRegion)
			tc.mutate(&req)

			_, err := svc.CreateCheck(t.Context(), org.Slug, req)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestBrowserSealedStaleFingerprintsCarryCurrentRecipients(t *testing.T) {
	t.Parallel()
	testBrowserSealedStaleFingerprintsCarryCurrentRecipients(t, sqliteSealingEnv)
}

func testBrowserSealedStaleFingerprintsCarryCurrentRecipients(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, _, org, keys := setupBrowserSealing(t, env)

	_, otherRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	_, err = svc.CreateCheck(t.Context(), org.Slug, sealedCheckRequest("stale",
		clientSeal(t, map[string]any{"basicAuth": "a:b"}, otherRecipient), sealTestRegion))

	var changed *checks.SealingRecipientsChangedError
	r.ErrorAs(err, &changed)
	r.Len(changed.Recipients, 1)
	r.Equal(keys.X25519Recipient, changed.Recipients[0].Recipient)
	r.Equal(sealing.RecipientKindAgent, changed.Recipients[0].Kind)
}

func TestBrowserSealedModeSwitches(t *testing.T) {
	t.Parallel()
	testBrowserSealedModeSwitches(t, sqliteSealingEnv)
}

func testBrowserSealedModeSwitches(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	// Server-side and browser-mode checks coexist.
	server, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "server-mode", Slug: "server-mode", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{"url": "https://internal.example.com", "username": "u", "password": "p"},
	})
	r.NoError(err)
	r.Equal("server", *server.SecretsEncryption)

	browser, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("browser-mode",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)
	r.Equal("browser", *browser.SecretsEncryption)

	// Plaintext secrets on a browser-mode check switch it to server-side.
	cfg := map[string]any{"url": "https://internal.example.com", "username": "bob", "password": "new"}
	updated, err := svc.UpdateCheck(ctx, org.Slug, browser.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.NoError(err)
	r.Equal("server", *updated.SecretsEncryption)

	row, err := dbSvc.GetCheck(ctx, org.UID, browser.UID)
	r.NoError(err)
	r.Equal(credentials.SealOriginServer, credentials.SealedOrigin(*row.ConfigSealed))

	// sealedSecrets on a server-side private-only check switches it to browser.
	updated, err = svc.UpdateCheck(ctx, org.Slug, server.UID, &checks.UpdateCheckRequest{
		SealedSecrets: clientSeal(t, map[string]any{"basicAuth": "u:p2"}, keys.X25519Recipient),
	})
	r.NoError(err)
	r.Equal("browser", *updated.SecretsEncryption)

	row, err = dbSvc.GetCheck(ctx, org.UID, server.UID)
	r.NoError(err)
	r.Nil(row.ConfigPrivate)
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(*row.ConfigSealed))
}

func TestBrowserSealingRequiredMode(t *testing.T) {
	t.Parallel()
	testBrowserSealingRequiredMode(t, sqliteSealingEnv)
}

func testBrowserSealingRequiredMode(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	r.NoError(sealing.SetBrowserSealingMode(ctx, dbSvc, org.UID, sealing.ModeRequired))

	plaintext := checks.CreateCheckRequest{
		Name: "plain", Slug: "plain", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{"url": "https://internal.example.com", "username": "u", "password": "p"},
	}
	_, err := svc.CreateCheck(ctx, org.Slug, plaintext)
	r.ErrorIs(err, checks.ErrSecretsMustBeSealed)

	// A check without secrets on a shared region still saves.
	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "no-secret", Slug: "no-secret", Type: "http", Regions: []string{"default"},
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	// Positive control: the same secrets, sealed, save.
	sealed, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("sealed",
		clientSeal(t, map[string]any{"basicAuth": "u:p"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	// A PATCH carrying plaintext secrets is refused too; one without is not.
	cfg := map[string]any{"url": "https://internal.example.com", "password": "p"}
	_, err = svc.UpdateCheck(ctx, org.Slug, sealed.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.ErrorIs(err, checks.ErrSecretsMustBeSealed)

	name := "renamed"
	_, err = svc.UpdateCheck(ctx, org.Slug, sealed.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)

	// The non-compliant list names the shared/mixed and server-encrypted
	// checks, never a browser-sealed or secret-less one.
	r.NoError(sealing.SetBrowserSealingMode(ctx, dbSvc, org.UID, sealing.ModeOptional))
	_, err = svc.CreateCheck(ctx, org.Slug, plaintext)
	r.NoError(err)
	r.NoError(sealing.SetBrowserSealingMode(ctx, dbSvc, org.UID, sealing.ModeRequired))

	list, err := svc.ListNonCompliantChecks(ctx, org.Slug)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal("plain", list[0].Slug)
	r.Equal("serverEncrypted", list[0].Reason)
}

func TestBrowserSealedPatchReplacementRules(t *testing.T) {
	t.Parallel()
	testBrowserSealedPatchReplacementRules(t, sqliteSealingEnv)
}

func testBrowserSealedPatchReplacementRules(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("replace",
		clientSeal(t, map[string]any{
			"basicAuth":     "alice:hunter2",
			"secretHeaders": map[string]any{"X-Token": "t"},
		}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	// A PATCH whose blob omits a stored key is refused...
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		SealedSecrets: clientSeal(t, map[string]any{"basicAuth": "alice:new"}, keys.X25519Recipient),
	})
	r.ErrorIs(err, checks.ErrSealedSecretsInvalid)

	// ...unless the PATCH clears it explicitly (positive control).
	cfg := map[string]any{"url": "https://internal.example.com", "secretHeaders": nil}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config:        &cfg,
		SealedSecrets: clientSeal(t, map[string]any{"basicAuth": "alice:new"}, keys.X25519Recipient),
	})
	r.NoError(err)

	// A region-only PATCH of a browser-sealed check is refused...
	dc2 := []string{"@dc2"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Regions: &dc2})
	r.ErrorIs(err, checks.ErrSealingRecipientsChanged)

	shared := []string{"default"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Regions: &shared})
	r.ErrorIs(err, checks.ErrSealingRecipientsChanged)

	// Spec §4 order on PATCH too: moving to a shared region with a malformed
	// blob is refused for the regions, not for the blob.
	badBlob := clientSeal(t, map[string]any{"basicAuth": "alice:new"}, keys.X25519Recipient)
	badBlob.CT = "%%%"
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Regions: &shared, SealedSecrets: badBlob,
	})
	r.ErrorIs(err, checks.ErrSealedSecretsRequirePrivateRegions)

	// ...and accepted with a new blob for the new regions' recipients.
	_, dc2Keys := enrollAgentIn(t, dbSvc, org.UID, "@dc2")
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Regions:       &dc2,
		SealedSecrets: clientSeal(t, map[string]any{"basicAuth": "alice:new"}, dc2Keys.X25519Recipient),
	})
	r.NoError(err)

	// A rename (no config) keeps the blob.
	name := "still-sealed"
	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)
	r.Equal("browser", *updated.SecretsEncryption)
}

// TestServerSealedOnlyPartialPatchRefused fixes the "To verify" of the spec: a
// PATCH carrying only some secret keys of a server-sealed-only check used to
// drop the others silently.
func TestServerSealedOnlyPartialPatchRefused(t *testing.T) {
	t.Parallel()
	testServerSealedOnlyPartialPatchRefused(t, sqliteSealingEnv)
}

func testServerSealedOnlyPartialPatchRefused(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "partial", Slug: "partial", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url": "https://internal.example.com", "username": "u", "password": "p",
			"secretHeaders": map[string]any{"X-Token": "t"},
		},
	})
	r.NoError(err)

	cfg := map[string]any{"url": "https://internal.example.com", "secretHeaders": map[string]any{"X-Token": "t2"}}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.ErrorIs(err, checks.ErrSealedSecretsInvalid)

	// Positive control: both keys sent.
	cfg = map[string]any{
		"url": "https://internal.example.com", "username": "u", "password": "p2",
		"secretHeaders": map[string]any{"X-Token": "t2"},
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.NoError(err)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("u:p2", secrets["basicAuth"])
	r.Contains(secrets, "secretHeaders")
}

func TestSealingRecipientsEndpoint(t *testing.T) {
	t.Parallel()
	testSealingRecipientsEndpoint(t, sqliteSealingEnv)
}

func testSealingRecipientsEndpoint(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	revoked, _ := enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)
	r.NoError(dbSvc.RevokeAgent(ctx, org.UID, revoked.UID))

	resp, err := svc.SealingRecipients(ctx, org.Slug, []string{sealTestRegion})
	r.NoError(err)
	r.Equal(sealing.ModeOptional, resp.BrowserSealingMode)
	got := resp.Data
	r.Len(got, 1, "active agents only")
	r.Equal(sealing.RecipientKindAgent, got[0].Kind)
	r.Equal(credentials.RecipientFingerprint(keys.X25519Recipient), got[0].Fingerprint)

	_, regionRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	key, err := sealing.NewRegionKey(regionRecipient, testNow())
	r.NoError(err)
	r.NoError(sealing.SetRegionKeys(ctx, dbSvc, org.UID, map[string]sealing.RegionKey{sealTestRegion: key}))

	resp, err = svc.SealingRecipients(ctx, org.Slug, []string{sealTestRegion})
	r.NoError(err)
	got = resp.Data
	r.Len(got, 1, "the region key only for a shared region")
	r.Equal(sealing.RecipientKindRegion, got[0].Kind)
	r.Equal(regionRecipient, got[0].Recipient)

	// Other orgs' regions and cloud regions are refused.
	other := models.NewOrganization("other-org", "Other")
	r.NoError(dbSvc.CreateOrganization(ctx, other))
	r.NoError(regions.NewService(dbSvc).SetOrgCustomRegions(ctx, other.UID, []regions.RegionDefinition{
		{Slug: "elsewhere", Name: "Elsewhere"},
	}))

	_, err = svc.SealingRecipients(ctx, org.Slug, []string{"@elsewhere"})
	r.ErrorIs(err, checks.ErrSealedSecretsRequirePrivateRegions)

	_, err = svc.SealingRecipients(ctx, org.Slug, []string{"default"})
	r.ErrorIs(err, checks.ErrSealedSecretsRequirePrivateRegions)

	// No region: the mode only.
	resp, err = svc.SealingRecipients(ctx, org.Slug, nil)
	r.NoError(err)
	r.Empty(resp.Data)
}

func TestSharedRegionKeySealing(t *testing.T) {
	t.Parallel()
	testSharedRegionKeySealing(t, sqliteSealingEnv)
}

func testSharedRegionKeySealing(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	// A per-agent check exists before the region switches to a shared key.
	before, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("per-agent",
		clientSeal(t, map[string]any{"basicAuth": "a:b"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	regionIdentity, regionRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	key, err := sealing.NewRegionKey(regionRecipient, testNow())
	r.NoError(err)
	r.NoError(sealing.SetRegionKeys(ctx, dbSvc, org.UID, map[string]sealing.RegionKey{sealTestRegion: key}))

	got, err := svc.GetCheck(ctx, org.Slug, before.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(got.NeedsReseal)
	r.True(*got.NeedsReseal, "switching the region to a shared key flags its per-agent blobs")

	// Server-side sealing of a shared-region check uses exactly one recipient.
	server, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "shared-server", Slug: "shared-server", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{"url": "https://internal.example.com", "username": "u", "password": "p"},
	})
	r.NoError(err)

	row, err := dbSvc.GetCheck(ctx, org.UID, server.UID)
	r.NoError(err)
	fps, err := credentials.SealedRecipientFingerprints(*row.ConfigSealed)
	r.NoError(err)
	r.Equal([]string{key.Fingerprint}, fps)

	secrets, err := credentials.UnsealWithIdentity(regionIdentity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("u:p", secrets["basicAuth"])

	// Enrolling another agent sets no needsReseal on a shared region.
	enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)

	got, err = svc.GetCheck(ctx, org.Slug, server.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.False(*got.NeedsReseal)

	// Rotating the key flags it.
	_, rotated, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	newKey, err := sealing.NewRegionKey(rotated, testNow())
	r.NoError(err)
	r.NoError(sealing.SetRegionKeys(ctx, dbSvc, org.UID, map[string]sealing.RegionKey{sealTestRegion: newKey}))

	got, err = svc.GetCheck(ctx, org.Slug, server.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.True(*got.NeedsReseal)
}

func TestResealChecksList(t *testing.T) {
	t.Parallel()
	testResealChecksList(t, sqliteSealingEnv)
}

// testResealChecksList: the Re-seal page lists a check once a new agent joins
// its `perAgent` region, and not before (spec 2026-11-08-01 §7).
func testResealChecksList(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	sealed, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("reseal-me",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "no-secrets", Slug: "no-secrets", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{"url": "https://internal.example.com"},
	})
	r.NoError(err)

	before, err := svc.ListResealChecks(ctx, org.Slug)
	r.NoError(err)
	r.Empty(before, "sealed for the current agents: nothing to re-seal")

	enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)

	after, err := svc.ListResealChecks(ctx, org.Slug)
	r.NoError(err)
	r.Len(after, 1)
	r.Equal(sealed.UID, after[0].UID)
	r.Equal([]string{sealTestRegion}, after[0].Regions)

	_, err = svc.ListResealChecks(ctx, "no-such-org")
	r.ErrorIs(err, checks.ErrOrganizationNotFound)
}
