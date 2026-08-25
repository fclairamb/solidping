package checks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// Ghost-region detection suite (spec 2026-08-24-09), the read-side companion
// of the region-migration suite in region_migration_test.go: it must surface
// every region slug referenced anywhere, and correctly tell a genuinely
// stranded ("ghost") region apart from one that is merely declared and idle.

// healthRegionDefs declares two regions: "healthy" (gets a live worker plus
// checks/jobs) and "dark-unused" (declared, never referenced, never served —
// the case that must NOT read as a ghost).
func healthRegionDefs() []regions.RegionDefinition {
	return []regions.RegionDefinition{
		{Slug: "healthy", Name: "Healthy"},
		{Slug: "dark-unused", Name: "Dark Unused"},
	}
}

// newRegionHealthService builds a checks.Service over a fresh in-memory
// SQLite DB with one org and healthRegionDefs declared.
func newRegionHealthService(t *testing.T) (*checks.Service, *sqlite.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, healthRegionDefs(), false))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// setJobScheduledAt overwrites the scheduled_at of every check_job belonging
// to checkUID (deterministic control over overdue-ness, since createRegionCheck
// materializes jobs scheduled at creation time — which races the assertion
// otherwise).
func setJobScheduledAt(t *testing.T, dbSvc *sqlite.Service, checkUID string, at time.Time) {
	t.Helper()

	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", at).
		Where("check_uid = ?", checkUID).
		Exec(t.Context())
	require.NoError(t, err)
}

// registerLiveWorker registers a worker for the given region and stamps its
// last_active_at as "now" (live).
func registerLiveWorker(t *testing.T, dbSvc *sqlite.Service, slug, region string) *models.Worker {
	t.Helper()

	w := models.NewWorker(slug, slug)
	w.Region = &region

	worker, err := dbSvc.RegisterOrUpdateWorker(t.Context(), w)
	require.NoError(t, err)
	require.NoError(t, dbSvc.UpdateWorkerHeartbeat(t.Context(), worker.UID, []string{}, ""))

	worker, err = dbSvc.GetWorker(t.Context(), worker.UID)
	require.NoError(t, err)

	return worker
}

// TestRegionHealth is the fixture the spec's Tests section demands: every
// case in one call so ghost vs non-ghost classification is exercised
// side-by-side against the same universe.
func TestRegionHealth(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionHealthService(t)
	ctx := t.Context()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-2 * time.Hour)

	// 1. Healthy declared region: a live worker plus a referencing check and
	// a not-yet-due job.
	healthyCheck := createRegionCheck(t, svc, org.Slug, []string{"healthy"})
	setJobScheduledAt(t, dbSvc, healthyCheck.UID, future)
	registerLiveWorker(t, dbSvc, "w-healthy", "healthy")

	// 2. "dark-unused" is declared but has no check, no job, and no worker —
	// dark, but NOT a ghost (nothing depends on it).

	// 3. Ghost with overdue jobs: undeclared slug, no worker, job overdue.
	ghostCheck := createRegionCheck(t, svc, org.Slug, []string{"ghost-overdue"})
	setJobScheduledAt(t, dbSvc, ghostCheck.UID, past)

	// 4. Undeclared slug referenced ONLY via checks.regions: create the check
	// then delete its materialized job, leaving just the checks.regions entry.
	onlyChecksCheck := createRegionCheck(t, svc, org.Slug, []string{"only-in-checks"})
	onlyChecksJobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, onlyChecksCheck.UID)
	r.NoError(err)
	r.Len(onlyChecksJobs, 1)
	r.NoError(dbSvc.DeleteCheckJob(ctx, onlyChecksJobs[0].UID))

	// 5. Prefix-servable slug: job region "us", worker region "us-1" — must
	// NOT be a ghost even though no worker is literally named "us".
	usCheck := createRegionCheck(t, svc, org.Slug, []string{"us"})
	setJobScheduledAt(t, dbSvc, usCheck.UID, future)
	registerLiveWorker(t, dbSvc, "w-us1", "us-1")

	// 6. Private ghost: an org-scoped region with no agent serving it.
	privateCheck := createRegionCheck(t, svc, org.Slug, []string{"@x"})
	setJobScheduledAt(t, dbSvc, privateCheck.UID, future)

	// 7. NULL-region job: bypass checks.Service (which always resolves an
	// empty region list to the full declared set) and go straight to the DB
	// layer, whose CreateCheck materializes exactly one region-less job.
	nullRegionCheck := models.NewCheck(org.UID, "null-region-check", "http")
	nullRegionCheck.Name = strPtr("null-region-check")
	r.NoError(dbSvc.CreateCheck(ctx, nullRegionCheck))

	report, err := svc.RegionHealth(ctx)
	r.NoError(err)

	byslug := make(map[string]checks.RegionHealthRow, len(report.Regions))
	for _, row := range report.Regions {
		// No row may ever represent the NULL-region job as its own slug.
		r.NotEmpty(row.Slug)
		byslug[row.Slug] = row
	}

	healthy := byslug["healthy"]
	r.True(healthy.Declared)
	r.Equal(1, healthy.LiveWorkers)
	r.Equal(1, healthy.ChecksReferencing)
	r.Equal(1, healthy.Jobs)
	r.Equal(0, healthy.JobsOverdue)
	r.False(healthy.Ghost)

	darkUnused := byslug["dark-unused"]
	r.True(darkUnused.Declared)
	r.Equal(0, darkUnused.LiveWorkers)
	r.Equal(0, darkUnused.ChecksReferencing)
	r.Equal(0, darkUnused.Jobs)
	r.False(darkUnused.Ghost, "declared-but-dark AND unused must not be a ghost")

	ghostOverdue := byslug["ghost-overdue"]
	r.False(ghostOverdue.Declared)
	r.Equal(0, ghostOverdue.LiveWorkers)
	r.Equal(1, ghostOverdue.Jobs)
	r.Equal(1, ghostOverdue.JobsOverdue)
	r.NotNil(ghostOverdue.OldestOverdueAt)
	r.WithinDuration(past, *ghostOverdue.OldestOverdueAt, time.Second)
	r.True(ghostOverdue.Ghost)

	onlyInChecks := byslug["only-in-checks"]
	r.False(onlyInChecks.Declared)
	r.Equal(1, onlyInChecks.ChecksReferencing)
	r.Equal(0, onlyInChecks.Jobs)
	r.Equal(0, onlyInChecks.LiveWorkers)
	r.True(onlyInChecks.Ghost, "a slug referenced only via checks.regions is still a ghost")

	us := byslug["us"]
	r.Equal(1, us.Jobs)
	r.Equal(1, us.LiveWorkers, "us-1's region must prefix-match the 'us' slug")
	r.False(us.Ghost, "a prefix-servable slug must not be flagged as a ghost")

	private := byslug["@x"]
	r.False(private.Declared)
	r.Equal(1, private.ChecksReferencing)
	r.Equal(0, private.LiveWorkers)
	r.True(private.Ghost, "a private region with no agent is still a ghost")

	// The NULL-region job's check declared no regions, so it contributes
	// nothing to any row — checked above via the blanket empty-slug assertion,
	// and here by confirming the ghost tally only counts the three designed
	// ghosts.
	r.Equal(3, report.GhostCount)
}

// TestRegionHealthLivenessBoundary pins the inclusive ">=" liveness cutoff
// (mirrors ListLiveWorkers / knownRegionSlugs) at the LITERAL edge: a worker
// whose last_active_at is exactly now - WorkerLivenessWindow counts as live,
// and one a single microsecond older does not. Exact-edge equality against a
// live wall clock would normally be a race (RegionHealth's own `now` is
// always captured slightly after any timestamp the test computed earlier —
// see reference_wallclock_phase_landmine_flakes), so this pins `now` itself
// via SetRegionHealthNowForTest instead of racing time.Now(), which is what
// makes bit-exact edge assertions safe.
func TestRegionHealthLivenessBoundary(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionHealthService(t)
	ctx := t.Context()

	fixedNow := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	checks.SetRegionHealthNowForTest(svc, func() time.Time { return fixedNow })

	exactEdge := fixedNow.Add(-regions.WorkerLivenessWindow)
	justPastEdge := exactEdge.Add(-time.Microsecond)

	check := createRegionCheck(t, svc, org.Slug, []string{"boundary-in", "boundary-out"})
	setJobScheduledAt(t, dbSvc, check.UID, fixedNow.Add(time.Hour))

	insideWorkerRow := models.NewWorker("w-inside", "w-inside")
	insideWorkerRow.Region = strPtr("boundary-in")
	insideWorker, err := dbSvc.RegisterOrUpdateWorker(ctx, insideWorkerRow)
	r.NoError(err)
	r.NoError(dbSvc.UpdateWorker(ctx, insideWorker.UID, models.WorkerUpdate{LastActiveAt: &exactEdge}))

	outsideWorkerRow := models.NewWorker("w-outside", "w-outside")
	outsideWorkerRow.Region = strPtr("boundary-out")
	outsideWorker, err := dbSvc.RegisterOrUpdateWorker(ctx, outsideWorkerRow)
	r.NoError(err)
	r.NoError(dbSvc.UpdateWorker(ctx, outsideWorker.UID, models.WorkerUpdate{LastActiveAt: &justPastEdge}))

	report, err := svc.RegionHealth(ctx)
	r.NoError(err)

	byslug := make(map[string]checks.RegionHealthRow, len(report.Regions))
	for _, row := range report.Regions {
		byslug[row.Slug] = row
	}

	r.Equal(1, byslug["boundary-in"].LiveWorkers, "last_active_at == now-window must count as live (inclusive >=)")
	r.False(byslug["boundary-in"].Ghost)

	r.Equal(0, byslug["boundary-out"].LiveWorkers, "last_active_at one unit older than now-window must not count as live")
	r.True(byslug["boundary-out"].Ghost)
}

// TestRegionHealthLastWorkerSeenAtIncludesDeleted pins that a soft-deleted
// worker still dates a ghost region's last heartbeat, even though it no
// longer counts toward liveWorkers.
func TestRegionHealthLastWorkerSeenAtIncludesDeleted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionHealthService(t)
	ctx := t.Context()

	seenAt := time.Now().Add(-time.Hour)

	check := createRegionCheck(t, svc, org.Slug, []string{"went-dark"})
	setJobScheduledAt(t, dbSvc, check.UID, time.Now().Add(time.Hour))

	workerRow := models.NewWorker("w-dark", "w-dark")
	workerRow.Region = strPtr("went-dark")
	worker, err := dbSvc.RegisterOrUpdateWorker(ctx, workerRow)
	r.NoError(err)
	r.NoError(dbSvc.UpdateWorker(ctx, worker.UID, models.WorkerUpdate{LastActiveAt: &seenAt}))
	r.NoError(dbSvc.DeleteWorker(ctx, worker.UID))

	report, err := svc.RegionHealth(ctx)
	r.NoError(err)

	byslug := make(map[string]checks.RegionHealthRow, len(report.Regions))
	for _, row := range report.Regions {
		byslug[row.Slug] = row
	}

	row := byslug["went-dark"]
	r.Equal(0, row.LiveWorkers)
	r.True(row.Ghost)
	r.NotNil(row.LastWorkerSeenAt)
	r.WithinDuration(seenAt, *row.LastWorkerSeenAt, time.Second)
}

// TestRegionHealthHandlerAuthMatrix verifies the /api/v1/system/regions/health
// route's RequireSuperAdmin gate: non-superadmin users get 403, a superadmin
// gets 200. Mirrors agents/handler_test.go's TestListAllAgentsHandlerAuthMatrix.
func TestRegionHealthHandlerAuthMatrix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _ := newRegionHealthService(t)
	handler := checks.NewHandler(svc, &config.Config{})

	mkUser := func(email string, super bool) *models.User {
		u := models.NewUser(email)
		u.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(t.Context(), u))

		return u
	}

	orgAdmin := mkUser("orgadmin@example.com", false)
	regular := mkUser("user@example.com", false)
	super := mkUser("super@example.com", true)

	authMw := middleware.NewAuthMiddleware(nil, dbSvc, &config.Config{})
	guarded := authMw.RequireSuperAdmin(handler.RegionHealth)

	requestWithUser := func(u *models.User) *http.Request {
		c := context.WithValue(context.Background(), base.ContextKeyUser, u)

		return httptest.NewRequestWithContext(c, http.MethodGet, "/api/v1/system/regions/health", http.NoBody)
	}

	tests := []struct {
		name       string
		user       *models.User
		wantStatus int
	}{
		{"super admin allowed", super, http.StatusOK},
		{"org admin forbidden", orgAdmin, http.StatusForbidden},
		{"regular user forbidden", regular, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			w := httptest.NewRecorder()
			_ = guarded(w, requestWithUser(tc.user))
			rr.Equal(tc.wantStatus, w.Code)
		})
	}
}
