package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestSetRegionsBulk: the bulk pin pins the listed checks only, to exactly the
// requested regions, and reports what it left alone (spec 2026-11-05-01).
func TestSetRegionsBulk(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	first := w.create(t, checks.CreateCheckRequest{})
	second := w.create(t, checks.CreateCheckRequest{Regions: []string{"gravelines", "paris"}})
	untouched := w.create(t, checks.CreateCheckRequest{})
	heartbeat := w.create(t, checks.CreateCheckRequest{Type: "heartbeat", Config: map[string]any{}})

	missing := "00000000-0000-0000-0000-000000000000"

	resp, err := w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{first.UID, second.UID, heartbeat.UID, missing},
		Regions:   []string{"paris"},
	})
	r.NoError(err)
	r.False(resp.DryRun)
	r.Len(resp.Updated, 2)

	reasons := map[string]string{}
	for _, skip := range resp.Skipped {
		reasons[skip.UID] = skip.Reason
	}

	r.Equal(checks.SetRegionsSkipPassive, reasons[heartbeat.UID])
	r.Equal(checks.SetRegionsSkipNotFound, reasons[missing])

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

func TestSetRegionsBulkRejectsBadRequests(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	_, err := w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{check.UID}, Regions: []string{"atlantis"},
	})
	r.Error(err)
	r.True(checks.IsSetRegionsRequestError(err), "an unknown region is a 400")

	_, err = w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{CheckUIDs: []string{check.UID}})
	r.True(checks.IsSetRegionsRequestError(err), "no region is a 400")

	_, err = w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{Regions: []string{"paris"}})
	r.True(checks.IsSetRegionsRequestError(err), "no check is a 400")

	unchanged, err := w.svc.GetCheck(ctx, w.org.Slug, check.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(check.Regions, unchanged.Regions)
}

func TestSetRegionsBulkDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	resp, err := w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{check.UID}, Regions: []string{"paris"}, DryRun: true,
	})
	r.NoError(err)
	r.True(resp.DryRun)
	r.Len(resp.Updated, 1)

	got, err := w.svc.GetCheck(ctx, w.org.Slug, check.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(models.PlacementAuto, got.Placement)
	r.Equal(check.Regions, got.Regions)
}

func TestSetRegionsBulkRefusesCrossOrgCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	mine := w.create(t, checks.CreateCheckRequest{})

	foreign := models.NewOrganization("foreign-org", "Foreign Org")
	r.NoError(w.db.CreateOrganization(ctx, foreign))

	foreignSvc := w.svc
	_, err := foreignSvc.CreateCheck(ctx, foreign.Slug, checks.CreateCheckRequest{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
	})
	r.NoError(err)

	list, err := foreignSvc.ListChecks(ctx, foreign.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.NotEmpty(list.Data)
	foreignUID := list.Data[0].UID

	resp, err := w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{mine.UID, foreignUID}, Regions: []string{"paris"},
	})
	r.NoError(err)
	r.Len(resp.Updated, 1)
	r.Equal(mine.UID, resp.Updated[0].UID)
	r.Len(resp.Skipped, 1)
	r.Equal(foreignUID, resp.Skipped[0].UID)
	r.Equal(checks.SetRegionsSkipNotFound, resp.Skipped[0].Reason)

	got, err := foreignSvc.GetCheck(ctx, foreign.Slug, foreignUID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotEqual([]string{"paris"}, got.Regions)
}
