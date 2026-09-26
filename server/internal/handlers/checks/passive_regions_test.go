package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Spec 2026-09-25-04 §1: a passive check (heartbeat, email) has no regions.
// An explicit list is accepted and DROPPED on every write path — the REST API
// and MCP (both CreateCheck/UpdateCheck) and config-as-code (ApplyChecks) —
// and the check always owns exactly one NULL-region job.

func requireOneNullRegionJob(t *testing.T, jobs []*models.CheckJob) {
	t.Helper()

	require.Len(t, jobs, 1, "a passive check owns exactly one job")
	require.Nil(t, jobs[0].Region, "and it has no region")
}

func TestPassiveCheckRegionsAreDroppedOnCreateAndUpdate(t *testing.T) {
	t.Parallel()

	for _, checkType := range []string{"heartbeat", "email"} {
		t.Run(checkType, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbSvc, org := newReconcileTestService(t)
			ctx := t.Context()

			created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
				Name:    "passive " + checkType,
				Slug:    "passive-" + checkType,
				Type:    checkType,
				Regions: []string{"gravelines"},
			})
			r.NoError(err, "an explicit region list is accepted, not rejected")
			r.Empty(created.Regions, "the response shows the empty list")

			stored, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
			r.NoError(err)
			r.Empty(stored.Regions, "the row stores the empty list")

			jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, created.UID)
			r.NoError(err)
			requireOneNullRegionJob(t, jobs)

			regions := []string{"gravelines", "roubaix"}
			updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Regions: &regions})
			r.NoError(err)
			r.Empty(updated.Regions)

			stored, err = dbSvc.GetCheck(ctx, org.UID, created.UID)
			r.NoError(err)
			r.Empty(stored.Regions)

			jobs, err = dbSvc.ListCheckJobsByCheckUID(ctx, created.UID)
			r.NoError(err)
			requireOneNullRegionJob(t, jobs)
		})
	}
}

// TestPassiveCheckRegionsInConfigAsCode: a manifest naming a region on a
// heartbeat keeps applying, stores no region, and re-applying the same file
// reads as unchanged (the dropped list is not a perpetual diff).
func TestPassiveCheckRegionsInConfigAsCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupApplyService(t, false)
	ctx := t.Context()

	// A real, declared region: the drop must come from the passive rule, not
	// from the region failing to resolve.
	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions,
		[]regions.RegionDefinition{{Slug: "gravelines", Name: "Gravelines"}}, false))

	heartbeat := checks.ExportCheck{
		Name:    "cron",
		Slug:    "cron",
		Type:    "heartbeat",
		Config:  map[string]any{"token": "cron-token"},
		Enabled: true,
		Regions: []string{"gravelines"},
	}

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("apply-org", heartbeat), checks.ApplyOptions{})
	r.NoError(err)
	r.Empty(res.Errors)
	r.Equal(1, res.Created)

	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "cron")
	r.NoError(err)
	r.Empty(stored.Regions)

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, stored.UID)
	r.NoError(err)
	requireOneNullRegionJob(t, jobs)

	plan, err := svc.ApplyChecks(ctx, org.Slug, doc("apply-org", heartbeat), checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Len(plan.Plan, 1)

	// Other fields may differ (the heartbeat token is a masked secret); the
	// regions the file still names must not.
	for _, change := range plan.Plan[0].Changes {
		r.NotEqualf("regions", change.Field, "the dropped region list must not read as a change: %+v", change)
	}

	// Applying it for real keeps it region-less.
	_, err = svc.ApplyChecks(ctx, org.Slug, doc("apply-org", heartbeat), checks.ApplyOptions{})
	r.NoError(err)

	jobs, err = dbSvc.ListCheckJobsByCheckUID(ctx, stored.UID)
	r.NoError(err)
	requireOneNullRegionJob(t, jobs)
}
