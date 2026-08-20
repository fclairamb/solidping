package checks_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// intp is a small pointer helper for building requests in these tests.
func intp(v int) *int { return &v }

// boolp is a small pointer helper for building requests in these tests.
func boolp(v bool) *bool { return &v }

// makeCleanOrg creates and returns a second organization in the same DB so a
// round-trip can import into a fresh scope.
func makeCleanOrg(ctx context.Context, t *testing.T, dbSvc db.Service, slug string) *models.Organization {
	t.Helper()
	r := require.New(t)
	org := models.NewOrganization(slug, slug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return org
}

// normalizeForCompare blanks the volatile top-level fields (timestamp and org
// name) so two exports from different orgs can be compared byte-for-byte on
// their defaults + checks content.
func normalizeForCompare(doc *checks.ExportDocument) {
	doc.ExportedAt = "FIXED"
	doc.Organization = "FIXED"
}

// TestExportV2RoundTripByteIdentical proves ordering determinism + defaults
// stability: export v2 → import into a clean org → re-export → byte-identical.
func TestExportV2RoundTripByteIdentical(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, srcOrg := setupApplyService(t, true)
	ctx := t.Context()

	// Two "common" checks (all defaults) and one deviating check (dnsbl-like).
	mk := func(slug string) checks.CreateCheckRequest {
		return checks.CreateCheckRequest{
			Name: slug, Slug: slug, Type: "http",
			Config:  map[string]any{"url": "https://example.com/" + slug},
			Regions: []string{"default"},
		}
	}
	_, err := svc.CreateCheck(ctx, srcOrg.Slug, mk("alpha"))
	r.NoError(err)
	_, err = svc.CreateCheck(ctx, srcOrg.Slug, mk("bravo"))
	r.NoError(err)

	deviating := mk("dnsbl")
	deviating.Regions = []string{"default", "eu-2"}
	deviating.Period = strPtr("1h")
	deviating.FlapBackoffFactor = intp(5)
	deviating.MaxRecoveryMultiplier = intp(10)
	deviating.ReopenCooldownMultiplier = intp(5)
	deviating.Enabled = boolp(false)
	_, err = svc.CreateCheck(ctx, srcOrg.Slug, deviating)
	r.NoError(err)

	cleanOrg := makeCleanOrg(ctx, t, dbSvc, "clean-org")

	// First export.
	export1, err := svc.ExportChecks(ctx, srcOrg.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Equal(2, export1.Version)
	r.Equal("stripped", export1.Secrets)
	normalizeForCompare(export1)
	b1, err := checks.MarshalExportDocument(export1)
	r.NoError(err)

	// Import the exported document into the clean org.
	var reimport checks.ExportDocument
	r.NoError(json.Unmarshal(b1, &reimport))
	result, err := svc.ImportChecks(ctx, cleanOrg.Slug, &reimport, false)
	r.NoError(err)
	r.Empty(result.Errors)
	r.Equal(3, result.Created)

	// Re-export from the clean org and compare bytes.
	export3, err := svc.ExportChecks(ctx, cleanOrg.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	normalizeForCompare(export3)
	b3, err := checks.MarshalExportDocument(export3)
	r.NoError(err)

	r.Equal(string(b1), string(b3), "round-tripped export must be byte-identical")

	// Sanity: the deviating check kept its overrides through the round trip.
	imported, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "dnsbl")
	r.NoError(err)
	r.Equal(5, imported.FlapBackoffFactor)
	r.Equal(10, imported.MaxRecoveryMultiplier)
	r.NotNil(imported.ReopenCooldownMultiplier)
	r.Equal(5, *imported.ReopenCooldownMultiplier)
	r.False(imported.Enabled)
	r.Equal([]string{"default", "eu-2"}, imported.Regions)
}

// TestExportV2IsPrettyPrinted verifies the emitted document is indented and
// carries the v2 markers.
func TestExportV2IsPrettyPrinted(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, org := setupApplyService(t, true)
	ctx := t.Context()

	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "web", Slug: "web", Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	doc, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	body, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	s := string(body)
	r.Contains(s, "\n  \"version\": 2")
	r.Contains(s, "\"secrets\": \"stripped\"")
	r.Contains(s, "\"defaults\": {")
	// enabled is inverted away; the common check has no disabled marker.
	r.NotContains(s, "\"enabled\"")
	r.NotContains(s, "_secretsStripped")
}

// TestImportV1StillWorks verifies a legacy v1 document imports: HH:MM:SS period,
// explicit enabled, and the _secretsStripped meta flag.
func TestImportV1StillWorks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	body := []byte(`{
      "version": 1,
      "organization": "apply-org",
      "_secretsStripped": true,
      "checks": [
        {"name": "Legacy", "slug": "legacy", "type": "http",
         "config": {"url": "https://legacy.example.com"},
         "enabled": false, "period": "00:05:00",
         "confirmationPeriodSeconds": 60}
      ]
    }`)

	var doc checks.ExportDocument
	r.NoError(json.Unmarshal(body, &doc))
	r.Equal(1, doc.Version)
	r.Equal("stripped", doc.Secrets)

	result, err := svc.ImportChecks(ctx, org.Slug, &doc, false)
	r.NoError(err)
	r.Empty(result.Errors)
	r.Equal(1, result.Created)

	got, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "legacy")
	r.NoError(err)
	r.False(got.Enabled)
	r.Equal(60, got.ConfirmationPeriodSeconds)
	// HH:MM:SS period parsed to 5 minutes.
	periodVal, _ := got.Period.Value()
	r.Equal("00:05:00", periodVal)
}

// TestImportDisabledSemantics covers: v2 absent → enabled, v2 disabled:true →
// disabled, v1 enabled:false → disabled.
func TestImportDisabledSemantics(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	body := []byte(`{
      "version": 2,
      "checks": [
        {"name": "on", "slug": "check-on", "type": "http", "config": {"url": "https://on"}},
        {"name": "off", "slug": "check-off", "type": "http", "config": {"url": "https://off"}, "disabled": true}
      ]
    }`)

	var doc checks.ExportDocument
	r.NoError(json.Unmarshal(body, &doc))
	result, err := svc.ImportChecks(ctx, org.Slug, &doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	on, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "check-on")
	r.NoError(err)
	r.True(on.Enabled, "absent disabled → enabled")

	off, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "check-off")
	r.NoError(err)
	r.False(off.Enabled, "disabled:true → disabled")
}

// TestImportV2DefaultsResolution verifies the resolution order check value →
// document default → system default, and that a v2 doc without a defaults block
// is valid.
func TestImportV2DefaultsResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	body := []byte(`{
      "version": 2,
      "defaults": {
        "flapBackoffFactor": 3,
        "confirmationPeriod": "30s"
      },
      "checks": [
        {"name": "inherits", "slug": "inherits", "type": "http", "config": {"url": "https://a"}},
        {"name": "overrides", "slug": "overrides", "type": "http", "config": {"url": "https://b"},
         "flapBackoffFactor": 5}
      ]
    }`)

	var doc checks.ExportDocument
	r.NoError(json.Unmarshal(body, &doc))
	result, err := svc.ImportChecks(ctx, org.Slug, &doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	inherits, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "inherits")
	r.NoError(err)
	// Document default beats the system default (2).
	r.Equal(3, inherits.FlapBackoffFactor)
	r.Equal(30, inherits.ConfirmationPeriodSeconds)
	// Absent from both check and defaults → system default (8).
	r.Equal(8, inherits.MaxRecoveryMultiplier)

	overrides, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "overrides")
	r.NoError(err)
	// Check value beats the document default.
	r.Equal(5, overrides.FlapBackoffFactor)
	// Inherits the document default confirmationPeriod.
	r.Equal(30, overrides.ConfirmationPeriodSeconds)
}

// TestImportV2WithoutDefaults verifies a v2 document with no defaults block
// imports, falling back to system defaults for every unspecified field.
func TestImportV2WithoutDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	body := []byte(`{
      "version": 2,
      "checks": [
        {"name": "plain", "slug": "plain", "type": "http", "config": {"url": "https://plain"}}
      ]
    }`)

	var doc checks.ExportDocument
	r.NoError(json.Unmarshal(body, &doc))
	result, err := svc.ImportChecks(ctx, org.Slug, &doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	got, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "plain")
	r.NoError(err)
	r.Equal(2, got.FlapBackoffFactor)
	r.Equal(8, got.MaxRecoveryMultiplier)
	r.Equal(120, got.ConfirmationPeriodSeconds)
}

// TestExportV2DeterministicOrdering verifies checks are emitted sorted by group
// (empty group last), then slug.
func TestExportV2DeterministicOrdering(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	group := models.NewCheckGroup(org.UID, "infra", "infra")
	r.NoError(dbSvc.CreateCheckGroup(ctx, group))

	mk := func(slug string, groupUID *string) {
		_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
			Name: slug, Slug: slug, Type: "http",
			Config:        map[string]any{"url": "https://example.com/" + slug},
			CheckGroupUID: groupUID,
		})
		r.NoError(err)
	}
	// Intentionally created out of final order.
	mk("zebra", nil)
	mk("mango", &group.UID)
	mk("beta", nil)
	mk("apple", &group.UID)

	doc, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	slugs := make([]string, len(doc.Checks))
	for i := range doc.Checks {
		slugs[i] = doc.Checks[i].Slug
	}
	// Grouped ("infra") first sorted by slug, then ungrouped sorted by slug.
	r.Equal([]string{"apple", "mango", "beta", "zebra"}, slugs)
}

// TestImportV2ChildBeforeParentOrdering verifies the two-pass importer accepts a
// document where a child check precedes the parent it depends on.
func TestImportV2ChildBeforeParentOrdering(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	body := []byte(`{
      "version": 2,
      "checks": [
        {"name": "child", "slug": "child", "type": "http", "config": {"url": "https://child"},
         "dependsOn": [{"parentSlug": "parent", "kind": "hard"}]},
        {"name": "parent", "slug": "parent", "type": "http", "config": {"url": "https://parent"}}
      ]
    }`)

	var doc checks.ExportDocument
	r.NoError(json.Unmarshal(body, &doc))
	result, err := svc.ImportChecks(ctx, org.Slug, &doc, false)
	r.NoError(err)
	r.Empty(result.Errors, "child-before-parent must import cleanly")
	r.Equal(2, result.Created)

	child, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "child")
	r.NoError(err)
	parents, err := dbSvc.ListCheckDependencyParents(ctx, child.UID)
	r.NoError(err)
	r.Len(parents, 1)
}

// TestExportV2CarriesCaptureFailureResponse pins the config-as-code half of
// spec 2026-08-20-01: `capture_failure_response` is an ordinary check-config
// key, so it must survive export -> import unchanged (and, being off by
// default, must be absent from a check that never enabled it).
func TestExportV2CarriesCaptureFailureResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, srcOrg := setupApplyService(t, true)
	ctx := t.Context()

	_, err := svc.CreateCheck(ctx, srcOrg.Slug, checks.CreateCheckRequest{
		Name: "captured", Slug: "captured", Type: "http",
		Config: map[string]any{
			"url":                      "https://acme.com/health",
			"capture_failure_response": true,
		},
		Regions: []string{"default"},
	})
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, srcOrg.Slug, checks.CreateCheckRequest{
		Name: "plain", Slug: "plain", Type: "http",
		Config:  map[string]any{"url": "https://acme.com/other"},
		Regions: []string{"default"},
	})
	r.NoError(err)

	export, err := svc.ExportChecks(ctx, srcOrg.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	raw, err := checks.MarshalExportDocument(export)
	r.NoError(err)
	// Positive control: the document really carries both checks.
	r.Contains(string(raw), "https://acme.com/health")
	r.Contains(string(raw), "capture_failure_response")

	cleanOrg := makeCleanOrg(ctx, t, dbSvc, "capture-clean-org")

	var reimport checks.ExportDocument

	r.NoError(json.Unmarshal(raw, &reimport))

	result, err := svc.ImportChecks(ctx, cleanOrg.Slug, &reimport, false)
	r.NoError(err)
	r.Empty(result.Errors)

	captured, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "captured")
	r.NoError(err)
	r.Equal(true, captured.Config["capture_failure_response"])

	plain, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "plain")
	r.NoError(err)
	r.NotContains(plain.Config, "capture_failure_response")
}
