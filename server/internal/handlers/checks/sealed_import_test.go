package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// sealedSecrets in config-as-code (spec 2026-11-10-03 §4).

func sealedDoc(org, slug string, sealed *checks.SealedSecretsRequest) *checks.ExportDocument {
	return &checks.ExportDocument{Version: 1, Organization: org, Checks: []checks.ExportCheck{{
		Name: slug, Slug: slug, Type: "http", Enabled: true, Regions: []string{sealTestRegion},
		Config:        map[string]any{"url": "https://internal.example.com"},
		SealedSecrets: sealed,
	}}}
}

func TestImportSealedSecretsStoresBrowserBlob(t *testing.T) {
	t.Parallel()
	testImportSealedSecretsStoresBrowserBlob(t, sqliteSealingEnv)
}

func testImportSealedSecretsStoresBrowserBlob(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	sealed := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)
	doc := sealedDoc(org.Slug, "doc-sealed", sealed)

	// The dry run validates and stores nothing.
	dry, err := svc.ImportChecks(ctx, org.Slug, doc, true)
	r.NoError(err)
	r.Empty(dry.Errors)
	r.Equal(1, dry.Created)

	_, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "doc-sealed")
	r.Error(err, "a dry run stores nothing")

	res, err := svc.ImportChecks(ctx, org.Slug, doc, false)
	r.NoError(err)
	r.Empty(res.Errors)
	r.Equal(1, res.Created)

	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "doc-sealed")
	r.NoError(err)
	r.NotNil(row.ConfigSealed)
	r.Nil(row.ConfigPrivate)
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(*row.ConfigSealed))

	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:hunter2", secrets["basicAuth"])

	// A re-import with a fresh blob updates the check (an update, never
	// "unchanged": ciphertexts cannot be compared).
	replaced := clientSeal(t, map[string]any{"basicAuth": "bob:s3cret"}, keys.X25519Recipient)

	again, err := svc.ImportChecks(ctx, org.Slug, sealedDoc(org.Slug, "doc-sealed", replaced), false)
	r.NoError(err)
	r.Empty(again.Errors)
	r.Equal(1, again.Updated)

	row, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "doc-sealed")
	r.NoError(err)

	secrets, err = credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("bob:s3cret", secrets["basicAuth"])

	// Export keeps stripping secrets: no sealedSecrets, no ciphertext.
	exported, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	raw, err := json.Marshal(exported)
	r.NoError(err)
	r.NotContains(string(raw), "sealedSecrets")
	r.NotContains(string(raw), sealed.CT)
}

func TestImportSealedSecretsStaleFingerprintsAreItemErrors(t *testing.T) {
	t.Parallel()
	testImportSealedSecretsStaleFingerprintsAreItemErrors(t, sqliteSealingEnv)
}

func testImportSealedSecretsStaleFingerprintsAreItemErrors(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	sealed := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)

	// A second agent joins: the blob no longer lists every recipient.
	enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)

	for _, dryRun := range []bool{true, false} {
		res, err := svc.ImportChecks(ctx, org.Slug, sealedDoc(org.Slug, "doc-stale", sealed), dryRun)
		r.NoError(err)
		r.Len(res.Errors, 1, "dryRun=%v", dryRun)
		r.Equal("SEALING_RECIPIENTS_CHANGED", res.Errors[0].Code)
		r.Len(res.Errors[0].Recipients, 2, "the item error carries the current recipients")
		r.Equal(0, res.Created)
	}

	_, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "doc-stale")
	r.Error(err)

	// The update path refuses it the same way, dry run included.
	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "doc-existing", Slug: "doc-existing", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{"url": "https://internal.example.com"},
	})
	r.NoError(err)

	for _, dryRun := range []bool{true, false} {
		res, importErr := svc.ImportChecks(ctx, org.Slug, sealedDoc(org.Slug, *created.Slug, sealed), dryRun)
		r.NoError(importErr)
		r.Len(res.Errors, 1, "dryRun=%v", dryRun)
		r.Equal("SEALING_RECIPIENTS_CHANGED", res.Errors[0].Code)
	}

	// Apply's dry run reports it too.
	applied, err := svc.ApplyChecks(ctx, org.Slug, sealedDoc(org.Slug, "doc-apply", sealed), checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Len(applied.Errors, 1)
	r.Equal("SEALING_RECIPIENTS_CHANGED", applied.Errors[0].Code)

	// A blob sealed to a malformed ciphertext is a plain item error.
	bad := &checks.SealedSecretsRequest{Keys: []string{"basicAuth"}, Fingerprints: sealed.Fingerprints, CT: "not-age"}

	res, err := svc.ImportChecks(ctx, org.Slug, sealedDoc(org.Slug, "doc-bad", bad), true)
	r.NoError(err)
	r.Len(res.Errors, 1)
	r.Empty(res.Errors[0].Code)
	r.Contains(res.Errors[0].Error, "sealedSecrets")
}
