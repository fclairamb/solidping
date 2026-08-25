package checks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Region migration suite (spec 2026-08-24-08). Renaming a worker region leaves
// every materialized check_job under the old slug, where the worker's
// prefix-matched claim can never reach it again — a silent, permanent outage.
// These tests pin both halves of the fix: the operator-facing migration
// endpoint, and the startup pass that heals the same drift unattended.

// migratedRegionDefs is the declared-region set the suite migrates within:
// the pre-rename slugs plus the post-rename ones, which is exactly the shape a
// deployment has mid-rename.
func migratedRegionDefs() []regions.RegionDefinition {
	return []regions.RegionDefinition{
		{Slug: "default", Name: "Default"},
		{Slug: "gravelines", Name: "Gravelines"},
		{Slug: "eu-2", Name: "EU 2"},
		{Slug: "paris", Name: "Paris"},
	}
}

// newRegionMigrationService builds a checks.Service over a fresh in-memory
// SQLite DB with one org and the declared regions above.
func newRegionMigrationService(t *testing.T) (*checks.Service, *sqlite.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, migratedRegionDefs(), false))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// createRegionCheck creates an enabled http check pinned to the given regions.
func createRegionCheck(
	t *testing.T, svc *checks.Service, orgSlug string, regionSlugs []string,
) checks.CheckResponse {
	t.Helper()

	resp, err := svc.CreateCheck(t.Context(), orgSlug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: regionSlugs,
		Period:  strPtr("00:01:00"),
	})
	require.NoError(t, err)

	return resp
}

// jobRegions lists a check's job regions (nil region rendered as "").
func jobRegions(t *testing.T, dbSvc *sqlite.Service, checkUID string) []string {
	t.Helper()

	jobs, err := dbSvc.ListCheckJobsByCheckUID(t.Context(), checkUID)
	require.NoError(t, err)

	out := make([]string, 0, len(jobs))
	for _, job := range jobs {
		region := ""
		if job.Region != nil {
			region = *job.Region
		}

		out = append(out, region)
	}

	return out
}

// TestMigrateRegionMovesChecksAndJobs is the headline case: the check's
// regions array AND its stranded job both move to the new slug.
func TestMigrateRegionMovesChecksAndJobs(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	check := createRegionCheck(t, svc, org.Slug, []string{"default"})

	report, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)

	r.Equal("default", report.From)
	r.Equal("gravelines", report.To)
	r.False(report.DryRun)
	r.Equal(1, report.ChecksUpdated)
	r.Equal(1, report.JobsReassigned)
	r.Zero(report.JobsDeleted)
	r.Equal(map[string]int{org.Slug: 1}, report.ByOrg)

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.Equal([]string{"gravelines"}, stored.Regions)

	r.Equal([]string{"gravelines"}, jobRegions(t, dbSvc, check.UID))
}

// TestMigrateRegionRecoversJobStrandedByRename covers the shape the live
// incident actually had: the CHECK already carries the new slug (it was
// re-saved after the rename), only the job row is stranded under the old one.
// The migration must still find it — a checks.regions-only sweep would miss it
// entirely.
func TestMigrateRegionRecoversJobStrandedByRename(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	check := createRegionCheck(t, svc, org.Slug, []string{"gravelines"})

	// Strand the job under the pre-rename slug, and backdate it so it also
	// exercises the overdue accounting.
	past := time.Now().Add(-2 * time.Hour)
	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("region = ?", "default").
		Set("scheduled_at = ?", past).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	r.NoError(err)

	report, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)

	r.Equal(1, report.ChecksUpdated)
	r.Equal(1, report.JobsReassigned)
	r.Zero(report.JobsDeleted)
	r.Equal(1, report.OverdueRecovered)

	r.Equal([]string{"gravelines"}, jobRegions(t, dbSvc, check.UID))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NotNil(jobs[0].ScheduledAt)
	r.False(jobs[0].ScheduledAt.Before(past.Add(time.Minute)),
		"the recovered job must be rescheduled, not left in the past")
}

// TestMigrateRegionRespectsUniqueJobIndex covers the half-migrated check that
// declares BOTH slugs. A raw `UPDATE check_jobs SET region = ?` would trip the
// unique (check_uid, region) index here; reusing reconcileCheckJobs collapses
// the pair onto the single correct job instead.
func TestMigrateRegionRespectsUniqueJobIndex(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	check := createRegionCheck(t, svc, org.Slug, []string{"default", "gravelines"})
	r.Len(jobRegions(t, dbSvc, check.UID), 2)

	report, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)

	r.Equal(1, report.ChecksUpdated)
	r.Zero(report.JobsReassigned)
	r.Equal(1, report.JobsDeleted)

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.Equal([]string{"gravelines"}, stored.Regions, "the duplicate target must be de-duplicated")

	r.Equal([]string{"gravelines"}, jobRegions(t, dbSvc, check.UID))
}

// TestMigrateRegionDryRunWritesNothing asserts the dry run is a pure read: the
// numbers match the real run, and not one row moves.
func TestMigrateRegionDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	check := createRegionCheck(t, svc, org.Slug, []string{"default"})

	countJobs := func() int {
		n, err := dbSvc.DB().NewSelect().Model((*models.CheckJob)(nil)).Count(ctx)
		r.NoError(err)

		return n
	}

	before := countJobs()

	report, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines", DryRun: true}, "actor-uid")
	r.NoError(err)

	r.True(report.DryRun)
	r.Equal(1, report.ChecksUpdated)
	r.Equal(1, report.JobsReassigned)

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.Equal([]string{"default"}, stored.Regions, "dry run must not rewrite checks.regions")
	r.Equal([]string{"default"}, jobRegions(t, dbSvc, check.UID), "dry run must not move jobs")
	r.Equal(before, countJobs(), "dry run must not create or delete job rows")

	// And the apply that follows reports the very same numbers.
	applied, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)
	r.Equal(report.ChecksUpdated, applied.ChecksUpdated)
	r.Equal(report.JobsReassigned, applied.JobsReassigned)
	r.Equal(report.JobsDeleted, applied.JobsDeleted)
}

// TestMigrateRegionIsIdempotent: a second call finds nothing and returns
// zeros rather than an error.
func TestMigrateRegionIsIdempotent(t *testing.T) {
	t.Parallel()

	svc, _, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	createRegionCheck(t, svc, org.Slug, []string{"default"})

	_, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)

	again, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)
	r.Zero(again.ChecksUpdated)
	r.Zero(again.JobsReassigned)
	r.Zero(again.JobsDeleted)
	r.Zero(again.OverdueRecovered)
	r.Empty(again.ByOrg)
}

// TestMigrateRegionValidation pins every refusal: an undeclared target (which
// would only move the stranding), private → cloud (whose sealed configs cannot
// be re-targeted server-side), and the trivial shape errors.
func TestMigrateRegionValidation(t *testing.T) {
	t.Parallel()

	svc, _, _ := newRegionMigrationService(t)
	ctx := t.Context()

	tests := []struct {
		name    string
		req     checks.RegionMigrationRequest
		wantErr error
	}{
		{
			"undeclared target",
			checks.RegionMigrationRequest{From: "default", To: "atlantis"},
			checks.ErrRegionMigrationTargetUnknown,
		},
		{
			"private to cloud",
			checks.RegionMigrationRequest{From: "@aws-paris", To: "gravelines"},
			checks.ErrRegionMigrationPrivateToCloud,
		},
		{
			"same slug",
			checks.RegionMigrationRequest{From: "default", To: "default"},
			checks.ErrRegionMigrationSameSlug,
		},
		{
			"missing source",
			checks.RegionMigrationRequest{From: "  ", To: "gravelines"},
			checks.ErrRegionMigrationMissingSlug,
		},
		{
			"missing target",
			checks.RegionMigrationRequest{From: "default", To: ""},
			checks.ErrRegionMigrationMissingSlug,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			_, err := svc.MigrateRegion(ctx, tc.req, "actor-uid")
			rr.ErrorIs(err, tc.wantErr)
		})
	}
}

// TestMigrateRegionUndeclaredTargetNamesKnownRegions: the 422 has to tell the
// operator what the valid slugs are, or the typo is unfixable from the error.
func TestMigrateRegionUndeclaredTargetNamesKnownRegions(t *testing.T) {
	t.Parallel()

	svc, _, _ := newRegionMigrationService(t)
	r := require.New(t)

	_, err := svc.MigrateRegion(t.Context(),
		checks.RegionMigrationRequest{From: "default", To: "atlantis"}, "actor-uid")
	r.ErrorIs(err, checks.ErrRegionMigrationTargetUnknown)
	r.Contains(err.Error(), "gravelines")
	r.Contains(err.Error(), "paris")
}

// TestMigrateRegionAcceptsTargetServedByLiveWorkerOnly: a slug nobody declared
// but a live worker serves is a legitimate target — that is how a private
// `@region` served by a connected agent qualifies.
func TestMigrateRegionAcceptsTargetServedByLiveWorkerOnly(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	worker := models.NewWorker("kc-1", "Kansas City 1")
	region := "kansas-city"
	worker.Region = &region
	now := time.Now()
	worker.LastActiveAt = &now
	r.NoError(dbSvc.CreateWorker(ctx, worker))

	createRegionCheck(t, svc, org.Slug, []string{"default"})

	report, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "kansas-city"}, "actor-uid")
	r.NoError(err)
	r.Equal(1, report.ChecksUpdated)
	r.Equal(1, report.JobsReassigned)
}

// TestMigrateRegionIsCrossOrg: org boundaries do not apply to the server
// operator's surface. This is the gap that left ~125 jobs stranded in the live
// incident, where the remediation could only reach one org's checks.
func TestMigrateRegionIsCrossOrg(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	other := models.NewOrganization("globex", "Globex")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	first := createRegionCheck(t, svc, org.Slug, []string{"default"})
	second := createRegionCheck(t, svc, other.Slug, []string{"default"})

	report, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)

	r.Equal(2, report.ChecksUpdated)
	r.Equal(2, report.JobsReassigned)
	r.Equal(map[string]int{org.Slug: 1, other.Slug: 1}, report.ByOrg)

	r.Equal([]string{"gravelines"}, jobRegions(t, dbSvc, first.UID))
	r.Equal([]string{"gravelines"}, jobRegions(t, dbSvc, second.UID))
}

// TestStartupReconcileFixesStaleJobRegions is the root-cause half: a job
// materialized under a slug the check no longer declares must be healed by the
// boot pass, WITHOUT any API call. That is what makes this class of incident
// self-heal on the next deploy even if nobody runs the migration.
func TestStartupReconcileFixesStaleJobRegions(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	check := createRegionCheck(t, svc, org.Slug, []string{"gravelines"})

	// The rename: the worker fleet moved to `gravelines`, but this job row was
	// materialized back when the region was called `default`.
	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("region = ?", "default").
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	r.NoError(err)
	r.Equal([]string{"default"}, jobRegions(t, dbSvc, check.UID))

	n, err := svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Equal(1, n)

	r.Equal([]string{"gravelines"}, jobRegions(t, dbSvc, check.UID))

	// Idempotent, like the period pass it extends.
	n, err = svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(n)

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.Equal([]string{"gravelines"}, stored.Regions)
}

// TestStartupReconcileCreatesMissingRegionJob is the symmetric hole: a region
// the check declares but that has no job at all.
func TestStartupReconcileCreatesMissingRegionJob(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	check := createRegionCheck(t, svc, org.Slug, []string{"gravelines", "paris"})
	r.Len(jobRegions(t, dbSvc, check.UID), 2)

	_, err := dbSvc.DB().NewDelete().
		Model((*models.CheckJob)(nil)).
		Where("check_uid = ?", check.UID).
		Where("region = ?", "paris").
		Exec(ctx)
	r.NoError(err)
	r.Len(jobRegions(t, dbSvc, check.UID), 1)

	n, err := svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Equal(1, n)

	r.ElementsMatch([]string{"gravelines", "paris"}, jobRegions(t, dbSvc, check.UID))

	n, err = svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(n)
}

// TestStartupReconcileLeavesRegionlessChecksAlone: a check with no regions
// legitimately owns exactly one NULL-region job. The stale-region query must
// never mistake that for drift, or every boot would churn every such check —
// and a NULL-region job is claimable by every worker, so there is nothing
// wrong with it.
func TestStartupReconcileLeavesRegionlessChecksAlone(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	// CreateCheck fills in the org's default regions, so the region-less shape
	// (which predates region support, and is what a deployment with no regions
	// parameter still writes) has to be built directly.
	check := createRegionCheck(t, svc, org.Slug, []string{"gravelines"})

	_, err := dbSvc.DB().NewUpdate().
		Model((*models.Check)(nil)).
		Set("regions = NULL").
		Where("uid = ?", check.UID).
		Exec(ctx)
	r.NoError(err)

	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("region = NULL").
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	r.NoError(err)

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.Empty(stored.Regions)

	n, err := svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(n, "a region-less check must not be seen as region-drifted")

	r.Equal([]string{""}, jobRegions(t, dbSvc, check.UID),
		"the NULL-region job must be left exactly as it was")
}

// TestMigrateRegionHandlerAuthMatrix verifies the
// /api/v1/system/regions/migrate route's RequireSuperAdmin gate: an org admin
// gets 403, only a super-admin passes. Mirrors the /system/agents matrix.
func TestMigrateRegionHandlerAuthMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, migratedRegionDefs(), false))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	mkUser := func(email string, super bool) *models.User {
		u := models.NewUser(email)
		u.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, u))

		return u
	}

	// Org role is irrelevant here by design: the gate is server-scope, so even
	// an org owner — the highest org role there is — must be refused.
	orgAdmin := mkUser("orgadmin@example.com", false)
	orgOwner := mkUser("owner@example.com", false)
	super := mkUser("super@example.com", true)

	authMw := middleware.NewAuthMiddleware(nil, dbSvc, &config.Config{})
	guarded := authMw.RequireSuperAdmin(handler.MigrateRegion)

	body := `{"from":"default","to":"gravelines","dryRun":true}`

	requestWithUser := func(u *models.User) *http.Request {
		c := context.WithValue(context.Background(), base.ContextKeyUser, u)

		return httptest.NewRequestWithContext(
			c, http.MethodPost, "/api/v1/system/regions/migrate", strings.NewReader(body))
	}

	tests := []struct {
		name       string
		user       *models.User
		wantStatus int
	}{
		{"super admin allowed", super, http.StatusOK},
		{"org admin forbidden", orgAdmin, http.StatusForbidden},
		{"org owner forbidden", orgOwner, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			w := httptest.NewRecorder()
			_ = guarded(w, requestWithUser(tc.user))
			rr.Equal(tc.wantStatus, w.Code)

			if tc.wantStatus == http.StatusOK {
				var report checks.RegionMigrationReport
				rr.NoError(json.Unmarshal(w.Body.Bytes(), &report))
				rr.True(report.DryRun)
				rr.Equal("gravelines", report.To)
			}
		})
	}
}

// TestMigrateRegionHandlerRejectsUndeclaredTarget pins the wire contract of a
// refusal: 422 with the repository's VALIDATION_ERROR code.
func TestMigrateRegionHandlerRejectsUndeclaredTarget(t *testing.T) {
	t.Parallel()

	svc, dbSvc, _ := newRegionMigrationService(t)
	r := require.New(t)
	ctx := t.Context()

	user := models.NewUser("super@example.com")
	user.SuperAdmin = true
	r.NoError(dbSvc.CreateUser(ctx, user))

	handler := checks.NewHandler(svc, &config.Config{})

	req := httptest.NewRequestWithContext(
		context.WithValue(ctx, base.ContextKeyUser, user),
		http.MethodPost, "/api/v1/system/regions/migrate",
		strings.NewReader(`{"from":"default","to":"atlantis"}`))

	w := httptest.NewRecorder()
	r.NoError(handler.MigrateRegion(w, req))
	r.Equal(http.StatusUnprocessableEntity, w.Code)

	var body map[string]any
	r.NoError(json.Unmarshal(w.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeValidationError), body["code"])
}
