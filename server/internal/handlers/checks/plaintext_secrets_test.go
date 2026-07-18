package checks_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// validSSHPrivateKeyPEM generates a fresh, parseable PKCS#8 ed25519 private key
// in PEM form — the SSH checker validates the key material, so a placeholder
// string won't do. The value is what the test asserts never leaks.
func validSSHPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// setupPlaintextChecksService builds a checks service whose credentials layer
// has NO master key configured — the self-hosted plaintext fallback that this
// spec makes safe. Secrets must still be split out of the public config column
// (into a plaintext envelope), never merged back into it.
func setupPlaintextChecksService(
	t *testing.T,
) (*checks.Service, db.Service, credentials.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, newMemDEKStore())
	require.NoError(t, err)
	require.False(t, creds.Enabled(), "this suite is the no-master-key path")

	org := models.NewOrganization("plaintext-test", "Plaintext Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := checks.NewService(dbSvc, nil, creds, nil)

	return svc, dbSvc, creds, org
}

// TestPlaintextModeSSHKeyNeverLeaks is the headline acceptance test: with no
// master key, an ssh check's private key never appears in the API response nor
// in the public config column, is advertised via configPrivateKeys, and — the
// positive control — is still recoverable at the dispatch boundary so the check
// keeps executing.
func TestPlaintextModeSSHKeyNeverLeaks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupPlaintextChecksService(t)
	ctx := t.Context()

	secretKey := validSSHPrivateKeyPEM(t)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "ssh-secret",
		Slug: "ssh-secret",
		Type: "ssh",
		Config: map[string]any{
			"host":        "lan.internal",
			"private_key": secretKey,
		},
	})
	r.NoError(err)

	// The API response must not carry the secret value, but must advertise it.
	r.NotContains(created.Config, "private_key", "the private key must never be in an API response")
	r.Contains(created.ConfigPrivateKeys, "private_key")
	r.Equal("lan.internal", created.Config["host"])

	// The stored PUBLIC column must not carry the secret either — it lives in a
	// plaintext envelope in config_private.
	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(row.Config, "private_key", "the public config column must never carry the secret")
	r.NotNil(row.ConfigPrivate)
	r.True(credentials.IsPlaintextEnvelope(*row.ConfigPrivate),
		"no master key ⇒ the private side is a plaintext envelope, not a merge back into public config")

	// LIST must not leak it either.
	listed, err := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(listed.Data, 1)
	r.NotContains(listed.Data[0].Config, "private_key", "LIST must not leak the secret")
	r.Contains(listed.Data[0].ConfigPrivateKeys, "private_key")

	// Positive control: the secret is recoverable at the claim/dispatch boundary
	// (with NO master key), so the check actually executes with its credential.
	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, created.UID)
	r.NoError(err)
	r.NotEmpty(jobs, "creating a check must create at least one dispatch job")

	job := jobs[0]
	r.NotNil(job.ConfigPrivate, "the job carries the split-out secret envelope")
	r.True(credentials.IsPlaintextEnvelope(*job.ConfigPrivate))
	r.NotContains(job.Config, "private_key", "the job's public config must not carry the secret pre-merge")

	outcome, err := checkjobsvc.MergeJobSecrets(ctx, creds, job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeMerged, outcome)
	r.Equal(secretKey, job.Config["private_key"],
		"the check must run with the real credential merged in at dispatch")
}

// TestPlaintextModeHTTPBasicAuthNeverLeaks is the http/basicAuth mirror of the
// ssh test, and asserts the PATCH round-trip: an edit that omits the secret
// (what the dashboard sends, since it never receives it) preserves it.
func TestPlaintextModeHTTPBasicAuthNeverLeaks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupPlaintextChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "http-secret",
		Slug: "http-secret",
		Type: "http",
		Config: map[string]any{
			"url":      "https://example.com",
			"username": "probe",
			"password": "hunter2",
		},
	})
	r.NoError(err)
	r.NotContains(created.Config, "basicAuth", "the folded credential must never be in an API response")
	r.NotContains(created.Config, "password")
	r.NotContains(created.Config, "username")
	r.Equal([]string{"basicAuth"}, created.ConfigPrivateKeys)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(row.Config, "basicAuth", "the public config column must never carry the credential")
	r.True(credentials.IsPlaintextEnvelope(*row.ConfigPrivate))

	// PATCH that omits the secret (dashboard edit of an unrelated field) must
	// preserve the credential — the negative-proof.
	newName := "http-secret-renamed"
	configWithoutSecret := map[string]any{"url": "https://example.com/health"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Name:   &newName,
		Config: &configWithoutSecret,
	})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(after.ConfigPrivate, "PATCH without the secret wiped the plaintext-mode credential")
	r.True(credentials.IsPlaintextEnvelope(*after.ConfigPrivate))
	r.Equal([]string{"basicAuth"}, privateKeys(t, after))
	r.Equal("https://example.com/health", after.Config["url"])

	// Positive control: the preserved credential decodes back to the real value.
	private, err := creds.DecryptForOrg(ctx, org.UID, *after.ConfigPrivate)
	r.NoError(err)
	r.Equal("probe:hunter2", private["basicAuth"], "the round-trip must not lose the credential")
}

// TestConvertResponseRedactsUnsplitRow is the defense-in-depth test: even a row
// whose secret was (wrongly) left in the public column — a not-yet-migrated row
// or a future write-path bug — is redacted at read time and the key is
// advertised, so no secret value can leave the API regardless of storage state.
func TestConvertResponseRedactsUnsplitRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "leaky-ssh",
		Slug:   "leaky-ssh",
		Type:   "ssh",
		Config: map[string]any{"host": "lan.internal"},
	})
	r.NoError(err)

	// Simulate a leaked/legacy row: plant the secret directly in the PUBLIC
	// config column with no envelope at all (bypassing the service split).
	leaky := models.JSONMap{"host": "lan.internal", "private_key": "LEAKED-KEY"}
	r.NoError(dbSvc.UpdateCheck(ctx, created.UID, &models.CheckUpdate{
		Config:             &leaky,
		ClearConfigPrivate: true,
	}))

	// GET must still not leak it — the read-time redaction strips it and
	// advertises the key.
	got, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotContains(got.Config, "private_key", "read-time redaction must strip an un-split secret")
	r.Contains(got.ConfigPrivateKeys, "private_key")
	r.Equal("lan.internal", got.Config["host"])
}
