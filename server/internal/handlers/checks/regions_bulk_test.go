package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
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

func TestSetRegionsBulkUnknownOrganization(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	_, err := w.svc.SetRegions(t.Context(), "ghost-org", &checks.SetRegionsRequest{
		CheckUIDs: []string{"x"}, Regions: []string{"paris"},
	})
	r.ErrorIs(err, checks.ErrOrganizationNotFound)
}

func TestSetRegionsBulkRejectsForeignPrivateRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	_, err := w.svc.SetRegions(t.Context(), w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{check.UID}, Regions: []string{"@evilcorp/aws-paris"},
	})
	r.Error(err)
	r.True(checks.IsSetRegionsRequestError(err), "another org's private location is a 400")
}

func TestSetRegionsBulkPrivateRegionOfTheOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	resp, err := w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{check.UID}, Regions: []string{"@" + w.org.Slug + "/office"},
	})
	r.NoError(err)
	r.Len(resp.Updated, 1)
	r.Equal([]string{"@office"}, resp.Updated[0].Regions)
}

func TestSetRegionsBulkSkipsChecksDemoCannotWrite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	resp, err := w.svc.SetRegions(demoCtx("visitor-1"), w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{check.UID}, Regions: []string{"paris"},
	})
	r.NoError(err)
	r.Empty(resp.Updated)
	r.Len(resp.Skipped, 1)
	r.Equal(checks.SetRegionsSkipReadOnly, resp.Skipped[0].Reason)

	got, err := w.svc.GetCheck(t.Context(), w.org.Slug, check.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(check.Regions, got.Regions)
}

// TestSetRegionsBulkDedupesWarnings: two browser checks pinned to a region
// without headless Chrome raise the same advisory, reported once.
func TestSetRegionsBulkDedupesWarnings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "tokyo", "gravelines")
	cfg := map[string]any{"url": "https://acme.com"}
	first := w.create(t, checks.CreateCheckRequest{Type: "browser", Config: cfg})
	second := w.create(t, checks.CreateCheckRequest{Type: "browser", Config: cfg})

	resp, err := w.svc.SetRegions(t.Context(), w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{first.UID, second.UID}, Regions: []string{"tokyo"},
	})
	r.NoError(err)
	r.Len(resp.Updated, 2)
	r.Len(resp.Warnings, 1)
}

// TestSetRegionsBulkRejectsAGatedCheck: a check the type gates refuse at the
// requested regions is skipped as rejected, on a real run and on a dry run.
func TestSetRegionsBulkRejectsAGatedCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	docker := w.create(t, checks.CreateCheckRequest{
		Type: "docker", Config: map[string]any{"containerName": "postgres"},
	})
	plain := w.create(t, checks.CreateCheckRequest{})

	w.svc.SetDeploymentMode(config.DeploymentModeSaaS)

	for _, dryRun := range []bool{false, true} {
		resp, err := w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
			CheckUIDs: []string{docker.UID, plain.UID}, Regions: []string{"paris"}, DryRun: dryRun,
		})
		r.NoError(err)
		r.Len(resp.Updated, 1, "dryRun=%v", dryRun)
		r.Equal(plain.UID, resp.Updated[0].UID)
		r.Len(resp.Skipped, 1)
		r.Equal(docker.UID, resp.Skipped[0].UID)
		r.Equal(checks.SetRegionsSkipRejected, resp.Skipped[0].Reason)
		r.NotEmpty(resp.Skipped[0].Message)
	}

	got, err := w.svc.GetCheck(ctx, w.org.Slug, docker.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(docker.Regions, got.Regions, "a rejected check is left untouched")
}

func TestSetRegionsBulkReportsStorageFailures(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})
	req := &checks.SetRegionsRequest{CheckUIDs: []string{check.UID}, Regions: []string{"paris"}}

	r.NoError(w.db.SetSystemParameter(ctx, regions.ParamRegions, "not-a-list", false))

	_, err := w.svc.SetRegions(ctx, w.org.Slug, req)
	r.Error(err)
	r.False(checks.IsSetRegionsRequestError(err), "a storage failure is a 500, not a 400")
}

func TestSetRegionsBulkReportsListFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	_, err := w.db.DB().ExecContext(ctx, "DROP TABLE checks")
	r.NoError(err)

	_, err = w.svc.SetRegions(ctx, w.org.Slug, &checks.SetRegionsRequest{
		CheckUIDs: []string{check.UID}, Regions: []string{"paris"},
	})
	r.Error(err)
	r.False(checks.IsSetRegionsRequestError(err))
}
