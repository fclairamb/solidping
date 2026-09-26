package regionsweep_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// Region slugs used across the suite. "lauterbourg" is the region that goes
// dark on 2026-09-24; "paris" stays healthy throughout.
const (
	darkRegion    = "lauterbourg"
	healthyRegion = "paris"
	operatorEmail = "ops@acme.com"
)

// testEnv is one in-memory instance. Every fixture is built against the REAL
// clock, because that is the clock checks.Service.RegionHealth reads.
type testEnv struct {
	db     *sqlite.Service
	checks *checks.Service
	jobs   jobsvc.Service
	acme   *models.Organization
	globex *models.Organization
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, []regions.RegionDefinition{
		{Slug: healthyRegion, Name: "Paris"},
		{Slug: darkRegion, Name: "Lauterbourg"},
	}, false))

	acme := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, acme))

	globex := models.NewOrganization("globex", "Globex")
	r.NoError(dbSvc.CreateOrganization(ctx, globex))

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	return &testEnv{
		db:     dbSvc,
		checks: checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, entSvc),
		jobs:   jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil),
		acme:   acme,
		globex: globex,
	}
}

// deps is the sweep's wiring, the way the job builds it.
func (e *testEnv) deps() *regionsweep.Deps {
	return &regionsweep.Deps{
		DB:       e.db,
		Health:   e.checks,
		Placer:   e.checks,
		Jobs:     e.jobs,
		Operator: opsnotifywire.Build(e.db, &services.Registry{Jobs: e.jobs}, nil),
		BaseURL:  "https://solidping.acme.com",
	}
}

// sweep runs one sweep and fails the test on error.
func (e *testEnv) sweep(t *testing.T) *regionsweep.Result {
	t.Helper()

	result, err := regionsweep.Sweep(t.Context(), e.deps())
	require.NoError(t, err)

	return result
}

// worker registers a worker announcing region, heartbeating now.
func (e *testEnv) worker(t *testing.T, identifier, region string) *models.Worker {
	t.Helper()

	worker := models.NewWorker(identifier, identifier)
	worker.Region = &region

	registered, err := e.db.RegisterOrUpdateWorker(t.Context(), worker)
	require.NoError(t, err)
	require.NoError(t, e.db.UpdateWorkerHeartbeat(t.Context(), registered.UID, []string{}, ""))

	return registered
}

// lastBeat back-dates a worker's last heartbeat.
func (e *testEnv) lastBeat(t *testing.T, worker *models.Worker, ago time.Duration) {
	t.Helper()

	at := time.Now().Add(-ago)
	require.NoError(t, e.db.UpdateWorker(t.Context(), worker.UID, models.WorkerUpdate{LastActiveAt: &at}))
}

// check creates an enabled check with one job per region, the next run
// scheduled in the future (so nothing is overdue).
func (e *testEnv) check(t *testing.T, org *models.Organization, name string, regionSlugs ...string) *models.Check {
	t.Helper()

	return e.checkScheduled(t, org, name, time.Now().Add(30*time.Second), regionSlugs...)
}

// checkScheduled is check with an explicit next run.
func (e *testEnv) checkScheduled(
	t *testing.T, org *models.Organization, name string, scheduledAt time.Time, regionSlugs ...string,
) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, strings.ToLower(strings.ReplaceAll(name, " ", "-")), "http")
	check.Name = &name
	// Regions stay unset so CreateCheck does not derive jobs of its own: the
	// fixture writes exactly the region jobs (and schedule) under test.
	require.NoError(t, e.db.CreateCheck(t.Context(), check))

	for _, region := range regionSlugs {
		slug := region
		at := scheduledAt
		job := &models.CheckJob{
			UID:             uuid.New().String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			Region:          &slug,
			Type:            "http",
			Period:          timeutils.Duration(time.Minute),
			ScheduledAt:     &at,
			UpdatedAt:       time.Now(),
		}

		_, err := e.db.DB().NewInsert().Model(job).Exec(t.Context())
		require.NoError(t, err)
	}

	return check
}

// autoCheck creates an enabled, AUTOMATICALLY placed check currently placed
// on regionSlugs, with its jobs materialized the way the write path does.
func (e *testEnv) autoCheck(t *testing.T, org *models.Organization, name string, regionSlugs ...string) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, strings.ToLower(strings.ReplaceAll(name, " ", "-")), "http")
	check.Name = &name
	check.Config = models.JSONMap{"url": "https://acme.com"}
	check.Placement = models.PlacementAuto
	count := len(regionSlugs)
	check.RegionCount = &count
	check.Regions = regionSlugs
	require.NoError(t, e.db.CreateCheck(t.Context(), check))

	return check
}

// reload re-reads a check.
func (e *testEnv) reload(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	fresh, err := e.db.GetCheck(t.Context(), check.OrganizationUID, check.UID)
	require.NoError(t, err)

	return fresh
}

// jobRegions lists a check's job regions and their next run.
func (e *testEnv) jobRegions(t *testing.T, check *models.Check) map[string]time.Time {
	t.Helper()

	jobs, err := e.db.ListCheckJobsByCheckUID(t.Context(), check.UID)
	require.NoError(t, err)

	out := make(map[string]time.Time, len(jobs))

	for _, job := range jobs {
		region := ""
		if job.Region != nil {
			region = *job.Region
		}

		var at time.Time
		if job.ScheduledAt != nil {
			at = *job.ScheduledAt
		}

		out[region] = at
	}

	return out
}

// admin adds an admin member with an email address to an org.
func (e *testEnv) admin(t *testing.T, org *models.Organization, email string) string {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, e.db.CreateUser(t.Context(), user))
	require.NoError(t, e.db.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	return user.UID
}

// operator creates the platform_watchdog recipient (with an email route) and
// writes the parameter. The operator belongs to an org of their own with no
// checks, so every email they get is an operator notice, never an org one.
func (e *testEnv) operator(t *testing.T, enabled bool) {
	t.Helper()

	ops := models.NewOrganization("platform-ops", "Platform ops")
	require.NoError(t, e.db.CreateOrganization(t.Context(), ops))

	uid := e.admin(t, ops, operatorEmail)
	require.NoError(t, e.db.EnsureDefaultEmailRoute(t.Context(), uid, ops.UID, operatorEmail))
	require.NoError(t, e.db.SetSystemParameter(t.Context(), watchdog.ParamPlatformWatchdog, map[string]any{
		"enabled": enabled, "recipients": []string{uid},
	}, false))
}

// emails returns the email jobs addressed to recipient whose config mentions
// every needle.
func (e *testEnv) emails(t *testing.T, recipient string, needles ...string) []*models.Job {
	t.Helper()

	var jobs []*models.Job

	require.NoError(t, e.db.DB().NewSelect().
		Model(&jobs).
		Where("type = ?", string(jobdef.JobTypeEmail)).
		Where("deleted_at IS NULL").
		Scan(t.Context()))

	out := make([]*models.Job, 0, len(jobs))

	for _, job := range jobs {
		raw, err := json.Marshal(job.Config)
		require.NoError(t, err)

		text := string(raw)
		if !strings.Contains(text, recipient) {
			continue
		}

		matches := true

		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				matches = false
			}
		}

		if matches {
			out = append(out, job)
		}
	}

	return out
}

// events lists one org's events of one type.
func (e *testEnv) events(t *testing.T, org *models.Organization, eventType models.EventType) []*models.Event {
	t.Helper()

	var events []*models.Event

	require.NoError(t, e.db.DB().NewSelect().
		Model(&events).
		Where("organization_uid = ?", org.UID).
		Where("event_type = ?", string(eventType)).
		Scan(t.Context()))

	return events
}

// marker reads the sweep's marker for a region.
func (e *testEnv) marker(t *testing.T, region string) *regionoutage.Marker {
	t.Helper()

	marker, err := regionoutage.Load(t.Context(), e.db, region)
	require.NoError(t, err)

	return marker
}

// transition finds the transition of one region in a result.
func transition(result *regionsweep.Result, region string) *regionsweep.Transition {
	for i := range result.Transitions {
		if result.Transitions[i].Region == region {
			return &result.Transitions[i]
		}
	}

	return nil
}

// strand makes darkRegion dark: its only worker last beat 10 minutes ago.
func (e *testEnv) strand(t *testing.T) *models.Worker {
	t.Helper()

	worker := e.worker(t, "w-"+darkRegion, darkRegion)
	e.lastBeat(t, worker, 10*time.Minute)

	return worker
}
