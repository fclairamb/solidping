package freshness_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/freshness"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// hintLog records every `checks` hint that reaches the bus for one org.
type hintLog struct {
	mu     sync.Mutex
	checks int
	uids   map[string]bool
}

func (h *hintLog) snapshot() (int, map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make(map[string]bool, len(h.uids))
	for k, v := range h.uids {
		out[k] = v
	}

	return h.checks, out
}

func (h *hintLog) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.checks = 0
	h.uids = map[string]bool{}
}

// world is the smallest setup that drives both halves of the spec over one
// database and one fake clock: the sweep that enters stale and the result
// pipeline (incidents.ProcessCheckResult) that leaves it.
type world struct {
	clk       *clock.Fake
	db        db.Service
	incidents *incidents.Service
	sweeper   *freshness.Service
	org       *models.Organization
	check     *models.Check
	hints     *hintLog
	t0        time.Time
}

// worldFactory builds one isolated world. Every case below runs on SQLite and
// on real Postgres — the candidate query applies the exact threshold in SQL on
// Postgres only, so both engines must agree on the outcome.
type worldFactory func(t *testing.T, configure func(*models.Check)) *world

func newSQLiteWorld(t *testing.T, configure func(*models.Check)) *world {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return newWorldOn(t, dbSvc, "freshness-test", configure)
}

func newWorldOn(t *testing.T, dbSvc db.Service, orgSlug string, configure func(*models.Check)) *world {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hints := &hintLog{uids: map[string]bool{}}
	orgUIDs := make(chan string, 1)

	listener := bus.Listen(realtime.ChannelOrgEvents)
	go func() {
		orgUID := <-orgUIDs
		for payload := range listener {
			hint, decodeErr := realtime.DecodeHint(payload)
			if decodeErr != nil || hint.Org != orgUID {
				continue
			}

			for _, kind := range hint.Kinds {
				if kind != string(realtime.KindChecks) {
					continue
				}

				hints.mu.Lock()
				hints.checks++
				for _, uid := range hint.CheckUids {
					hints.uids[uid] = true
				}
				hints.mu.Unlock()
			}
		}
	}()

	pub := realtime.NewPublisher(t.Context(), bus, 20*time.Millisecond, nil)
	t.Cleanup(func() {
		pub.Close()
		_ = bus.Close()
	})

	// The check is created with the real wall clock (models.NewCheck), so the
	// fake clock starts at "now" and only ever moves forward from there.
	t0 := time.Now().UTC().Truncate(time.Second)
	clk := clock.NewFake(t0)

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	incidentsSvc := incidents.NewService(dbSvc, jobs, clk, pub)

	org := models.NewOrganization(orgSlug, "Freshness Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	orgUIDs <- org.UID

	check := models.NewCheck(org.UID, "acme", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0

	if configure != nil {
		configure(check)
	}

	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &world{
		clk:       clk,
		db:        dbSvc,
		incidents: incidentsSvc,
		sweeper:   freshness.NewService(dbSvc, incidentsSvc, pub, nil),
		org:       org,
		check:     check,
		hints:     hints,
		t0:        t0,
	}
}

// setClock moves the fake clock forward to `at` (it never moves back).
func (w *world) setClock(at time.Time) {
	if delta := at.Sub(w.clk.Now()); delta > 0 {
		w.clk.Advance(delta)
	}
}

func timeDuration(d time.Duration) timeutils.Duration { return timeutils.Duration(d) }

func (w *world) reload(t *testing.T) *models.Check {
	t.Helper()

	check, err := w.db.GetCheck(t.Context(), w.org.UID, w.check.UID)
	require.NoError(t, err)

	return check
}

// submitAt writes one result at the given instant and runs it through the
// result pipeline with the PERSISTED check, the way the remote-worker path does.
func (w *world) submitAt(t *testing.T, at time.Time, status models.ResultStatus, region string) {
	t.Helper()

	w.setClock(at)

	result := models.NewResult(w.org.UID, w.check.UID, status, 10)
	result.PeriodStart = at

	if region != "" {
		result.Region = &region
	}

	require.NoError(t, w.db.CreateResult(t.Context(), result))
	require.NoError(t, w.incidents.ProcessCheckResult(context.Background(), w.reload(t), result))
}

// sweepAt runs one sweep at `at` and reports whether THIS world's check moved
// to stale (1) or not (0). The sweep is global by design — on the shared
// Postgres database it also sweeps other cases' orgs — so the return value of
// SweepStale itself is not what a case can assert on.
func (w *world) sweepAt(t *testing.T, at time.Time) int {
	t.Helper()

	w.setClock(at)

	before := w.reload(t).Status

	_, err := w.sweeper.SweepStale(t.Context(), at)
	require.NoError(t, err)

	if before != models.CheckStatusStale && w.reload(t).Status == models.CheckStatusStale {
		return 1
	}

	return 0
}

func (w *world) activeIncident(t *testing.T) *models.Incident {
	t.Helper()

	incident, err := w.db.FindActiveIncidentByCheckUID(t.Context(), w.check.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	require.NoError(t, err)

	return incident
}

func (w *world) events(t *testing.T, types ...models.EventType) []*models.Event {
	t.Helper()

	events, err := w.db.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: w.org.UID,
		EventTypes:      types,
		Limit:           100,
	})
	require.NoError(t, err)

	return events
}

func (w *world) jobCount(t *testing.T) int {
	t.Helper()

	jobs, err := w.db.ListJobs(t.Context(), &w.org.UID, 1000)
	require.NoError(t, err)

	return len(jobs)
}

// A pinned check whose worker stops goes stale after max(3 × period, 5 min):
// no incident, no notification, no event, the streak untouched, both clocks
// cleared, and exactly one `checks` hint.
func caseSweepEntersStaleAfterThreshold(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) {
		c.Regions = []string{"eu-west"}
	})

	w.submitAt(t, w.t0, models.ResultStatusUp, "eu-west")
	w.submitAt(t, w.t0.Add(time.Minute), models.ResultStatusUp, "eu-west")

	before := w.reload(t)
	r.Equal(models.CheckStatusUp, before.Status)
	r.NotNil(before.LastResultAt)

	eventsBefore := len(w.events(t))
	jobsBefore := w.jobCount(t)

	time.Sleep(100 * time.Millisecond) // let the result-path hints flush
	w.hints.reset()

	lastResult := w.t0.Add(time.Minute)

	// Period 1 min → threshold is the 5-minute floor. Just under: nothing.
	r.Equal(0, w.sweepAt(t, lastResult.Add(5*time.Minute-time.Second)))
	r.Equal(models.CheckStatusUp, w.reload(t).Status)

	sweptAt := lastResult.Add(5*time.Minute + time.Second)
	r.Equal(1, w.sweepAt(t, sweptAt))

	after := w.reload(t)
	r.Equal(models.CheckStatusStale, after.Status)
	r.Equal("stale", after.Status.String())
	r.NotNil(after.StatusChangedAt)
	r.WithinDuration(sweptAt, *after.StatusChangedAt, time.Second)
	r.Equal(before.StatusStreak, after.StatusStreak, "entering stale must not touch the streak")
	r.Nil(after.FirstFailureAt)
	r.Nil(after.FirstSuccessSinceFailureAt)

	r.Nil(w.activeIncident(t), "stale never opens an incident")
	r.Len(w.events(t), eventsBefore, "entering stale bypasses the event pipeline")
	r.Equal(jobsBefore, w.jobCount(t), "entering stale queues no notification")

	r.Eventually(func() bool {
		count, _ := w.hints.snapshot()

		return count == 1
	}, 2*time.Second, 10*time.Millisecond)

	// A second sweep finds nothing to do (status <> stale filter) and sends
	// no second hint.
	r.Equal(0, w.sweepAt(t, sweptAt.Add(time.Minute)))
	time.Sleep(100 * time.Millisecond)

	count, uids := w.hints.snapshot()
	r.Equal(1, count, "exactly one checks hint")
	r.True(uids[w.check.UID])
}

// A check that never produced a result goes stale once it is older than the
// threshold: it should have run by then.
func caseSweepNeverRanCheckGoesStaleFromCreation(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) { c.Status = models.CheckStatusCreated })
	created := w.reload(t).CreatedAt

	r.Equal(0, w.sweepAt(t, created.Add(4*time.Minute)))
	r.Equal(1, w.sweepAt(t, created.Add(6*time.Minute)))
	r.Equal(models.CheckStatusStale, w.reload(t).Status)
}

// A private-location liveness monitor still `created` (its location has no
// agent yet, so its evaluator writes nothing) never goes stale (spec
// 2026-09-25-05). Positive control: the same monitor once it has a real status
// goes stale like any check.
func caseSweepMonitorAwaitingFirstAgentNeverGoesStale(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) {
		c.Type = string(checkerdef.CheckTypePrivateLocation)
		c.Config = models.JSONMap{"region": "@office"}
		c.Status = models.CheckStatusCreated
	})
	created := w.reload(t).CreatedAt

	r.Equal(0, w.sweepAt(t, created.Add(time.Hour)))
	r.Equal(models.CheckStatusCreated, w.reload(t).Status)

	_, err := w.db.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("status = ?", models.CheckStatusDown).Where("uid = ?", w.check.UID).Exec(t.Context())
	r.NoError(err)

	r.Equal(1, w.sweepAt(t, created.Add(2*time.Hour)))
}

// A long-period check uses 3 × period, not the 5-minute floor — on SQLite the
// candidate query only applies the floor, so this pins the exact Go filter.
func caseSweepLongPeriodUsesThreePeriods(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) { c.Period = timeDuration(10 * time.Minute) })
	w.submitAt(t, w.t0, models.ResultStatusUp, "")

	r.Equal(0, w.sweepAt(t, w.t0.Add(29*time.Minute)))
	r.Equal(models.CheckStatusUp, w.reload(t).Status)
	r.Equal(1, w.sweepAt(t, w.t0.Add(31*time.Minute)))
}

// Disabled and internal checks are never considered.
func caseSweepIgnoresDisabledAndInternal(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) { c.Enabled = false })
	r.Equal(0, w.sweepAt(t, w.t0.Add(time.Hour)))
	r.Equal(models.CheckStatusUp, w.reload(t).Status)

	internal := newWorld(t, func(c *models.Check) { c.Internal = true })
	r.Equal(0, internal.sweepAt(t, internal.t0.Add(time.Hour)))
	r.Equal(models.CheckStatusUp, internal.reload(t).Status)
}

// A result landing at the same moment as the sweep wins: the guarded update is
// a compare-and-set on both the status and the freshness reference.
func caseSweepResultWinsTheRace(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	w := newWorld(t, nil)
	w.submitAt(t, w.t0, models.ResultStatusUp, "")

	now := w.t0.Add(10 * time.Minute)

	candidates, err := w.db.ListStaleCandidates(ctx, now, 100)
	r.NoError(err)
	r.Len(candidates, 1)

	candidate := candidates[0]

	// The result lands between the sweep's read and its write.
	w.submitAt(t, now, models.ResultStatusUp, "")

	changed, err := w.db.MarkCheckStale(ctx, candidate.UID, candidate.Status, now.Add(-5*time.Minute), now)
	r.NoError(err)
	r.False(changed, "a fresh result must beat the sweep")
	r.Equal(models.CheckStatusUp, w.reload(t).Status)

	// Same guard on the status half: a status that moved since the read loses.
	changed, err = w.db.MarkCheckStale(ctx, candidate.UID, models.CheckStatusDown, now.Add(time.Hour), now)
	r.NoError(err)
	r.False(changed)
	r.Equal(models.CheckStatusUp, w.reload(t).Status)
}

// The first result after stale restores the status and bumps the change time.
func caseLeaveStaleFirstResultRestoresStatus(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, nil)
	w.submitAt(t, w.t0, models.ResultStatusUp, "")
	r.Equal(1, w.sweepAt(t, w.t0.Add(10*time.Minute)))

	back := w.t0.Add(12 * time.Minute)
	w.submitAt(t, back, models.ResultStatusUp, "")

	check := w.reload(t)
	r.Equal(models.CheckStatusUp, check.Status)
	r.Equal(1, check.StatusStreak, "the fresh edge restarts the streak")
	r.WithinDuration(back, *check.StatusChangedAt, time.Second)
	r.WithinDuration(back, *check.LastResultAt, time.Second)
}

// "Changed" is decided against the live row, never against the claim-time
// snapshot a worker hands in: the snapshot still says `up` with an old failure
// clock, the row says `stale` with both clocks cleared.
func caseLeaveStaleDecidesAgainstTheLiveRowNotTheSnapshot(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) { c.ConfirmationPeriodSeconds = 120 })

	// A failure arms the confirmation clock; the snapshot is taken now, as a
	// worker's claim would.
	w.submitAt(t, w.t0, models.ResultStatusDown, "")
	snapshot := w.reload(t)
	r.Equal(models.CheckStatusValidating, snapshot.Status)
	r.NotNil(snapshot.FirstFailureAt)

	r.Equal(1, w.sweepAt(t, w.t0.Add(10*time.Minute)))
	time.Sleep(100 * time.Millisecond)
	w.hints.reset()

	// The claim-time snapshot comes back with a failure 10 minutes later.
	at := w.t0.Add(10*time.Minute + time.Second)
	w.setClock(at)

	result := models.NewResult(w.org.UID, w.check.UID, models.ResultStatusDown, 10)
	result.PeriodStart = at
	r.NoError(w.db.CreateResult(t.Context(), result))
	r.NoError(w.incidents.ProcessCheckResult(t.Context(), snapshot, result))

	check := w.reload(t)
	r.Equal(models.CheckStatusValidating, check.Status,
		"the snapshot's 10-minute-old failure clock must not confirm the outage")
	r.WithinDuration(at, *check.FirstFailureAt, time.Second, "confirmation restarts from fresh evidence")
	r.Nil(w.activeIncident(t))

	r.Eventually(func() bool {
		count, _ := w.hints.snapshot()

		return count >= 1
	}, 2*time.Second, 10*time.Millisecond, "the stale→validating flip must send a checks hint")
}

// A failure after a gap waits for a fresh confirmation window.
func caseLeaveStaleFailureWaitsForFreshConfirmation(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) { c.ConfirmationPeriodSeconds = 120 })

	w.submitAt(t, w.t0, models.ResultStatusDown, "")
	r.Equal(models.CheckStatusValidating, w.reload(t).Status)

	r.Equal(1, w.sweepAt(t, w.t0.Add(10*time.Minute)))

	w.submitAt(t, w.t0.Add(11*time.Minute), models.ResultStatusDown, "")
	r.Equal(models.CheckStatusValidating, w.reload(t).Status)
	r.Nil(w.activeIncident(t), "the pre-gap failure must not count toward confirmation")

	w.submitAt(t, w.t0.Add(12*time.Minute), models.ResultStatusDown, "")
	r.Nil(w.activeIncident(t))

	w.submitAt(t, w.t0.Add(13*time.Minute+time.Second), models.ResultStatusDown, "")
	r.Equal(models.CheckStatusDown, w.reload(t).Status)
	r.NotNil(w.activeIncident(t), "a full fresh confirmation window opens the incident")
}

// An open incident stays open while the check is stale, gets "monitoring
// interrupted" and "resumed" on its timeline, and resolves only after a full
// FRESH recovery window.
func caseLeaveStaleOpenIncidentNeedsFreshRecoveryWindow(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, func(c *models.Check) { c.RecoveryPeriodSeconds = 300 })

	w.submitAt(t, w.t0, models.ResultStatusDown, "")
	incident := w.activeIncident(t)
	r.NotNil(incident)

	// One success arms the recovery clock, then the check goes silent.
	w.submitAt(t, w.t0.Add(time.Minute), models.ResultStatusUp, "")
	r.NotNil(w.reload(t).FirstSuccessSinceFailureAt)

	jobsBefore := w.jobCount(t)

	r.Equal(1, w.sweepAt(t, w.t0.Add(10*time.Minute)))

	stale := w.reload(t)
	r.Equal(models.CheckStatusStale, stale.Status)
	r.Nil(stale.FirstSuccessSinceFailureAt, "the recovery clock does not survive a gap")

	still := w.activeIncident(t)
	r.NotNil(still, "no data is not a recovery")
	r.Equal(incident.UID, still.UID)
	r.Len(w.events(t, models.EventTypeIncidentMonitoringInterrupted), 1)
	r.Equal(jobsBefore, w.jobCount(t), "monitoring interrupted never pages")

	// Data resumes: the old clock (armed at t0+1m) would already satisfy the
	// 5-minute recovery period. It must not.
	w.submitAt(t, w.t0.Add(11*time.Minute), models.ResultStatusUp, "")
	r.Equal(models.CheckStatusUp, w.reload(t).Status)
	r.NotNil(w.activeIncident(t), "resolution needs a fresh recovery window")
	r.Len(w.events(t, models.EventTypeIncidentMonitoringResumed), 1)

	w.submitAt(t, w.t0.Add(14*time.Minute), models.ResultStatusUp, "")
	r.NotNil(w.activeIncident(t))

	w.submitAt(t, w.t0.Add(16*time.Minute+time.Second), models.ResultStatusUp, "")
	r.Nil(w.activeIncident(t), "a full fresh recovery window resolves it")
}

// A check never ENTERS stale while its maintenance window is open; the first
// sweep after the window evaluates it normally.
func caseMaintenanceNeverEntersStaleDuringWindow(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	w := newWorld(t, nil)
	w.submitAt(t, w.t0, models.ResultStatusUp, "")

	windowEnd := w.t0.Add(time.Hour)
	window := models.NewMaintenanceWindow(w.org.UID, "deploy", w.t0, windowEnd)
	r.NoError(w.db.CreateMaintenanceWindow(ctx, window))
	r.NoError(w.db.SetMaintenanceWindowChecks(ctx, window.UID, []string{w.check.UID}, nil))

	r.Equal(0, w.sweepAt(t, w.t0.Add(30*time.Minute)))
	r.Equal(models.CheckStatusUp, w.reload(t).Status)

	r.Equal(1, w.sweepAt(t, windowEnd.Add(time.Minute)))
	r.Equal(models.CheckStatusStale, w.reload(t).Status)
}

// A stale check leaves stale immediately if a result arrives during a window,
// without any incident processing.
func caseMaintenanceLeavesStaleDuringWindow(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	w := newWorld(t, nil)
	w.submitAt(t, w.t0, models.ResultStatusUp, "")
	r.Equal(1, w.sweepAt(t, w.t0.Add(10*time.Minute)))

	window := models.NewMaintenanceWindow(w.org.UID, "deploy", w.t0.Add(11*time.Minute), w.t0.Add(2*time.Hour))
	r.NoError(w.db.CreateMaintenanceWindow(ctx, window))
	r.NoError(w.db.SetMaintenanceWindowChecks(ctx, window.UID, []string{w.check.UID}, nil))

	w.submitAt(t, w.t0.Add(15*time.Minute), models.ResultStatusUp, "")
	r.Equal(models.CheckStatusUp, w.reload(t).Status)

	// A failure inside the window after that does not open anything.
	w.submitAt(t, w.t0.Add(16*time.Minute), models.ResultStatusDown, "")
	r.Equal(models.CheckStatusUp, w.reload(t).Status, "maintenance still suppresses incident processing")
	r.Nil(w.activeIncident(t))
}

// A stale check whose first result inside a window is a failure reads
// `validating`, never `down` with no incident behind it.
func caseMaintenanceStaleFailureInWindowReadsValidating(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	w := newWorld(t, nil)
	w.submitAt(t, w.t0, models.ResultStatusUp, "")
	r.Equal(1, w.sweepAt(t, w.t0.Add(10*time.Minute)))

	window := models.NewMaintenanceWindow(w.org.UID, "deploy", w.t0.Add(11*time.Minute), w.t0.Add(2*time.Hour))
	r.NoError(w.db.CreateMaintenanceWindow(ctx, window))
	r.NoError(w.db.SetMaintenanceWindowChecks(ctx, window.UID, []string{w.check.UID}, nil))

	w.submitAt(t, w.t0.Add(15*time.Minute), models.ResultStatusDown, "")

	check := w.reload(t)
	r.Equal(models.CheckStatusValidating, check.Status)
	r.Nil(check.FirstFailureAt, "maintenance arms no clock")
	r.Nil(w.activeIncident(t))
}

// A multi-region check with one silent region is NOT stale: it is still being
// checked. The per-region freshness shows the silent one.
func caseSweepOneSilentRegionIsNotStale(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	w := newWorld(t, func(c *models.Check) { c.Regions = []string{"eu-west", "lauterbourg"} })

	w.submitAt(t, w.t0, models.ResultStatusUp, "lauterbourg")

	for minute := 1; minute <= 30; minute++ {
		w.submitAt(t, w.t0.Add(time.Duration(minute)*time.Minute), models.ResultStatusUp, "eu-west")
	}

	r.Equal(0, w.sweepAt(t, w.t0.Add(31*time.Minute)))
	r.Equal(models.CheckStatusUp, w.reload(t).Status)

	perRegion, err := w.db.ListLastRealResultPerRegion(ctx, w.org.UID, w.check.UID)
	r.NoError(err)
	r.Len(perRegion, 2)

	byRegion := map[string]time.Time{}
	for _, row := range perRegion {
		byRegion[row.Region] = row.LastResultAt
	}

	r.WithinDuration(w.t0, byRegion["lauterbourg"], time.Second)
	r.WithinDuration(w.t0.Add(30*time.Minute), byRegion["eu-west"], time.Second)
}

// Abandoned and lifecycle rows never count as a real result.
func caseProcessCheckResultAbandonedDoesNotRefreshFreshness(t *testing.T, newWorld worldFactory) {
	t.Helper()
	r := require.New(t)

	w := newWorld(t, nil)
	w.submitAt(t, w.t0, models.ResultStatusUp, "")
	w.submitAt(t, w.t0.Add(4*time.Minute), models.ResultStatusAbandoned, "")

	r.WithinDuration(w.t0, *w.reload(t).LastResultAt, time.Second)
	r.Equal(1, w.sweepAt(t, w.t0.Add(6*time.Minute)))
}

// The gauge counts distinct stale checks per placement region, folding private
// regions and any-region jobs.
func TestStaleCountsByRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	counts := freshness.StaleCountsByRegion([]models.StaleCheckPlacement{
		{CheckUID: "a", Region: "lauterbourg"},
		{CheckUID: "a", Region: "eu-west"},
		{CheckUID: "b", Region: "lauterbourg"},
		{CheckUID: "c", Region: ""},
		{CheckUID: "d", Region: "@paris"},
		{CheckUID: "e", Region: "@paris"},
	})

	r.Equal(map[string]int{
		"lauterbourg":                2,
		"eu-west":                    1,
		freshness.RegionLabelAny:     1,
		freshness.RegionLabelPrivate: 2,
	}, counts)
}

// portFreshnessPG is distinct from every other embedded-Postgres port claimed
// in the repo.
const portFreshnessPG = 15540

type freshnessCase struct {
	name string
	run  func(t *testing.T, newWorld worldFactory)
}

func freshnessCases() []freshnessCase {
	return []freshnessCase{
		{"Sweep_EntersStaleAfterThreshold", caseSweepEntersStaleAfterThreshold},
		{"Sweep_NeverRanCheckGoesStaleFromCreation", caseSweepNeverRanCheckGoesStaleFromCreation},
		{"Sweep_MonitorAwaitingFirstAgentNeverGoesStale", caseSweepMonitorAwaitingFirstAgentNeverGoesStale},
		{"Sweep_LongPeriodUsesThreePeriods", caseSweepLongPeriodUsesThreePeriods},
		{"Sweep_IgnoresDisabledAndInternal", caseSweepIgnoresDisabledAndInternal},
		{"Sweep_ResultWinsTheRace", caseSweepResultWinsTheRace},
		{"LeaveStale_FirstResultRestoresStatus", caseLeaveStaleFirstResultRestoresStatus},
		{"LeaveStale_DecidesAgainstTheLiveRowNotTheSnapshot", caseLeaveStaleDecidesAgainstTheLiveRowNotTheSnapshot},
		{"LeaveStale_FailureWaitsForFreshConfirmation", caseLeaveStaleFailureWaitsForFreshConfirmation},
		{"LeaveStale_OpenIncidentNeedsFreshRecoveryWindow", caseLeaveStaleOpenIncidentNeedsFreshRecoveryWindow},
		{"Maintenance_NeverEntersStaleDuringWindow", caseMaintenanceNeverEntersStaleDuringWindow},
		{"Maintenance_LeavesStaleDuringWindow", caseMaintenanceLeavesStaleDuringWindow},
		{"Maintenance_StaleFailureInWindowReadsValidating", caseMaintenanceStaleFailureInWindowReadsValidating},
		{"Sweep_OneSilentRegionIsNotStale", caseSweepOneSilentRegionIsNotStale},
		{"ProcessCheckResult_AbandonedDoesNotRefreshFreshness", caseProcessCheckResultAbandonedDoesNotRefreshFreshness},
	}
}

func TestFreshness_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range freshnessCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, newSQLiteWorld)
		})
	}
}

// TestFreshness_Postgres runs its cases sequentially on purpose (see the loop).
//
//nolint:paralleltest,tparallel // the sweep is global across orgs; parallel cases would sweep each other
func TestFreshness_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portFreshnessPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	var (
		mu    sync.Mutex
		count int
	)

	// One embedded server, one org per world.
	factory := func(t *testing.T, configure func(*models.Check)) *world {
		t.Helper()

		mu.Lock()
		count++
		slug := fmt.Sprintf("freshness-pg-%03d", count)
		mu.Unlock()

		return newWorldOn(t, dbSvc, slug, configure)
	}

	// Sequential, unlike the SQLite runner: the sweep is global, so a case
	// sweeping at "now + 1h" on the shared database would move a concurrently
	// running case's check to stale mid-assertion.
	for _, tc := range freshnessCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, factory)
		})
	}
}
