package checks_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

const missingCheckUID = "00000000-0000-0000-0000-000000000000"

func bulkRequest(uids []string, changes string, dryRun bool) *checks.BulkUpdateChecksRequest {
	return &checks.BulkUpdateChecksRequest{CheckUIDs: uids, Changes: json.RawMessage(changes), DryRun: dryRun}
}

func skipReasons(resp *checks.BulkUpdateChecksResponse) map[string]string {
	reasons := map[string]string{}
	for _, skip := range resp.Skipped {
		reasons[skip.UID] = skip.Reason
	}

	return reasons
}

// TestBulkUpdatePinsRegions: the bulk update pins the listed checks only, to
// exactly the requested regions, and reports what it left alone (spec
// 2026-11-05-01).
func TestBulkUpdatePinsRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	first := w.create(t, checks.CreateCheckRequest{})
	second := w.create(t, checks.CreateCheckRequest{Regions: []string{"gravelines", "paris"}})
	untouched := w.create(t, checks.CreateCheckRequest{})
	heartbeat := w.create(t, checks.CreateCheckRequest{Type: "heartbeat", Config: map[string]any{}})

	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{first.UID, second.UID, heartbeat.UID, missingCheckUID}, `{"regions": ["paris"]}`, false))
	r.NoError(err)
	r.False(resp.DryRun)
	r.Len(resp.Data, 2)

	reasons := skipReasons(resp)
	r.Equal(checks.BulkUpdateSkipRejected, reasons[heartbeat.UID], "a passive check has no regions")
	r.Equal(checks.BulkUpdateSkipNotFound, reasons[missingCheckUID])

	for _, uid := range []string{first.UID, second.UID} {
		got, getErr := w.svc.GetCheck(ctx, w.org.Slug, uid, checks.GetCheckOptions{})
		r.NoError(getErr)
		r.Equal(models.PlacementPinned, got.Placement)
		r.Equal([]string{"paris"}, got.Regions)
		r.Equal([]string{"paris"}, w.jobRegions(t, uid), "jobs follow the new regions")
	}

	other, err := w.svc.GetCheck(ctx, w.org.Slug, untouched.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(untouched.Regions, other.Regions, "a check that was not listed is untouched")
	r.Equal(models.PlacementAuto, other.Placement)
}

// TestBulkUpdateDisablesPassiveChecksToo: the passive skip is about placement
// fields only; a passive check can still be disabled in bulk.
func TestBulkUpdateDisablesPassiveChecksToo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	active := w.create(t, checks.CreateCheckRequest{})
	heartbeat := w.create(t, checks.CreateCheckRequest{Type: "heartbeat", Config: map[string]any{}})

	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{active.UID, heartbeat.UID}, `{"enabled": false, "period": "00:05:00"}`, false))
	r.NoError(err)
	r.Empty(resp.Skipped)
	r.Len(resp.Data, 2)

	for _, uid := range []string{active.UID, heartbeat.UID} {
		got, getErr := w.svc.GetCheck(ctx, w.org.Slug, uid, checks.GetCheckOptions{})
		r.NoError(getErr)
		r.False(*got.Enabled)
		r.Equal("00:05:00", *got.Period)
	}
}

// TestBulkUpdateSwitchesToAutoPlacement: {"placement": "auto"} replaces the
// deprecated POST /checks/auto-placement. A check keeps its region count, and
// its regions while they are healthy; a private location stays pinned.
func TestBulkUpdateSwitchesToAutoPlacement(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	pinned := w.create(t, checks.CreateCheckRequest{Regions: []string{"gravelines", "paris"}})
	private := w.create(t, checks.CreateCheckRequest{Regions: []string{"@office"}})

	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{pinned.UID, private.UID}, `{"placement": "auto"}`, false))
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(pinned.UID, resp.Data[0].UID)
	r.Equal(checks.BulkUpdateSkipRejected, skipReasons(resp)[private.UID])

	switched, err := w.svc.GetCheck(ctx, w.org.Slug, pinned.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(models.PlacementAuto, switched.Placement)
	r.ElementsMatch([]string{"gravelines", "paris"}, switched.Regions, "healthy regions are kept")
	r.Equal(2, *switched.RegionCount, "same cost")

	stillPinned, err := w.svc.GetCheck(ctx, w.org.Slug, private.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(models.PlacementPinned, stillPinned.Placement)
}

func TestBulkUpdateRejectsBadRequests(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	tooMany := make([]string, checks.MaxBulkUpdateChecks+1)
	for idx := range tooMany {
		tooMany[idx] = strings.Repeat("a", 8) + string(rune('a'+idx%26)) + strings.Repeat("b", idx/26)
	}

	cases := []struct {
		name string
		req  *checks.BulkUpdateChecksRequest
	}{
		{"no check", bulkRequest(nil, `{"enabled": false}`, false)},
		{"only empty uids", bulkRequest([]string{""}, `{"enabled": false}`, false)},
		{"too many checks", bulkRequest(tooMany, `{"enabled": false}`, false)},
		{"no changes", bulkRequest([]string{check.UID}, `{}`, false)},
		{"changes missing", bulkRequest([]string{check.UID}, ``, false)},
		{"changes not an object", bulkRequest([]string{check.UID}, `[1]`, false)},
		{"unsupported field", bulkRequest([]string{check.UID}, `{"enabled": false, "name": "x"}`, false)},
		{"org reference", bulkRequest([]string{check.UID}, `{"checkGroupUid": "x"}`, false)},
		{"wrong type", bulkRequest([]string{check.UID}, `{"enabled": "no"}`, false)},
		{"unknown region", bulkRequest([]string{check.UID}, `{"regions": ["atlantis"]}`, false)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			_, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, tc.req)
			r.Error(err)
			r.True(checks.IsBulkUpdateRequestError(err), "a 400, got %v", err)
		})
	}

	unchanged, err := w.svc.GetCheck(ctx, w.org.Slug, check.UID, checks.GetCheckOptions{})
	require.NoError(t, err)
	require.True(t, *unchanged.Enabled)
}

// TestBulkUpdateRejectsPerCheck: a value one check cannot take is reported on
// that check, with the PATCH's validation message, and the others are updated.
func TestBulkUpdateRejectsPerCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{check.UID}, `{"confirmationPeriodSeconds": -5}`, false))
	r.NoError(err)
	r.Empty(resp.Data)
	r.Len(resp.Skipped, 1)
	r.Equal(checks.BulkUpdateSkipRejected, resp.Skipped[0].Reason)
	r.Contains(resp.Skipped[0].Message, "confirmationPeriodSeconds")
}

func TestBulkUpdateDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{check.UID, check.UID}, `{"regions": ["paris"], "enabled": false}`, true))
	r.NoError(err)
	r.True(resp.DryRun)
	r.Len(resp.Data, 1, "a repeated uid counts once")

	got, err := w.svc.GetCheck(ctx, w.org.Slug, check.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(models.PlacementAuto, got.Placement)
	r.Equal(check.Regions, got.Regions)
	r.True(*got.Enabled)
}

func TestBulkUpdateRefusesCrossOrgCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	mine := w.create(t, checks.CreateCheckRequest{})

	foreign := models.NewOrganization("foreign-org", "Foreign Org")
	r.NoError(w.db.CreateOrganization(ctx, foreign))

	foreignCheck, err := w.svc.CreateCheck(ctx, foreign.Slug, checks.CreateCheckRequest{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
	})
	r.NoError(err)

	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{mine.UID, foreignCheck.UID}, `{"enabled": false}`, false))
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(mine.UID, resp.Data[0].UID)
	r.Equal(checks.BulkUpdateSkipNotFound, skipReasons(resp)[foreignCheck.UID])

	got, err := w.svc.GetCheck(ctx, foreign.Slug, foreignCheck.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.True(*got.Enabled, "another org's check is never written")
}
