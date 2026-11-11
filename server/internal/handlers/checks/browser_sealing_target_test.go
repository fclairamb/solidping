package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// The target binding of browser-sealed secrets (spec 2026-11-11-01): a write
// that changes where the secrets go must bring new ones.

func TestSealedTargetChangeNeedsNewSecrets(t *testing.T) {
	t.Parallel()
	testSealedTargetChangeNeedsNewSecrets(t, sqliteSealingEnv)
}

func testSealedTargetChangeNeedsNewSecrets(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, env)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("target",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	stored := func() string {
		row, getErr := dbSvc.GetCheck(ctx, org.UID, created.UID)
		r.NoError(getErr)
		r.NotNil(row.ConfigSealed)

		return *row.ConfigSealed
	}
	blob := stored()

	// A new URL without new secrets: 409 SEALED_TARGET_CHANGED.
	evil := map[string]any{"url": "https://evil.example"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &evil})
	r.ErrorIs(err, checks.ErrSealedTargetChanged)
	r.Equal(blob, stored(), "the refused write leaves the blob alone")

	// A tunnel added is a target change too.
	tunneled := map[string]any{"url": "https://internal.example.com", "tunnelCheckUid": "bastion"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &tunneled})
	r.ErrorIs(err, checks.ErrSealedTargetChanged)

	// Only the timeout (and the URL written differently): 200, blob kept.
	timeout := map[string]any{"url": "https://Internal.example.com:443", "timeout": "9s"}
	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &timeout})
	r.NoError(err)
	r.Equal("browser", *updated.SecretsEncryption)
	r.Equal(blob, stored())

	// The new URL with new sealed secrets: accepted, new blob.
	moved := map[string]any{"url": "https://other.acme.com"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config:        &moved,
		SealedSecrets: clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient),
	})
	r.NoError(err)
	r.NotEqual(blob, stored())

	// Clearing the secrets with the move drops the blob: nothing left to bind.
	cleared := map[string]any{"url": "https://third.acme.com", "basicAuth": nil}
	updated, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cleared})
	r.NoError(err)
	r.Nil(updated.SecretsEncryption)
}

// TestSealedTargetBulkUpdate: a bulk update cannot carry a config, so it can
// never move a sealed check's target; a period change keeps the blob.
func TestSealedTargetBulkUpdate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, sqliteSealingEnv)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("bulk-target",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)

	_, err = svc.BulkUpdateChecks(ctx, org.Slug, &checks.BulkUpdateChecksRequest{
		CheckUIDs: []string{created.UID},
		Changes:   json.RawMessage(`{"config":{"url":"https://evil.example"}}`),
	})
	r.Error(err, "config is not a bulk field")

	resp, err := svc.BulkUpdateChecks(ctx, org.Slug, &checks.BulkUpdateChecksRequest{
		CheckUIDs: []string{created.UID},
		Changes:   json.RawMessage(`{"period":"00:05:00"}`),
	})
	r.NoError(err)
	r.Empty(resp.Skipped)
	r.Len(resp.Data, 1)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal(*before.ConfigSealed, *after.ConfigSealed)
	r.Equal("https://internal.example.com", after.Config["url"])
}

// TestSealedTargetConfigAsCode: /checks/apply and /checks/import refuse a
// document that moves a sealed check's target without sealedSecrets, dry run
// included, and accept one that only changes the timeout.
func TestSealedTargetConfigAsCode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, sqliteSealingEnv)
	ctx := t.Context()

	_, err := svc.ApplyChecks(ctx, org.Slug, sealedDoc(org.Slug, "cac-target",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)), checks.ApplyOptions{})
	r.NoError(err)

	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "cac-target")
	r.NoError(err)
	r.NotNil(row.ConfigSealed)
	blob := *row.ConfigSealed

	moved := sealedDoc(org.Slug, "cac-target", nil)
	moved.Checks[0].Config = map[string]any{"url": "https://evil.example"}

	for _, dryRun := range []bool{true, false} {
		applied, applyErr := svc.ApplyChecks(ctx, org.Slug, moved, checks.ApplyOptions{DryRun: dryRun})
		r.NoError(applyErr)
		r.Len(applied.Errors, 1, "apply dryRun=%v", dryRun)
		r.Equal("SEALED_TARGET_CHANGED", applied.Errors[0].Code)

		imported, importErr := svc.ImportChecks(ctx, org.Slug, moved, dryRun)
		r.NoError(importErr)
		r.Len(imported.Errors, 1, "import dryRun=%v", dryRun)
		r.Equal("SEALED_TARGET_CHANGED", imported.Errors[0].Code)
	}

	row, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "cac-target")
	r.NoError(err)
	r.Equal("https://internal.example.com", row.Config["url"])
	r.Equal(blob, *row.ConfigSealed)

	timeout := sealedDoc(org.Slug, "cac-target", nil)
	timeout.Checks[0].Config = map[string]any{"url": "https://internal.example.com", "timeout": "9s"}

	for _, dryRun := range []bool{true, false} {
		applied, applyErr := svc.ApplyChecks(ctx, org.Slug, timeout, checks.ApplyOptions{DryRun: dryRun})
		r.NoError(applyErr)
		r.Empty(applied.Errors, "dryRun=%v", dryRun)
	}

	row, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "cac-target")
	r.NoError(err)
	r.Equal("9s", row.Config["timeout"])
	r.Equal(blob, *row.ConfigSealed)
}

// TestResealListsUnboundBlobs: a browser blob sealed without a binding (before
// spec 2026-11-11-01) is listed on the Re-seal page as "unbound".
func TestResealListsUnboundBlobs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org, keys := setupBrowserSealing(t, sqliteSealingEnv)
	ctx := t.Context()

	legacy := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)
	legacy.Bound = false

	unbound, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("unbound", legacy, sealTestRegion))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("bound",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	listed, err := svc.ListResealChecks(ctx, org.Slug)
	r.NoError(err)
	r.Len(listed, 1)
	r.Equal(unbound.UID, listed[0].UID)
	r.Equal(checks.ResealReasonUnbound, listed[0].Reason)
}
