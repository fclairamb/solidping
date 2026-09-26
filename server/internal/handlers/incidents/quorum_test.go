package incidents_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Multi-region quorum (spec 2026-09-25-10). Every scenario here is written
// against db.Service so the Postgres twin (quorum_postgres_test.go) runs the
// exact same code.

const (
	quorumConfirmation = 120 * time.Second
	quorumRecovery     = 120 * time.Second
	quorumStep         = 10 * time.Second
)

// quorumWorld is an org, a fake clock and an incidents service over one
// database, in which each scenario creates the checks it needs.
type quorumWorld struct {
	svc   *incidents.Service
	dbSvc db.Service
	clk   *clock.Fake
	org   *models.Organization
}

func newQuorumWorld(t *testing.T, dbSvc db.Service, orgSlug string) *quorumWorld {
	t.Helper()

	clk := clock.NewFake(time.Now().UTC().Truncate(time.Second))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization(orgSlug, "Quorum")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	return &quorumWorld{svc: svc, dbSvc: dbSvc, clk: clk, org: org}
}

func newSQLiteQuorumWorld(t *testing.T) *quorumWorld {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return newQuorumWorld(t, dbSvc, "quorum-test")
}

// quorumCheck is one check of the world. `snapshot` is the in-memory row the
// test hands to ProcessCheckResult, never refreshed on purpose: production
// hands it a claim-time snapshot too, and the incident engine must decide
// from the LIVE row (current regions included).
type quorumCheck struct {
	w        *quorumWorld
	snapshot *models.Check
}

func (w *quorumWorld) newCheck(t *testing.T, slug string, regions []string, failQuorum *string) *quorumCheck {
	t.Helper()

	check := models.NewCheck(w.org.UID, slug, "http")
	check.Status = models.CheckStatusUp
	check.Regions = regions
	check.FailQuorum = failQuorum
	check.ConfirmationPeriodSeconds = int(quorumConfirmation / time.Second)
	check.RecoveryPeriodSeconds = int(quorumRecovery / time.Second)
	// Flapping off: the recovery period stays a constant, these tests are about
	// which results count, not about the backoff.
	check.FlapBackoffFactor = 1
	require.NoError(t, w.dbSvc.CreateCheck(t.Context(), check))

	snapshot := *check

	return &quorumCheck{w: w, snapshot: &snapshot}
}

// submit processes one result from `region` ("" = a result with no region)
// at the current fake time.
func (c *quorumCheck) submit(t *testing.T, region string, status models.ResultStatus) {
	t.Helper()

	result := models.NewResult(c.w.org.UID, c.snapshot.UID, status, 0)
	result.PeriodStart = c.w.clk.Now()
	result.CreatedAt = result.PeriodStart

	if region != "" {
		result.Region = &region
	}

	require.NoError(t, c.w.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, c.w.svc.ProcessCheckResult(t.Context(), c.snapshot, result))
}

func (c *quorumCheck) reload(t *testing.T) *models.Check {
	t.Helper()

	check, err := c.w.dbSvc.GetCheck(t.Context(), c.w.org.UID, c.snapshot.UID)
	require.NoError(t, err)

	return check
}

func (c *quorumCheck) status(t *testing.T) models.CheckStatus {
	t.Helper()

	return c.reload(t).Status
}

func (c *quorumCheck) incident(t *testing.T) *models.Incident {
	t.Helper()

	incident, err := c.w.dbSvc.FindActiveIncidentByCheckUID(t.Context(), c.snapshot.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	require.NoError(t, err)

	return incident
}

func (c *quorumCheck) incidentCount(t *testing.T) int {
	t.Helper()

	list, _, err := c.w.dbSvc.ListIncidents(t.Context(), &models.ListIncidentsFilter{
		OrganizationUID: c.w.org.UID,
		CheckUIDs:       []string{c.snapshot.UID},
	})
	require.NoError(t, err)

	return len(list)
}

// round submits one result per region, in order, advancing the clock by
// quorumStep after each.
func (c *quorumCheck) round(t *testing.T, readings ...reading) {
	t.Helper()

	for _, rd := range readings {
		c.submit(t, rd.region, rd.status)
		c.w.clk.Advance(quorumStep)
	}
}

type reading struct {
	region string
	status models.ResultStatus
}

func up(region string) reading   { return reading{region: region, status: models.ResultStatusUp} }
func down(region string) reading { return reading{region: region, status: models.ResultStatusDown} }

func strPtr(value string) *string { return &value }

// scenarioOneRegionFailingIsRegional is the spec's headline test: N=3 with
// the default quorum (majority = 2). One failing region gives a regional
// warning and never an incident, however long it lasts; a second failing
// region, past the confirmation period, gives down and an incident.
func scenarioOneRegionFailingIsRegional(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)
	c := w.newCheck(t, "three-regions", []string{"a", "b", "c"}, nil)

	// One region failing for well over the confirmation period (6 rounds of
	// 30s = 3 minutes).
	for range 6 {
		c.round(t, down("a"), up("b"), up("c"))
		r.Equal(models.CheckStatusWarning, c.status(t), "one failing region of 3 is a regional issue")
	}

	r.Nil(c.incident(t), "a regional issue never opens an incident")
	r.Nil(c.reload(t).FirstFailureAt, "the confirmation clock is not armed below the quorum")

	// A second region fails: the quorum (2) is met from here.
	quorumMetAt := w.clk.Now()
	c.submit(t, "b", models.ResultStatusDown)
	r.Equal(models.CheckStatusValidating, c.status(t))
	r.Equal(quorumMetAt.Unix(), c.reload(t).FirstFailureAt.Unix(), "the clock starts when the quorum is met")
	w.clk.Advance(quorumStep)

	// Inside the confirmation window: validating, whichever region reports.
	for w.clk.Now().Before(quorumMetAt.Add(quorumConfirmation - quorumStep)) {
		c.round(t, down("a"))
		r.Equal(models.CheckStatusValidating, c.status(t))
		c.round(t, up("c"))
		r.Equal(models.CheckStatusValidating, c.status(t), "a passing region does not reset a met quorum")
	}

	r.Nil(c.incident(t))

	// Past the confirmation period, a PASSING region's result opens nothing
	// and must not show `down` without an incident behind it.
	w.clk.Advance(quorumMetAt.Add(quorumConfirmation).Sub(w.clk.Now()))
	c.submit(t, "c", models.ResultStatusUp)
	r.Equal(models.CheckStatusValidating, c.status(t), "never down without an incident")
	r.Nil(c.incident(t))

	// The next failing region's result opens it.
	c.submit(t, "a", models.ResultStatusDown)
	r.Equal(models.CheckStatusDown, c.status(t))
	r.NotNil(c.incident(t), "two failing regions past confirmation open an incident")
	r.Equal(1, c.incidentCount(t))
}

// scenarioRecoveryMirrorsQuorum: once fewer than the quorum are failing for
// the recovery period, the incident resolves — even with one region still
// failing, which then reads as the regional issue. A relapse to the quorum
// inside the recovery window restarts it.
func scenarioRecoveryMirrorsQuorum(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)
	c := w.newCheck(t, "recovering", []string{"a", "b", "c"}, nil)

	// Open an incident: a and b failing past confirmation.
	start := w.clk.Now()
	for w.clk.Now().Before(start.Add(quorumConfirmation + quorumStep)) {
		c.round(t, down("a"), down("b"), up("c"))
	}

	incident := c.incident(t)
	r.NotNil(incident)
	r.Equal(models.CheckStatusDown, c.status(t))

	// b recovers: 1 of 3 failing, below the quorum. The recovery clock arms,
	// the check shows the regional issue, the incident stays open.
	recoveryStart := w.clk.Now()
	c.submit(t, "b", models.ResultStatusUp)
	r.Equal(models.CheckStatusWarning, c.status(t))
	r.NotNil(c.incident(t))
	r.Equal(recoveryStart.Unix(), c.reload(t).FirstSuccessSinceFailureAt.Unix())
	w.clk.Advance(quorumStep)

	// a keeps failing: still below the quorum, the recovery clock keeps
	// running.
	c.round(t, down("a"), up("c"))
	r.Equal(recoveryStart.Unix(), c.reload(t).FirstSuccessSinceFailureAt.Unix(),
		"a failing region below the quorum does not reset recovery")

	// Relapse: b fails again, the quorum is met, recovery restarts.
	c.submit(t, "b", models.ResultStatusDown)
	r.Nil(c.reload(t).FirstSuccessSinceFailureAt, "reaching the quorum again clears the recovery clock")
	r.Equal(models.CheckStatusDown, c.status(t))
	w.clk.Advance(quorumStep)

	// b recovers for good.
	secondRecovery := w.clk.Now()
	c.submit(t, "b", models.ResultStatusUp)
	w.clk.Advance(quorumStep)

	for w.clk.Now().Before(secondRecovery.Add(quorumRecovery)) {
		c.submit(t, "a", models.ResultStatusDown)
		c.submit(t, "c", models.ResultStatusUp)
		r.NotNil(c.incident(t), "not resolved inside the recovery period")
		w.clk.Advance(quorumStep)
	}

	c.submit(t, "a", models.ResultStatusDown)
	r.Nil(c.incident(t), "fewer than the quorum failing for the recovery period resolves the incident")
	r.Equal(models.CheckStatusWarning, c.status(t), "the region still failing shows as the regional issue")

	w.clk.Advance(quorumStep)
	c.submit(t, "a", models.ResultStatusUp)
	r.Equal(models.CheckStatusUp, c.status(t), "all regions passing is up")
	r.Equal(1, c.incidentCount(t))
}

// scenarioExplicitIntegerQuorum: N=4 with failQuorum 2 opens on two failing
// regions, where the default (majority of 4 = 3) would not.
func scenarioExplicitIntegerQuorum(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)
	explicit := w.newCheck(t, "four-regions-two", []string{"a", "b", "c", "d"}, strPtr("2"))
	byDefault := w.newCheck(t, "four-regions-default", []string{"a", "b", "c", "d"}, nil)

	for _, c := range []*quorumCheck{explicit, byDefault} {
		c.submit(t, "a", models.ResultStatusDown)
		r.Equal(models.CheckStatusWarning, c.status(t))
	}

	start := w.clk.Now()
	for w.clk.Now().Before(start.Add(quorumConfirmation + 2*quorumStep)) {
		for _, c := range []*quorumCheck{explicit, byDefault} {
			c.submit(t, "a", models.ResultStatusDown)
			c.submit(t, "b", models.ResultStatusDown)
			c.submit(t, "c", models.ResultStatusUp)
			c.submit(t, "d", models.ResultStatusUp)
		}

		w.clk.Advance(quorumStep)
	}

	r.NotNil(explicit.incident(t), "failQuorum 2: two failing regions of 4 open an incident")
	r.Equal(models.CheckStatusDown, explicit.status(t))
	r.Nil(byDefault.incident(t), "default majority of 4 is 3: two failing regions are a regional issue")
	r.Equal(models.CheckStatusWarning, byDefault.status(t))
}

// scenarioReplacedRegionDoesNotCount: an automatic re-placement swaps a
// failing region out of checks.regions. Its stored reading stays in storage
// but no longer counts, and the swapped-in region is unknown (not failing)
// until it reports. A late in-flight result from the old region is recorded
// and ignored.
func scenarioReplacedRegionDoesNotCount(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	c := w.newCheck(t, "replaced", []string{"a", "b", "c"}, nil)

	c.round(t, down("a"), down("b"))
	r.Equal(models.CheckStatusValidating, c.status(t), "two of three failing: quorum met")
	r.NotNil(c.reload(t).FirstFailureAt)

	// The region sweep moves the check off `a` onto `d`. The snapshot handed
	// to ProcessCheckResult still says [a b c] — the engine must read the
	// live regions.
	r.NoError(w.dbSvc.UpdateCheck(ctx, c.snapshot.UID, &models.CheckUpdate{Regions: &[]string{"d", "b", "c"}}))

	start := w.clk.Now()
	for w.clk.Now().Before(start.Add(quorumConfirmation + quorumStep)) {
		c.round(t, down("b"), down("a"), up("c"))
		r.Equal(models.CheckStatusWarning, c.status(t),
			"only b is a current failing region: a regional issue, whatever `a` still says")
	}

	r.Nil(c.incident(t), "a region the check left never counts toward the quorum")
	r.Nil(c.reload(t).FirstFailureAt)

	stored, err := w.dbSvc.ListCheckRegionStates(ctx, c.snapshot.UID)
	r.NoError(err)

	regions := make([]string, 0, len(stored))
	for i := range stored {
		regions = append(regions, stored[i].Region)
	}

	r.Contains(regions, "a", "the left-behind reading is ignored, not pruned")
	r.NotContains(regions, "d", "the new region has not reported yet")

	// The new region reports down: with b, the quorum is met again.
	c.submit(t, "d", models.ResultStatusDown)
	r.Equal(models.CheckStatusValidating, c.status(t))
}

// scenarioStaleRegionReadingIgnored: a failing reading older than the
// check's staleness threshold (5 min floor) does not count.
func scenarioStaleRegionReadingIgnored(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)
	c := w.newCheck(t, "stale-reading", []string{"a", "b", "c"}, nil)

	c.submit(t, "a", models.ResultStatusDown)
	r.Equal(models.CheckStatusWarning, c.status(t))

	// `a` goes silent for 6 minutes (its reading is now stale), then `b`
	// fails: one fresh failing region, not two.
	w.clk.Advance(6 * time.Minute)
	c.submit(t, "b", models.ResultStatusDown)
	r.Equal(models.CheckStatusWarning, c.status(t), "a stale failing reading does not count toward the quorum")
	r.Nil(c.reload(t).FirstFailureAt)
}

// equivalenceStep is one scripted step of the N <= 2 equivalence timeline.
type equivalenceStep struct {
	region  string // for the regional check; the regionless twin gets no region
	status  models.ResultStatus
	advance time.Duration
}

// equivalenceTimeline exercises every edge of the per-result machine:
// alternating regions (never opens), one success resetting the window,
// continuous failure opening at exactly the confirmation period, recovery
// interrupted by a failure, and resolution.
func equivalenceTimeline() []equivalenceStep {
	steps := make([]equivalenceStep, 0, 64)

	add := func(region string, status models.ResultStatus) {
		steps = append(steps, equivalenceStep{region: region, status: status, advance: quorumStep})
	}

	// 5 minutes of "a fails, b passes".
	for range 15 {
		add("a", models.ResultStatusDown)
		add("b", models.ResultStatusUp)
	}

	// Both fail for 90s, then one success, then both fail past confirmation.
	for range 5 {
		add("a", models.ResultStatusDown)
		add("b", models.ResultStatusDown)
	}

	add("b", models.ResultStatusUp)

	for range 8 {
		add("a", models.ResultStatusDown)
		add("b", models.ResultStatusTimeout)
	}

	// Recovery interrupted by one failure, then clean recovery.
	add("a", models.ResultStatusUp)
	add("b", models.ResultStatusUp)
	add("a", models.ResultStatusError)

	for range 8 {
		add("a", models.ResultStatusUp)
		add("b", models.ResultStatusWarning)
	}

	return steps
}

// equivalenceState is what must match step for step.
type equivalenceState struct {
	Status        models.CheckStatus
	Streak        int
	FirstFailure  string
	FirstSuccess  string
	StatusChanged string
	Incident      string
	FailureCount  int
}

func (c *quorumCheck) equivalenceState(t *testing.T) equivalenceState {
	t.Helper()

	check := c.reload(t)
	incident := c.incident(t)
	state := equivalenceState{
		Status:        check.Status,
		Streak:        check.StatusStreak,
		FirstFailure:  timeOrNil(check.FirstFailureAt),
		FirstSuccess:  timeOrNil(check.FirstSuccessSinceFailureAt),
		StatusChanged: timeOrNil(check.StatusChangedAt),
		Incident:      "none",
	}

	if incident != nil {
		state.Incident = incident.StartedAt.UTC().Format(time.RFC3339)
		state.FailureCount = incident.FailureCount
	}

	return state
}

func timeOrNil(value *time.Time) string {
	if value == nil {
		return "nil"
	}

	return value.UTC().Format(time.RFC3339)
}

// scenarioLegacyEquivalence proves the N <= 2 default (and an explicit "all")
// keeps today's per-result behavior exactly: the same scripted timeline,
// replayed on a regionless check (which never had a region to consider) and
// on regional checks, yields identical status, streak, clocks and incident
// after every single step. It also pins the absolute behavior of today's
// rule, so the reference itself cannot drift.
func scenarioLegacyEquivalence(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)

	reference := w.newCheck(t, "regionless", nil, nil)
	twins := map[string]*quorumCheck{
		"two regions, default":   w.newCheck(t, "two-default", []string{"a", "b"}, nil),
		"two regions, majority":  w.newCheck(t, "two-majority", []string{"a", "b"}, strPtr("majority")),
		"three regions, all":     w.newCheck(t, "three-all", []string{"a", "b", "c"}, strPtr("all")),
		"one region":             w.newCheck(t, "one-region", []string{"a"}, nil),
		"two regions, quorum 99": w.newCheck(t, "two-ninety-nine", []string{"a", "b"}, strPtr("99")),
	}

	openedAt := -1

	for i, step := range equivalenceTimeline() {
		reference.submit(t, "", step.status)

		for _, twin := range twins {
			twin.submit(t, step.region, step.status)
		}

		want := reference.equivalenceState(t)
		for name, twin := range twins {
			r.Equal(want, twin.equivalenceState(t), "step %d (%s %s): %s diverged", i, step.region,
				models.StatusToString(int(step.status)), name)
		}

		if openedAt < 0 && want.Incident != "none" {
			openedAt = i
		}

		w.clk.Advance(step.advance)
	}

	// Today's rule, pinned absolutely: 30 alternating steps never opened
	// anything; the incident opened on the first failure at least 120s after
	// the failure streak that followed the single success at step 40.
	r.Equal(41+12, openedAt,
		"continuous failure opens at exactly the confirmation period")
	r.Equal(1, reference.incidentCount(t))
	r.Nil(reference.incident(t), "the clean recovery resolved it")

	// Region readings are kept for the 2+ region checks, not for one region.
	states, err := w.dbSvc.ListCheckRegionStates(t.Context(), twins["two regions, default"].snapshot.UID)
	r.NoError(err)
	r.Len(states, 2)

	states, err = w.dbSvc.ListCheckRegionStates(t.Context(), twins["one region"].snapshot.UID)
	r.NoError(err)
	r.Empty(states)
}

// scenarioRegionStateStatusSince: status_since moves only when the region
// crosses between failing and passing, and an older reading processed late
// never overwrites a newer one.
func scenarioRegionStateStatusSince(t *testing.T, w *quorumWorld) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	c := w.newCheck(t, "status-since", []string{"a", "b"}, nil)

	failingSince := w.clk.Now()
	c.submit(t, "a", models.ResultStatusDown)
	w.clk.Advance(quorumStep)
	c.submit(t, "a", models.ResultStatusTimeout)

	latest := w.clk.Now()

	// A late, older result from the same region.
	old := models.NewResult(w.org.UID, c.snapshot.UID, models.ResultStatusUp, 0)
	old.PeriodStart = failingSince.Add(-time.Minute)
	region := "a"
	old.Region = &region
	r.NoError(w.dbSvc.CreateResult(ctx, old))
	r.NoError(w.svc.ProcessCheckResult(ctx, c.snapshot, old))

	states, err := w.dbSvc.ListCheckRegionStates(ctx, c.snapshot.UID)
	r.NoError(err)
	r.Len(states, 1)
	r.Equal(models.ResultStatusTimeout, states[0].Status, "an older reading never overwrites a newer one")
	r.Equal(failingSince.Unix(), states[0].StatusSince.Unix(), "down -> timeout stays on the failing side")
	r.Equal(latest.Unix(), states[0].LastResultAt.Unix())

	w.clk.Advance(quorumStep)
	passingSince := w.clk.Now()
	c.submit(t, "a", models.ResultStatusWarning)

	states, err = w.dbSvc.ListCheckRegionStates(ctx, c.snapshot.UID)
	r.NoError(err)
	r.Equal(passingSince.Unix(), states[0].StatusSince.Unix(), "crossing to passing moves status_since")
}

func TestQuorumOneRegionFailingIsRegional(t *testing.T) {
	t.Parallel()
	scenarioOneRegionFailingIsRegional(t, newSQLiteQuorumWorld(t))
}

func TestQuorumRecoveryMirrorsQuorum(t *testing.T) {
	t.Parallel()
	scenarioRecoveryMirrorsQuorum(t, newSQLiteQuorumWorld(t))
}

func TestQuorumExplicitIntegerQuorum(t *testing.T) {
	t.Parallel()
	scenarioExplicitIntegerQuorum(t, newSQLiteQuorumWorld(t))
}

func TestQuorumReplacedRegionDoesNotCount(t *testing.T) {
	t.Parallel()
	scenarioReplacedRegionDoesNotCount(t, newSQLiteQuorumWorld(t))
}

func TestQuorumStaleRegionReadingIgnored(t *testing.T) {
	t.Parallel()
	scenarioStaleRegionReadingIgnored(t, newSQLiteQuorumWorld(t))
}

func TestQuorumLegacyEquivalence(t *testing.T) {
	t.Parallel()
	scenarioLegacyEquivalence(t, newSQLiteQuorumWorld(t))
}

func TestQuorumRegionStateStatusSince(t *testing.T) {
	t.Parallel()
	scenarioRegionStateStatusSince(t, newSQLiteQuorumWorld(t))
}
