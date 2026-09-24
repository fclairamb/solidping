package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// Config-as-code carries the placement (spec 2026-09-25-06 A4): export v2 and
// the plan diff know the fields, the dry run and the real upsert agree, and an
// empty regions list with placement auto is meaningful.

func changeFields(entry checks.ApplyPlanEntry) map[string]checks.CheckFieldChange {
	out := make(map[string]checks.CheckFieldChange, len(entry.Changes))
	for _, change := range entry.Changes {
		out[change.Field] = change
	}

	return out
}

func planEntry(t *testing.T, result *checks.ApplyResult, slug string) checks.ApplyPlanEntry {
	t.Helper()

	for _, entry := range result.Plan {
		if entry.Slug == slug {
			return entry
		}
	}

	t.Fatalf("no plan entry for %s", slug)

	return checks.ApplyPlanEntry{}
}

// TestPlacementRoundTripsThroughExportV2: an automatic check exports as
// placement/regionCount/regionPool with no regions (and the document default
// regions never apply to it); re-applying the export is a no-op.
func TestPlacementRoundTripsThroughExportV2(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "lauterbourg", "paris")

	two := 2
	_, err := w.svc.ApplyChecks(ctx, w.org.Slug, &checks.ExportDocument{
		Version: 1, Organization: "team-a",
		Checks: []checks.ExportCheck{
			{
				Name: "auto", Slug: "auto", Type: "http", Enabled: true,
				Config:    map[string]any{"url": "https://acme.com/auto"},
				Placement: models.PlacementAuto, RegionCount: &two, RegionPool: []string{"lauterbourg", "paris"},
			},
			{
				Name: "pinned", Slug: "pinned", Type: "http", Enabled: true,
				Config:  map[string]any{"url": "https://acme.com/pinned"},
				Regions: []string{"gravelines"},
			},
		},
	}, checks.ApplyOptions{})
	r.NoError(err)

	exported, err := w.svc.ExportChecks(ctx, w.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	exported.Version = checks.ExportVersionV2
	raw, err := checks.MarshalExportDocument(exported)
	r.NoError(err)

	var wire struct {
		Defaults struct {
			Regions []string `json:"regions"`
		} `json:"defaults"`
		Checks []map[string]any `json:"checks"`
	}
	r.NoError(json.Unmarshal(raw, &wire))
	r.Equal([]string{"gravelines"}, wire.Defaults.Regions, "the modal default comes from pinned checks only")

	for _, entry := range wire.Checks {
		if entry["slug"] != "auto" {
			continue
		}

		r.Equal("auto", entry["placement"])
		r.InDelta(2, entry["regionCount"], 0)
		r.Equal([]any{"lauterbourg", "paris"}, entry["regionPool"])
		r.NotContains(entry, "regions", "an auto check's regions are the scheduler's")
	}

	parsed, err := checks.ParseManifest(raw, "application/json")
	r.NoError(err)

	for i := range parsed.Checks {
		if parsed.Checks[i].Slug == "auto" {
			r.Empty(parsed.Checks[i].Regions, "the document default regions never apply to an auto check")
			r.Equal(models.PlacementAuto, parsed.Checks[i].Placement)
		}
	}

	parsed.Organization = "team-a"
	res, err := w.svc.ApplyChecks(ctx, w.org.Slug, parsed, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(2, res.Unchanged, "%+v", res.Plan)
	r.Empty(res.Errors)
}

// TestPlacementAutoWithNoRegionsSwitchesAPinnedCheck: a document naming
// `placement: auto` and no regions switches a pinned check (an empty regions
// list is not "no change" here), the dry run reports exactly what the apply
// then does, and the next plan converges.
func TestPlacementAutoWithNoRegionsSwitchesAPinnedCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	pinned := checks.ExportCheck{
		Name: "api", Slug: "api", Type: "http", Enabled: true,
		Config: map[string]any{"url": "https://acme.com"}, Regions: []string{"paris"},
	}
	_, err := w.svc.ApplyChecks(ctx, w.org.Slug,
		&checks.ExportDocument{Version: 1, Organization: "team-a", Checks: []checks.ExportCheck{pinned}},
		checks.ApplyOptions{})
	r.NoError(err)

	auto := pinned
	auto.Regions = nil
	auto.Placement = models.PlacementAuto
	document := &checks.ExportDocument{Version: 1, Organization: "team-a", Checks: []checks.ExportCheck{auto}}

	dry, err := w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{DryRun: true})
	r.NoError(err)

	entry := planEntry(t, dry, "api")
	r.Equal(checks.ApplyActionUpdate, entry.Action)

	fields := changeFields(entry)
	r.Equal(checks.CheckFieldChange{Field: "placement", From: "pinned", To: "auto"}, fields["placement"])
	r.Equal(checks.CheckFieldChange{Field: "regionCount", From: "", To: "1"}, fields["regionCount"],
		"switching keeps the current region count")

	_, err = w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{})
	r.NoError(err)

	stored, err := w.svc.GetCheck(ctx, w.org.Slug, "api", checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(models.PlacementAuto, stored.Placement)
	r.Equal(1, *stored.RegionCount, "the apply stored what the dry run announced")
	r.Equal([]string{"paris"}, stored.Regions, "the current, healthy region is kept")

	again, err := w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(checks.ApplyActionUnchanged, planEntry(t, again, "api").Action, "the plan converges")

	// And back: a document listing regions pins it again.
	back, err := w.svc.ApplyChecks(ctx, w.org.Slug,
		&checks.ExportDocument{Version: 1, Organization: "team-a", Checks: []checks.ExportCheck{pinned}},
		checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(checks.CheckFieldChange{Field: "placement", From: "auto", To: "pinned"},
		changeFields(planEntry(t, back, "api"))["placement"])
}

// TestPlacementRegionCountDiffConverges: a regionCount the write caps (only
// two eligible regions) diffs against what will actually be stored, so the
// plan does not report the same change forever.
func TestPlacementRegionCountDiffConverges(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	five := 5
	check := checks.ExportCheck{
		Name: "api", Slug: "api", Type: "http", Enabled: true,
		Config:    map[string]any{"url": "https://acme.com"},
		Placement: models.PlacementAuto, RegionCount: &five, RegionPool: []string{"gravelines", "paris"},
	}
	document := &checks.ExportDocument{Version: 1, Organization: "team-a", Checks: []checks.ExportCheck{check}}

	_, err := w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{})
	r.NoError(err)

	again, err := w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(checks.ApplyActionUnchanged, planEntry(t, again, "api").Action, "%+v", again.Plan)
}

// TestDocumentValidatorRejectsPlacementContradictions: the offline validator
// holds the placement fields to the same rules as the write path.
func TestDocumentValidatorRejectsPlacementContradictions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	document := &checks.ExportDocument{
		Version: 2, Organization: "acme",
		Checks: []checks.ExportCheck{{
			Name: "api", Slug: "api", Type: "http", Enabled: true,
			Config:    map[string]any{"url": "https://acme.com"},
			Placement: models.PlacementAuto, Regions: []string{"paris"},
		}},
	}

	issues := checks.ValidateDocument(document)

	found := false

	for _, issue := range issues {
		if issue.Code == checks.CodeInvalidPlacement {
			found = true
		}
	}

	r.True(found, "%+v", issues)

	// Positive control: the same check without the contradiction is clean.
	document.Checks[0].Regions = nil
	for _, issue := range checks.ValidateDocument(document) {
		r.NotEqual(checks.CodeInvalidPlacement, issue.Code, "%+v", issue)
	}
}
