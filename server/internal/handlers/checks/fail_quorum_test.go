package checks_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regionquorum"
)

// The failQuorum setting on the API and config-as-code surfaces (spec
// 2026-09-25-10).

func quorumValue(raw string) *regionquorum.Value {
	value := regionquorum.Value(raw)

	return &value
}

// decodeCreate builds a create request from JSON, so the number/string
// decoding of failQuorum is exercised exactly as the handler sees it.
func decodeCreate(t *testing.T, body string) checks.CreateCheckRequest {
	t.Helper()

	var req checks.CreateCheckRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	return req
}

func TestFailQuorumCreateAcceptsEveryForm(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris", "tokyo")

	cases := []struct {
		body          string
		wantWire      string
		wantEffective int
	}{
		{body: `{"slug":"fq-absent"}`, wantWire: "default", wantEffective: 2},
		{body: `{"slug":"fq-default","failQuorum":"default"}`, wantWire: "default", wantEffective: 2},
		{body: `{"slug":"fq-all","failQuorum":"all"}`, wantWire: "all", wantEffective: 3},
		{body: `{"slug":"fq-majority","failQuorum":"MAJORITY"}`, wantWire: "majority", wantEffective: 2},
		{body: `{"slug":"fq-number","failQuorum":1}`, wantWire: "1", wantEffective: 1},
		{body: `{"slug":"fq-string","failQuorum":"1"}`, wantWire: "1", wantEffective: 1},
		// Larger than the region count: accepted, clamped.
		{body: `{"slug":"fq-large","failQuorum":7}`, wantWire: "7", wantEffective: 3},
	}

	for _, tc := range cases {
		req := decodeCreate(t, tc.body)
		req.Name = req.Slug
		req.Regions = []string{"gravelines", "paris", "tokyo"}

		resp := w.create(t, req)
		r.NotNil(resp.FailQuorum, tc.body)
		r.Equal(tc.wantWire, string(*resp.FailQuorum), tc.body)
		r.NotNil(resp.EffectiveFailQuorum, tc.body)
		r.Equal(tc.wantEffective, *resp.EffectiveFailQuorum, tc.body)
	}

	// The wire encodes a count as a JSON number.
	resp, err := w.svc.GetCheck(t.Context(), w.org.Slug, "fq-number", checks.GetCheckOptions{})
	r.NoError(err)

	raw, err := json.Marshal(resp)
	r.NoError(err)

	var wire map[string]any
	r.NoError(json.Unmarshal(raw, &wire))
	r.InDelta(1, wire["failQuorum"], 0)
	r.InDelta(1, wire["effectiveFailQuorum"], 0)
}

func TestFailQuorumRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	for _, raw := range []string{`0`, `-2`, `"half"`, `true`, `2.5`, `101`} {
		req := decodeCreate(t, `{"slug":"bad","failQuorum":`+raw+`}`)
		req.Name = "bad"
		req.Type = "http"
		req.Config = map[string]any{"url": "https://acme.com"}

		_, err := w.svc.CreateCheck(ctx, w.org.Slug, req)
		r.ErrorIs(err, regionquorum.ErrInvalid, raw)

		validation, err := w.svc.ValidateCheck(ctx, w.org.Slug, &checks.ValidateCheckRequest{
			Type: "http", Config: map[string]any{"url": "https://acme.com"}, FailQuorum: req.FailQuorum,
		})
		r.NoError(err)
		r.False(validation.Valid, raw)

		codes := make([]string, 0, len(validation.Fields))
		for _, field := range validation.Fields {
			codes = append(codes, field.Code)
		}

		r.Contains(codes, checks.CodeInvalidFailQuorum, raw)
	}

	// A valid value validates.
	validation, err := w.svc.ValidateCheck(ctx, w.org.Slug, &checks.ValidateCheckRequest{
		Type: "http", Config: map[string]any{"url": "https://acme.com"}, FailQuorum: quorumValue("2"),
	})
	r.NoError(err)
	r.True(validation.Valid, "%+v", validation.Fields)
}

func TestFailQuorumUpdateUpsertAndClone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris", "tokyo")

	created := w.create(t, checks.CreateCheckRequest{
		Name: "api", Slug: "api", Regions: []string{"gravelines", "paris", "tokyo"},
	})
	r.Equal("default", string(*created.FailQuorum))

	updated, err := w.svc.UpdateCheck(ctx, w.org.Slug, "api", &checks.UpdateCheckRequest{
		FailQuorum: quorumValue("all"),
	})
	r.NoError(err)
	r.Equal("all", string(*updated.FailQuorum))
	r.Equal(3, *updated.EffectiveFailQuorum)

	// A PATCH without the field leaves it alone.
	name := "api renamed"
	updated, err = w.svc.UpdateCheck(ctx, w.org.Slug, "api", &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)
	r.Equal("all", string(*updated.FailQuorum))

	// "default" puts it back on the default.
	updated, err = w.svc.UpdateCheck(ctx, w.org.Slug, "api", &checks.UpdateCheckRequest{
		FailQuorum: quorumValue("default"),
	})
	r.NoError(err)
	r.Equal("default", string(*updated.FailQuorum))

	_, err = w.svc.UpdateCheck(ctx, w.org.Slug, "api", &checks.UpdateCheckRequest{FailQuorum: quorumValue("0")})
	r.ErrorIs(err, regionquorum.ErrInvalid)

	// PUT by slug.
	upserted, _, err := w.svc.UpsertCheck(ctx, w.org.Slug, "api", &checks.UpsertCheckRequest{
		Name: "api", Type: "http", Config: map[string]any{"url": "https://acme.com"},
		Regions: []string{"gravelines", "paris", "tokyo"}, FailQuorum: quorumValue("2"),
	})
	r.NoError(err)
	r.Equal("2", string(*upserted.FailQuorum))

	createdByUpsert, wasCreated, err := w.svc.UpsertCheck(ctx, w.org.Slug, "web", &checks.UpsertCheckRequest{
		Name: "web", Type: "http", Config: map[string]any{"url": "https://acme.com/web"},
		Regions: []string{"gravelines", "paris", "tokyo"}, FailQuorum: quorumValue("majority"),
	})
	r.NoError(err)
	r.True(wasCreated)
	r.Equal("majority", string(*createdByUpsert.FailQuorum))

	clone, err := w.svc.CloneCheck(ctx, w.org.Slug, "api", &checks.CloneCheckRequest{})
	r.NoError(err)
	r.Equal("2", string(*clone.FailQuorum), "a clone keeps the quorum")
}

// TestFailQuorumPassiveCheckDropsIt: a heartbeat has no regions and no
// quorum. The value is accepted (a document naming it keeps applying) and
// dropped, and the response carries no quorum at all.
func TestFailQuorumPassiveCheckDropsIt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	resp, err := w.svc.CreateCheck(ctx, w.org.Slug, checks.CreateCheckRequest{
		Name: "beat", Slug: "beat", Type: "heartbeat", Config: map[string]any{}, FailQuorum: quorumValue("2"),
	})
	r.NoError(err)
	r.Nil(resp.FailQuorum)
	r.Nil(resp.EffectiveFailQuorum)

	stored, err := w.db.GetCheck(ctx, w.org.UID, resp.UID)
	r.NoError(err)
	r.Nil(stored.FailQuorum)

	updated, err := w.svc.UpdateCheck(ctx, w.org.Slug, "beat", &checks.UpdateCheckRequest{
		FailQuorum: quorumValue("majority"),
	})
	r.NoError(err)
	r.Nil(updated.FailQuorum)
}

// TestFailQuorumConfigAsCodeRoundTrip: export v2 carries failQuorum (a
// number stays a number), re-applying the export is a no-op, and a document
// that drops the field puts the check back on the default — the dry run says
// so and the apply does it.
func TestFailQuorumConfigAsCodeRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris", "tokyo")

	withQuorum := checks.ExportCheck{
		Name: "api", Slug: "api", Type: "http", Enabled: true,
		Config:     map[string]any{"url": "https://acme.com"},
		Regions:    []string{"gravelines", "paris", "tokyo"},
		FailQuorum: quorumValue("2"),
	}
	plain := checks.ExportCheck{
		Name: "web", Slug: "web", Type: "http", Enabled: true,
		Config:  map[string]any{"url": "https://acme.com/web"},
		Regions: []string{"gravelines", "paris", "tokyo"},
	}

	_, err := w.svc.ApplyChecks(ctx, w.org.Slug, &checks.ExportDocument{
		Version: 1, Organization: "team-a", Checks: []checks.ExportCheck{withQuorum, plain},
	}, checks.ApplyOptions{})
	r.NoError(err)

	exported, err := w.svc.ExportChecks(ctx, w.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	exported.Version = checks.ExportVersionV2
	raw, err := checks.MarshalExportDocument(exported)
	r.NoError(err)

	var wire struct {
		Checks []map[string]any `json:"checks"`
	}
	r.NoError(json.Unmarshal(raw, &wire))

	for _, entry := range wire.Checks {
		switch entry["slug"] {
		case "api":
			r.InDelta(2, entry["failQuorum"], 0, "a count exports as a number")
		case "web":
			r.NotContains(entry, "failQuorum", "the default is absent")
		}
	}

	parsed, err := checks.ParseManifest(raw, "application/json")
	r.NoError(err)

	parsed.Organization = "team-a"
	res, err := w.svc.ApplyChecks(ctx, w.org.Slug, parsed, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(2, res.Unchanged, "%+v", res.Plan)

	// Drop the field: back to the default.
	dropped := withQuorum
	dropped.FailQuorum = nil
	document := &checks.ExportDocument{Version: 1, Organization: "team-a", Checks: []checks.ExportCheck{dropped}}

	dry, err := w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(checks.CheckFieldChange{Field: "failQuorum", From: "2", To: "default"},
		changeFields(planEntry(t, dry, "api"))["failQuorum"])

	_, err = w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{})
	r.NoError(err)

	stored, err := w.svc.GetCheck(ctx, w.org.Slug, "api", checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal("default", string(*stored.FailQuorum), "the apply did what the dry run announced")

	again, err := w.svc.ApplyChecks(ctx, w.org.Slug, document, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(checks.ApplyActionUnchanged, planEntry(t, again, "api").Action, "the plan converges")
}

func TestDocumentValidatorRejectsInvalidFailQuorum(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	issues := checks.ValidateDocument(&checks.ExportDocument{
		Version: 1, Organization: "team-a",
		Checks: []checks.ExportCheck{{
			Name: "api", Slug: "api", Type: "http", Enabled: true,
			Config: map[string]any{"url": "https://acme.com"}, FailQuorum: quorumValue("half"),
		}},
	})

	found := false

	for _, issue := range issues {
		if issue.Code == checks.CodeInvalidFailQuorum {
			found = true

			r.Equal("failQuorum", issue.Field)
		}
	}

	r.True(found, "%+v", issues)
}

// TestCheckDetailRegionalIssue: the detail's per-region list carries each
// region's newest reading, and the regionalIssue block appears while fewer
// than the quorum of CURRENT regions are failing — evaluated the same way the
// incident engine does (a left-behind region never counts).
func TestCheckDetailRegionalIssue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris", "tokyo")

	created := w.create(t, checks.CreateCheckRequest{
		Name: "api", Slug: "api", Regions: []string{"gravelines", "paris", "tokyo"},
	})

	now := time.Now().UTC()
	reading := func(region string, status models.ResultStatus) {
		result := models.NewResult(w.org.UID, created.UID, status, 0)
		result.Region = &region
		result.PeriodStart = now
		r.NoError(w.db.CreateResult(ctx, result))
		r.NoError(w.db.UpsertCheckRegionState(ctx, &models.CheckRegionState{
			CheckUID: created.UID, Region: region, OrganizationUID: w.org.UID, Status: status,
			StatusSince: now.Add(-time.Minute), LastResultAt: now, UpdatedAt: now,
		}))
	}

	reading("gravelines", models.ResultStatusUp)
	reading("paris", models.ResultStatusUp)
	reading("tokyo", models.ResultStatusTimeout)
	// A region the check does not run in: inert.
	reading("lauterbourg", models.ResultStatusDown)

	detail, err := w.svc.GetCheck(ctx, w.org.Slug, "api", checks.GetCheckOptions{IncludeRegionFreshness: true})
	r.NoError(err)
	r.NotNil(detail.RegionalIssue)
	r.Equal([]string{"tokyo"}, detail.RegionalIssue.FailingRegions)
	r.Equal(2, detail.RegionalIssue.FailQuorum)
	r.Equal(3, detail.RegionalIssue.RegionCount)

	statuses := map[string]string{}
	for _, region := range detail.RegionFreshness {
		if region.Status != nil {
			statuses[region.Region] = *region.Status
			r.NotNil(region.StatusSince)
		}
	}

	r.Equal("timeout", statuses["tokyo"])
	r.Equal("up", statuses["paris"])

	// Without the embed, no extra block.
	plain, err := w.svc.GetCheck(ctx, w.org.Slug, "api", checks.GetCheckOptions{})
	r.NoError(err)
	r.Nil(plain.RegionalIssue)

	// With "all" there is no regional issue: the per-result machine applies.
	_, err = w.svc.UpdateCheck(ctx, w.org.Slug, "api", &checks.UpdateCheckRequest{FailQuorum: quorumValue("all")})
	r.NoError(err)

	detail, err = w.svc.GetCheck(ctx, w.org.Slug, "api", checks.GetCheckOptions{IncludeRegionFreshness: true})
	r.NoError(err)
	r.Nil(detail.RegionalIssue)
}
