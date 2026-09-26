package degradedeval_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/degradedeval"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// evalSetup is the smallest world that can drive both halves of this spec: the
// per-probe incident state machine (incidents.ProcessCheckResult) and the
// periodic degraded evaluator, over the same check, the same results table and
// the same fake clock. Both halves matter together — the whole point of the
// feature is that the first one stays silent where the second one speaks.
type evalSetup struct {
	clk       *clock.Fake
	dbSvc     *sqlite.Service
	incidents *incidents.Service
	eval      *degradedeval.Service
	org       *models.Organization
	check     *models.Check
}

const testPeriod = time.Minute

// intPtr spells an EXPLICIT degraded setting. Leaving a field nil is the other
// half of the contract these tests exercise: NULL means "use the code default",
// so a test that wants 5-of-60 sets nothing at all.
func intPtr(value int) *int { return &value }

// newEvalSetup builds the world. `configure` gets the check before it is
// written, so a test can retune the rules or turn the feature off.
func newEvalSetup(t *testing.T, configure func(*models.Check)) *evalSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// Anchored three hours back so every probe the tests write lands inside the
	// evaluator's 24 h look-back and inside a "7d" availability window.
	clk := clock.NewFake(time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	incidentsSvc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization("degraded-test", "Degraded Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "acme", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 120
	check.RecoveryPeriodSeconds = 0

	if configure != nil {
		configure(check)
	}

	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &evalSetup{
		clk:       clk,
		dbSvc:     dbSvc,
		incidents: incidentsSvc,
		eval:      degradedeval.NewService(dbSvc, incidentsSvc, clk, nil),
		org:       org,
		check:     check,
	}
}

// reload reads the persisted check, never the in-memory copy.
func (s *evalSetup) reload(t *testing.T) *models.Check {
	t.Helper()

	check, err := s.dbSvc.GetCheck(t.Context(), s.org.UID, s.check.UID)
	require.NoError(t, err)

	return check
}

// submit writes one probe AND runs it through the per-probe incident state
// machine, then advances the clock by one period — exactly what production does
// on every execution.
func (s *evalSetup) submit(t *testing.T, status models.ResultStatus, durationMs float32) {
	t.Helper()
	s.submitTagged(t, status, durationMs, false)
}

// submitTagged is submit with the maintenance tag, for the non-slot test.
func (s *evalSetup) submitTagged(
	t *testing.T, status models.ResultStatus, durationMs float32, maintenance bool,
) {
	t.Helper()

	result := models.NewResult(s.org.UID, s.check.UID, status, durationMs)
	result.PeriodStart = s.clk.Now()
	result.Maintenance = maintenance
	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))

	check := s.reload(t)
	require.NoError(t, s.incidents.ProcessCheckResult(context.Background(), check, result))

	s.clk.Advance(testPeriod)
}

// up / down are the two shorthands the timelines below are written in.
func (s *evalSetup) up(t *testing.T, count int, durationMs float32) {
	t.Helper()

	for range count {
		s.submit(t, models.ResultStatusUp, durationMs)
	}
}

func (s *evalSetup) down(t *testing.T) {
	t.Helper()
	s.submit(t, models.ResultStatusDown, 0)
}

// evaluate runs one sweep at the current fake time.
func (s *evalSetup) evaluate(t *testing.T) {
	t.Helper()

	_, err := s.eval.EvaluateDegraded(t.Context(), s.clk.Now())
	require.NoError(t, err)
}

// incidentsOfKind returns every incident on the check with the given kind.
func (s *evalSetup) incidentsOfKind(t *testing.T, kind string) []*models.Incident {
	t.Helper()

	rows, _, err := s.dbSvc.ListIncidents(t.Context(), &models.ListIncidentsFilter{
		OrganizationUID: s.org.UID,
		CheckUIDs:       []string{s.check.UID},
		Kinds:           []string{kind},
		Limit:           100,
	})
	require.NoError(t, err)

	return rows
}

func (s *evalSetup) activeDegraded(t *testing.T) *models.Incident {
	t.Helper()

	incident, err := s.dbSvc.FindActiveDegradedIncident(t.Context(), s.check.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	require.NoError(t, err)

	return incident
}

// --- The negative control ---------------------------------------------------

// TestNegativeControlIntermittentFailures replays the Problem shape measured in
// production: seven failures spread over forty minutes, each one followed by a
// success before the 120 s confirmation period elapses. Today that produces
// nothing at all — no incident, no notification, no history line. It must now
// produce exactly ONE degraded incident and still ZERO check incidents.
func TestNegativeControlIntermittentFailures(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, nil)

	// 14:35-14:37 — three slow-but-successful probes, then healthy minutes and
	// seven isolated failures, each followed immediately by a success.
	s.up(t, 10, 453)

	for range 7 {
		s.down(t)
		s.up(t, 4, 453)
	}

	r.Empty(s.incidentsOfKind(t, models.IncidentKindCheck),
		"the confirmation period is doing its job: no failure was ever confirmed")

	s.evaluate(t)

	degradedIncidents := s.incidentsOfKind(t, models.IncidentKindDegraded)
	r.Len(degradedIncidents, 1, "7 failures in 60 probes must trip the 5-of-60 failure rule")
	r.Equal(models.IncidentStateActive, degradedIncidents[0].State)
	r.Empty(s.incidentsOfKind(t, models.IncidentKindCheck),
		"detecting intermittence must not manufacture an outage")

	// The incident carries the numbers a reader needs, and starts at the FIRST
	// bad probe rather than at the instant the sweep noticed.
	r.Contains(*degradedIncidents[0].Title, "is degraded")
	r.NotContains(*degradedIncidents[0].Title, "is down")
	r.True(degradedIncidents[0].StartedAt.Before(s.clk.Now()))
	r.EqualValues(7, degradedIncidents[0].Details["degraded_failure_count"])

	// And a degraded incident never schedules an escalation cycle — notify-only.
	r.Nil(degradedIncidents[0].EscalatedAt)
}

// --- The positive control --------------------------------------------------

// TestPositiveControlContinuousOutage pins the other direction: a real,
// continuous outage opens a CHECK incident and no degraded one. Without the
// suppression rule this would double every outage with an amber twin — measured
// at 114 degraded incidents a day fleet-wide instead of 25.
func TestPositiveControlContinuousOutage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, nil)

	s.up(t, 5, 100)

	for range 10 {
		s.down(t)
	}

	r.Len(s.incidentsOfKind(t, models.IncidentKindCheck), 1, "a continuous outage is an outage")

	s.evaluate(t)

	r.Empty(s.incidentsOfKind(t, models.IncidentKindDegraded),
		"suppressed while a check incident is open — do not weaken this rule")
}

// --- The slow rule, and its sequencing ------------------------------------

// TestSlowRuleFiresBeforeFailureRule replays the motivating episode's own
// timeline: three slow-but-successful probes at 14:35-14:37 (2565 ms, 1362 ms,
// 5534 ms), five minutes before the first failure and thirty-three minutes
// before any failure rule could fire. The slow rule is the priority, not the
// add-on.
func TestSlowRuleFiresBeforeFailureRule(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.SlowThresholdMs = intPtr(1000) // ~2x the measured 453 ms baseline
	})

	s.up(t, 20, 453)

	// The opening triple.
	s.submit(t, models.ResultStatusUp, 2565)
	s.submit(t, models.ResultStatusUp, 1362)
	s.submit(t, models.ResultStatusUp, 5534)

	s.evaluate(t)

	degradedIncidents := s.incidentsOfKind(t, models.IncidentKindDegraded)
	r.Len(degradedIncidents, 1, "3 of the last 6 probes were over the threshold")
	r.Equal(true, degradedIncidents[0].Details["degraded_slow_fired"])
	r.Equal(false, degradedIncidents[0].Details["degraded_failures_fired"],
		"the failure rule is still silent — this is why there are two rules")
	r.Empty(s.incidentsOfKind(t, models.IncidentKindCheck))

	// Later the failures arrive and the failure rule fires too. The incident is
	// updated in place rather than duplicated.
	for range 6 {
		s.down(t)
		s.up(t, 4, 453)
	}

	s.evaluate(t)

	degradedIncidents = s.incidentsOfKind(t, models.IncidentKindDegraded)
	r.Len(degradedIncidents, 1, "a worsening episode updates the incident, never opens a second")
	r.Equal(true, degradedIncidents[0].Details["degraded_failures_fired"])
}

// --- Resolution ------------------------------------------------------------

// TestResolutionAfterCleanProbes pins that a degraded incident closes only once
// the condition has been false for the governing number of consecutive
// countable probes — and that the window it closes under is the one it OPENED
// under.
func TestResolutionAfterCleanProbes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.DegradedFailures = intPtr(0) // failure rule off: this is the slow rule's test
		check.DegradedSlow = intPtr(3)
		check.DegradedSlowWindow = intPtr(6)
		check.SlowThresholdMs = intPtr(1000)
	})

	s.up(t, 6, 200)
	s.submit(t, models.ResultStatusUp, 5000)
	s.submit(t, models.ResultStatusUp, 5000)
	s.submit(t, models.ResultStatusUp, 5000)

	s.evaluate(t)

	open := s.activeDegraded(t)
	r.NotNil(open)
	r.EqualValues(6, open.Details["degraded_resolve_window"])

	// Five clean probes: one short. The incident must stay open.
	s.up(t, 5, 200)
	s.evaluate(t)
	r.NotNil(s.activeDegraded(t), "five clean probes is not six")

	// The sixth closes it.
	s.up(t, 1, 200)
	s.evaluate(t)

	r.Nil(s.activeDegraded(t))

	closed := s.incidentsOfKind(t, models.IncidentKindDegraded)
	r.Len(closed, 1)
	r.Equal(models.IncidentStateResolved, closed[0].State)
	r.NotNil(closed[0].ResolutionType)
	r.Equal(models.ResolutionTypeAuto, *closed[0].ResolutionType)
}

// --- Maintenance is not a slot ---------------------------------------------

// TestMaintenanceProbesAreNotSlots pins the rule at the evaluator level, over
// real rows: a maintenance-tagged failure must neither count nor push a real
// probe out of the window.
func TestMaintenanceProbesAreNotSlots(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.DegradedFailures = intPtr(3)
		check.DegradedFailuresWindow = intPtr(10)
	})

	s.up(t, 10, 100)

	// Three failures, all inside planned maintenance. ProcessCheckResult skips
	// them as well, so nothing opens on the per-probe path either.
	for range 3 {
		s.submitTagged(t, models.ResultStatusDown, 0, true)
	}

	s.evaluate(t)

	r.Empty(s.incidentsOfKind(t, models.IncidentKindDegraded),
		"planned maintenance must never trip a degraded rule")

	// The same three failures untagged do fire. Spaced out, so the per-probe path
	// still never confirms an outage — otherwise the suppression rule would be
	// what kept the degraded incident away, and this test would prove nothing
	// about maintenance.
	for range 3 {
		s.down(t)
		s.up(t, 1, 100)
	}

	s.evaluate(t)
	r.Len(s.incidentsOfKind(t, models.IncidentKindDegraded), 1)
}

// --- Degraded detection off ------------------------------------------------

// TestDisabledCheckIsNotEvaluated pins that a check with degraded_enabled false
// is not evaluated at all: the sweep does not list it, and even a direct
// EvaluateCheck opens nothing and writes nothing on the row, not even the
// rotation cursor.
func TestDisabledCheckIsNotEvaluated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.DegradedEnabled = false
	})

	s.up(t, 10, 100)

	for range 7 {
		s.down(t)
		s.up(t, 4, 100)
	}

	queue, err := s.dbSvc.ListChecksForDegradedEval(t.Context(), 0)
	r.NoError(err)
	r.Empty(queue, "a disabled check is not in the sweep's work queue")

	s.evaluate(t)
	r.NoError(s.eval.EvaluateCheck(t.Context(), s.reload(t), s.clk.Now()))

	r.Empty(s.incidentsOfKind(t, models.IncidentKindDegraded),
		"the rules match, but a disabled check opens nothing")
	r.Nil(s.reload(t).DegradedEvaluatedAt, "and nothing is written on the check row")
}

// TestEnablingStartsEvaluation is the positive control: the same timeline on
// the same check opens a degraded incident once the flag is turned on.
func TestEnablingStartsEvaluation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.DegradedEnabled = false
	})

	s.up(t, 10, 100)

	for range 7 {
		s.down(t)
		s.up(t, 4, 100)
	}

	s.evaluate(t)
	r.Empty(s.incidentsOfKind(t, models.IncidentKindDegraded))

	enabled := true
	r.NoError(s.dbSvc.UpdateCheck(t.Context(), s.check.UID, &models.CheckUpdate{DegradedEnabled: &enabled}))

	queue, err := s.dbSvc.ListChecksForDegradedEval(t.Context(), 0)
	r.NoError(err)
	r.Len(queue, 1)
	r.Equal(s.check.UID, queue[0].UID)

	s.evaluate(t)

	r.Len(s.incidentsOfKind(t, models.IncidentKindDegraded), 1, "enabled, it opens for real")
	r.NotNil(s.reload(t).DegradedEvaluatedAt)
}

// TestResolveDegradedOnDisable pins the incidents-side half of the toggle-off
// hook (spec 2026-09-24-08): the open degraded incident closes as `disabled`,
// with the details marker the resolved notification reads.
func TestResolveDegradedOnDisable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, nil)

	s.up(t, 10, 100)

	for range 7 {
		s.down(t)
		s.up(t, 4, 100)
	}

	s.evaluate(t)

	open := s.activeDegraded(t)
	r.NotNil(open)

	r.NoError(s.incidents.ResolveDegradedOnDisable(t.Context(), s.check.UID))
	r.Nil(s.activeDegraded(t))

	closed, err := s.dbSvc.GetIncident(t.Context(), s.org.UID, open.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, closed.State)
	r.NotNil(closed.ResolutionType)
	r.Equal(models.ResolutionTypeDisabled, *closed.ResolutionType)
	r.Equal(true, closed.Details["degraded_turned_off"])

	// Nothing open: a no-op.
	r.NoError(s.incidents.ResolveDegradedOnDisable(t.Context(), s.check.UID))
}

// --- Escalation into a real outage ----------------------------------------

// TestDegradedEscalatesIntoOutage pins the no-in-place-promotion rule: when a
// real outage opens on a degraded check, the degraded incident resolves as
// `escalated` and the outage carries caused_by_incident_uid. Nothing keyed on
// `kind` ever sees a kind change mid-life.
func TestDegradedEscalatesIntoOutage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.ConfirmationPeriodSeconds = 0 // open the outage on the first failure
	})

	s.up(t, 10, 100)

	for range 7 {
		s.down(t)
		s.up(t, 4, 100)
	}

	s.evaluate(t)

	degradedIncident := s.activeDegraded(t)
	r.NotNil(degradedIncident)

	// Now the target actually goes down and stays down.
	s.down(t)

	outages := s.incidentsOfKind(t, models.IncidentKindCheck)
	r.Len(outages, 1)

	r.Nil(s.activeDegraded(t), "the degraded incident must not outlive its escalation")

	closed := s.incidentsOfKind(t, models.IncidentKindDegraded)
	r.Len(closed, 1)
	r.Equal(models.IncidentStateResolved, closed[0].State)
	r.NotNil(closed[0].ResolutionType)
	r.Equal(models.ResolutionTypeEscalated, *closed[0].ResolutionType)

	r.NotNil(outages[0].CausedByIncidentUID)
	r.Equal(degradedIncident.UID, *outages[0].CausedByIncidentUID)
	r.False(outages[0].PagingSuppressed,
		"provenance is not suppression: an outage preceded by a degraded episode still pages")

	// While the outage is open the evaluator opens nothing new.
	s.evaluate(t)
	r.Nil(s.activeDegraded(t))

	// Once the outage resolves and the check is still intermittent, a NEW
	// degraded incident opens — the evaluator simply runs again.
	s.up(t, 1, 100)
	r.Empty(s.activeCheckIncident(t), "the outage resolved")

	s.evaluate(t)

	r.NotNil(s.activeDegraded(t), "still intermittent, so a fresh degraded incident opens")
	r.Len(s.incidentsOfKind(t, models.IncidentKindDegraded), 2, "a new row, never a revived one")
}

// activeCheckIncident returns the open check incident, or an empty slice.
func (s *evalSetup) activeCheckIncident(t *testing.T) []*models.Incident {
	t.Helper()

	_, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	require.NoError(t, err)

	return []*models.Incident{{}}
}

// --- Rules off ------------------------------------------------------------

// TestBothRulesOffEvaluatesNothing pins that M = 0 and slow_threshold_ms = 0
// really is off — the reason none of these columns carries a bun `default:`.
func TestBothRulesOffEvaluatesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, func(check *models.Check) {
		check.DegradedFailures = intPtr(0)
		check.DegradedSlow = intPtr(0)
		check.SlowThresholdMs = intPtr(0)
	})

	s.up(t, 5, 9999)

	for range 20 {
		s.down(t)
		s.up(t, 1, 100)
	}

	s.evaluate(t)

	r.Empty(s.incidentsOfKind(t, models.IncidentKindDegraded))
	r.NotNil(s.reload(t).DegradedEvaluatedAt, "still swept, still rotated")
}
