package sloalerts_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/sloalerts"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Burn windows are ROLLING and read from the raw tier, which uptimebar clamps
// against the real wall clock (raw retention). Fixtures therefore hang off
// time.Now() rather than a fixed calendar date the way the burn-down tests do.
type env struct {
	t         *testing.T
	db        *sqlite.Service
	slos      *slos.Service
	incidents *incidents.Service
	svc       *sloalerts.Service
	org       *models.Organization
	check     *models.Check
}

func newEnv(t *testing.T) *env {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.CreatedAt = time.Now().Add(-90 * 24 * time.Hour)
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	sloSvc := slos.NewService(dbSvc, &config.Config{}, nil)
	incidentSvc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	return &env{
		t:         t,
		db:        dbSvc,
		slos:      sloSvc,
		incidents: incidentSvc,
		svc: sloalerts.NewService(
			dbSvc, sloSvc, incidentSvc, clock.Real{},
			slog.New(slog.DiscardHandler),
		),
		org:   org,
		check: check,
	}
}

// seedSLO creates a 99.9% objective over the env's check.
func (e *env) seedSLO() *models.SLO {
	e.t.Helper()

	objective := models.NewSLO(e.org.UID, "api", "api", 99.9)
	objective.CheckUID = &e.check.UID
	require.NoError(e.t, e.db.CreateSLO(e.t.Context(), objective))

	policies, err := e.slos.EnsureAlertPolicies(e.t.Context(), e.org.UID, objective.UID)
	require.NoError(e.t, err)
	require.Len(e.t, policies, 2)

	return objective
}

// policy returns one built-in policy, enabled, for the given SLO.
func (e *env) enablePolicy(sloUID, kind string) *models.SLOAlertPolicy {
	e.t.Helper()

	policies, err := e.db.ListSLOAlertPolicies(e.t.Context(), sloUID)
	require.NoError(e.t, err)

	for _, policy := range policies {
		if policy.Kind != kind {
			continue
		}

		enabled := true
		require.NoError(e.t, e.db.UpdateSLOAlertPolicy(
			e.t.Context(), policy.UID, models.SLOAlertPolicyUpdate{Enabled: &enabled},
		))

		reloaded, getErr := e.db.GetSLOAlertPolicy(e.t.Context(), e.org.UID, policy.UID)
		require.NoError(e.t, getErr)

		return reloaded
	}

	require.FailNow(e.t, "policy kind not found: "+kind)

	return nil
}

// seedRaw writes `count` raw probes spread evenly backwards from `end`, of
// which `failures` are down. Raw rows are what a 5-minute window can possibly
// be answered from — the rollup tiers have no bucket that small.
//
// The failures are INTERLEAVED rather than bunched at the start, because the
// short confirmation window only ever looks at the newest slice: a fixture
// that front-loads its failures produces a hot long window and a clean short
// one, which is a different scenario entirely (and has its own test).
func (e *env) seedRaw(end time.Time, span time.Duration, count, failures int, maintenance bool) {
	e.t.Helper()

	if count == 0 {
		return
	}

	step := span / time.Duration(count)

	for i := range count {
		status := int(models.ResultStatusUp)
		if (i*failures)/count < ((i+1)*failures)/count {
			status = int(models.ResultStatusDown)
		}

		at := end.Add(-span).Add(time.Duration(i)*step + step/2)

		require.NoError(e.t, e.db.CreateResult(e.t.Context(), &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: e.org.UID,
			CheckUID:        e.check.UID,
			PeriodType:      models.PeriodTypeRaw,
			PeriodStart:     at,
			Status:          &status,
			Maintenance:     maintenance,
			CreatedAt:       at,
		}))
	}
}

// activeBurnIncidents returns the SLO's open burn incidents.
func (e *env) activeBurnIncidents(sloUID string) []*models.Incident {
	e.t.Helper()

	out, err := e.db.ListActiveBurnIncidentsForSLOs(e.t.Context(), e.org.UID, []string{sloUID})
	require.NoError(e.t, err)

	return out
}

func (e *env) reloadPolicy(uid string) *models.SLOAlertPolicy {
	e.t.Helper()

	policy, err := e.db.GetSLOAlertPolicy(e.t.Context(), e.org.UID, uid)
	require.NoError(e.t, err)

	return policy
}

// TestFastBurnFiresWhenBothWindowsExceedThreshold is the happy path: a 99.9%
// objective failing 50% of its probes burns at 500x, far over the 14.4x fast
// threshold, in both windows.
func TestFastBurnFiresWhenBothWindowsExceedThreshold(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	policy := e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	// The whole hour is bad, so the 5m window inside it is bad too.
	e.seedRaw(now, time.Hour, 60, 30, false)

	evaluated, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)
	r.Equal(1, evaluated)

	open := e.activeBurnIncidents(objective.UID)
	r.Len(open, 1)
	r.Equal(models.IncidentKindSLOBurn, open[0].Kind)
	r.Equal(policy.UID, *open[0].SLOAlertPolicyUID)
	r.Equal(objective.UID, *open[0].SLOUID)
	// The routing anchor is the SLO's own check, so channel and escalation
	// resolution behave exactly as they do for a check incident.
	r.Equal(e.check.UID, open[0].CheckUID)
	r.NotNil(open[0].Title)
	r.Contains(*open[0].Title, "Fast burn")
}

// TestBurnDoesNotFireWhenOnlyTheLongWindowIsOver is the multiwindow rule: a
// spike that has already stopped leaves the long window hot and the short one
// clean, and must not page.
func TestBurnDoesNotFireWhenOnlyTheLongWindowIsOver(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	// Bad half-hour that ended 30 minutes ago...
	e.seedRaw(now.Add(-30*time.Minute), 30*time.Minute, 30, 20, false)
	// ...followed by a clean half-hour, which is what the 5m window sees.
	e.seedRaw(now, 30*time.Minute, 30, 0, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)

	r.Empty(e.activeBurnIncidents(objective.UID))
}

// TestSparseDataDoesNotFabricateAnAlert: two failing probes in five minutes is
// a 100% error rate and an astronomic burn rate, but it is two probes. Below
// the sample floor the window is inconclusive and nothing fires.
func TestSparseDataDoesNotFabricateAnAlert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	policy := e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	minSamples := 10
	r.NoError(e.db.UpdateSLOAlertPolicy(
		t.Context(), policy.UID, models.SLOAlertPolicyUpdate{MinSamples: &minSamples},
	))

	now := time.Now()
	// Nine probes in the hour, all failing: a 100% error rate, one short of
	// the floor.
	e.seedRaw(now, 5*time.Minute, 9, 9, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)

	r.Empty(e.activeBurnIncidents(objective.UID))
}

// TestMaintenanceProbesDoNotPage: an SLO that excludes maintenance must not
// alert on a planned window, or every deploy night becomes a page.
func TestMaintenanceProbesDoNotPage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	r.True(objective.ExcludeMaintenance)
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	// A full hour of failures, every one tagged maintenance.
	e.seedRaw(now, time.Hour, 60, 60, true)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)

	r.Empty(e.activeBurnIncidents(objective.UID))
}

// TestBurnIncidentIsDeduplicated: while an incident is open the evaluator
// updates it rather than opening more, however many times it runs.
func TestBurnIncidentIsDeduplicated(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 30, false)

	for range 3 {
		_, err := e.svc.EvaluateBurnRates(context.Background(), now)
		r.NoError(err)
	}

	open := e.activeBurnIncidents(objective.UID)
	r.Len(open, 1)
}

// TestBurnPeakIsCarriedForward: the incident remembers the worst rate seen,
// which is the number a post-mortem asks for long after the rate has fallen.
func TestBurnPeakIsCarriedForward(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	// Everything fails: the worst possible rate for this objective.
	e.seedRaw(now, time.Hour, 60, 60, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)

	open := e.activeBurnIncidents(objective.UID)
	r.Len(open, 1)

	peak, ok := open[0].Details["burn_peak"].(float64)
	r.True(ok)
	r.InDelta(1000.0, peak, 1.0)

	// A milder second sweep: still firing, but the peak must not drop.
	e.seedRaw(now.Add(time.Second), 30*time.Second, 30, 6, false)

	_, err = e.svc.EvaluateBurnRates(context.Background(), now.Add(time.Second))
	r.NoError(err)

	open = e.activeBurnIncidents(objective.UID)
	r.Len(open, 1)

	peak, ok = open[0].Details["burn_peak"].(float64)
	r.True(ok)
	r.InDelta(1000.0, peak, 1.0)
}

// TestAutoResolveWaitsForAFullShortWindow is the hysteresis rule: the first
// cool sweep only starts the clock. Resolving on it would re-fire a minute
// later and page twice for one outage.
func TestAutoResolveWaitsForAFullShortWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	policy := e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	fired := time.Now().Add(-time.Hour)
	e.seedRaw(fired, time.Hour, 60, 30, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), fired)
	r.NoError(err)
	r.Len(e.activeBurnIncidents(objective.UID), 1)

	// A clean stretch that runs PAST `cooled`, so the short window still has
	// probes to look at on the later sweeps — an empty window is inconclusive,
	// not cool, and would keep the incident open forever.
	cooled := time.Now()
	e.seedRaw(cooled.Add(10*time.Minute), 70*time.Minute, 70, 0, false)

	_, err = e.svc.EvaluateBurnRates(context.Background(), cooled)
	r.NoError(err)

	// First cool sweep: the clock starts, the incident stays open.
	r.Len(e.activeBurnIncidents(objective.UID), 1)
	r.NotNil(e.reloadPolicy(policy.UID).BelowThresholdSince)

	// Still inside the 5-minute short window.
	_, err = e.svc.EvaluateBurnRates(context.Background(), cooled.Add(2*time.Minute))
	r.NoError(err)
	r.Len(e.activeBurnIncidents(objective.UID), 1)

	// Past a full short window: now it resolves.
	_, err = e.svc.EvaluateBurnRates(context.Background(), cooled.Add(6*time.Minute))
	r.NoError(err)
	r.Empty(e.activeBurnIncidents(objective.UID))
	r.Nil(e.reloadPolicy(policy.UID).BelowThresholdSince)
}

// TestManualResolveWhileStillBurningReopensOnNextEvaluation mirrors the check
// incident rule: a manual resolve is never revived, but a target that is still
// bad opens a fresh incident on the next pass.
func TestManualResolveWhileStillBurningReopensOnNextEvaluation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 30, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)

	open := e.activeBurnIncidents(objective.UID)
	r.Len(open, 1)
	first := open[0].UID

	_, err = e.incidents.ResolveIncident(context.Background(), e.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: first,
		Via:         "web",
	})
	r.NoError(err)
	r.Empty(e.activeBurnIncidents(objective.UID))

	// Still burning on the next sweep.
	_, err = e.svc.EvaluateBurnRates(context.Background(), now.Add(time.Minute))
	r.NoError(err)

	reopened := e.activeBurnIncidents(objective.UID)
	r.Len(reopened, 1)
	r.NotEqual(first, reopened[0].UID)
}

// TestDisabledPolicyIsNeverEvaluated: alerting is opt-in, so an untouched SLO
// on a freshly upgraded install must page nobody.
func TestDisabledPolicyIsNeverEvaluated(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 60, false)

	evaluated, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)
	r.Zero(evaluated)
	r.Empty(e.activeBurnIncidents(objective.UID))
}

// TestDisablingTheSLOStopsAlerting: turning the objective off must stop the
// paging, or "enabled" would not mean what an operator expects.
func TestDisablingTheSLOStopsAlerting(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	disabled := false
	r.NoError(e.db.UpdateSLO(t.Context(), objective.UID, models.SLOUpdate{Enabled: &disabled}))

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 60, false)

	evaluated, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)
	r.Zero(evaluated)
}

// TestBurnIncidentIsInvisibleToTheCheckStateMachine is the regression guard
// for the sharpest edge of reusing incidents: the routing anchor means a burn
// incident carries a real check_uid, and if the check-result lookup saw it the
// check would look permanently down.
func TestBurnIncidentIsInvisibleToTheCheckStateMachine(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 30, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)
	r.Len(e.activeBurnIncidents(objective.UID), 1)

	_, err = e.db.FindActiveIncidentByCheckUID(t.Context(), e.check.UID)
	r.Error(err, "the burn incident must not answer the check state machine's lookup")
}

// TestListPoliciesMaterializesBuiltInsForAnOlderSLO: an SLO created before
// this feature existed must answer exactly like a new one.
func TestListPoliciesMaterializesBuiltInsForAnOlderSLO(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	objective := models.NewSLO(e.org.UID, "legacy", "legacy", 99.9)
	objective.CheckUID = &e.check.UID
	r.NoError(e.db.CreateSLO(t.Context(), objective))

	policies, err := e.svc.ListPolicies(context.Background(), e.org.Slug, "legacy", time.Now())
	r.NoError(err)
	r.Len(policies, 2)

	kinds := map[string]sloalerts.PolicyResponse{}
	for _, policy := range policies {
		kinds[policy.Kind] = policy
	}

	fast := kinds[models.SLOAlertPolicyKindFast]
	r.False(fast.Enabled)
	r.InDelta(14.4, fast.Threshold, 0.001)
	r.Equal(3600, fast.LongWindowSeconds)
	r.Equal(300, fast.ShortWindowSeconds)
	r.Equal(models.SLOAlertSeverityCritical, fast.Severity)

	slow := kinds[models.SLOAlertPolicyKindSlow]
	r.InDelta(6.0, slow.Threshold, 0.001)
	r.Equal(6*3600, slow.LongWindowSeconds)
	r.Equal(1800, slow.ShortWindowSeconds)
	r.Equal(models.SLOAlertSeverityWarning, slow.Severity)
}

// TestUpdatePolicyRejectsAnUnorderedWindowPair: a short window longer than the
// long one makes the multiwindow rule meaningless, so it is a 400, not a 500
// from the CHECK constraint.
func TestUpdatePolicyRejectsAnUnorderedWindowPair(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()

	policies, err := e.db.ListSLOAlertPolicies(t.Context(), objective.UID)
	r.NoError(err)

	short := 99999

	_, err = e.svc.UpdatePolicy(
		context.Background(), e.org.Slug, objective.UID, policies[0].UID,
		sloalerts.UpdatePolicyRequest{ShortWindowSeconds: &short}, time.Now(),
	)
	r.ErrorIs(err, sloalerts.ErrInvalidWindows)
}

// TestUpdatePolicyClearsTheHysteresisClockOnRetune: the anchor was measured
// against the old threshold, so carrying it forward could auto-resolve on
// evidence gathered under a rule that no longer applies.
func TestUpdatePolicyClearsTheHysteresisClockOnRetune(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	policy := e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	anchor := time.Now().Add(-time.Minute)
	r.NoError(e.db.UpdateSLOAlertPolicy(
		t.Context(), policy.UID, models.SLOAlertPolicyUpdate{BelowThresholdSince: &anchor},
	))
	r.NotNil(e.reloadPolicy(policy.UID).BelowThresholdSince)

	threshold := 20.0

	_, err := e.svc.UpdatePolicy(
		context.Background(), e.org.Slug, objective.UID, policy.UID,
		sloalerts.UpdatePolicyRequest{Threshold: &threshold}, time.Now(),
	)
	r.NoError(err)
	r.Nil(e.reloadPolicy(policy.UID).BelowThresholdSince)
}

// TestUpdatePolicyRefusesAPolicyFromAnotherSLO: without the ownership check a
// policy UID guessed from elsewhere would be editable through any SLO's path.
func TestUpdatePolicyRefusesAPolicyFromAnotherSLO(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	first := e.seedSLO()

	second := models.NewSLO(e.org.UID, "other", "other", 99.9)
	second.CheckUID = &e.check.UID
	r.NoError(e.db.CreateSLO(t.Context(), second))
	_, err := e.slos.EnsureAlertPolicies(t.Context(), e.org.UID, second.UID)
	r.NoError(err)

	foreign, err := e.db.ListSLOAlertPolicies(t.Context(), second.UID)
	r.NoError(err)

	enabled := true

	_, err = e.svc.UpdatePolicy(
		context.Background(), e.org.Slug, first.UID, foreign[0].UID,
		sloalerts.UpdatePolicyRequest{Enabled: &enabled}, time.Now(),
	)
	r.ErrorIs(err, sloalerts.ErrPolicyNotFound)
}

// TestSLOListReportsBurning: the list badge is derived from the incident rows,
// so it cannot go stale when somebody resolves the incident by hand.
func TestSLOListReportsBurning(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	rows, err := e.slos.ListSLOs(t.Context(), e.org.Slug, "", 10)
	r.NoError(err)
	r.Len(rows, 1)
	r.False(rows[0].Burning)

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 30, false)

	_, err = e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)

	rows, err = e.slos.ListSLOs(t.Context(), e.org.Slug, "", 10)
	r.NoError(err)
	r.Len(rows, 1)
	r.True(rows[0].Burning)
}

// TestFireAndResolveEmitAuditEvents: the burn alert rides the existing
// incident.* event types rather than inventing its own, so the incident
// timeline, the resolution notice and every channel sender keep working — but
// each event has to carry the burn payload, or the audit trail cannot tell a
// burn alert from an outage after the fact.
func TestFireAndResolveEmitAuditEvents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	fired := time.Now().Add(-time.Hour)
	e.seedRaw(fired, time.Hour, 60, 30, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), fired)
	r.NoError(err)

	open := e.activeBurnIncidents(objective.UID)
	r.Len(open, 1)

	created := e.eventsFor(open[0].UID, models.EventTypeIncidentCreated)
	r.Len(created, 1)
	r.Equal(models.ActorTypeSystem, created[0].ActorType)
	r.Equal(objective.UID, created[0].Payload["slo_uid"])
	// check_uid is load-bearing: emitEvent refuses to page without it, so a
	// burn incident that lost its routing anchor would silently notify nobody.
	r.Equal(e.check.UID, created[0].Payload["check_uid"])
	r.Contains(created[0].Payload, "burn_rate_long")
	r.Contains(created[0].Payload, "budget_remaining_seconds")

	cooled := time.Now()
	e.seedRaw(cooled.Add(10*time.Minute), 70*time.Minute, 70, 0, false)

	for _, at := range []time.Time{cooled, cooled.Add(6 * time.Minute)} {
		_, evalErr := e.svc.EvaluateBurnRates(context.Background(), at)
		r.NoError(evalErr)
	}

	r.Empty(e.activeBurnIncidents(objective.UID))

	resolved := e.eventsFor(open[0].UID, models.EventTypeIncidentResolved)
	r.Len(resolved, 1)
	r.Equal(objective.UID, resolved[0].Payload["slo_uid"])
	r.Contains(resolved[0].Payload, "duration_seconds")
}

// eventsFor returns one incident's events of a given type.
func (e *env) eventsFor(incidentUID string, eventType models.EventType) []*models.Event {
	e.t.Helper()

	events, err := e.db.ListEvents(e.t.Context(), &models.ListEventsFilter{
		OrganizationUID: e.org.UID,
		IncidentUID:     &incidentUID,
		EventTypes:      []models.EventType{eventType},
		Limit:           50,
	})
	require.NoError(e.t, err)

	return events
}

// TestBurnAlertPagesThroughTheOrdinaryChannelFanOut is the load-bearing claim
// of this whole design: because firing goes through the existing incidents
// service, the objective's routing anchor's channels are notified with no new
// alerting path. If this regresses, burn alerts become silent.
func TestBurnAlertPagesThroughTheOrdinaryChannelFanOut(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	conn := models.NewIntegration(e.org.UID, models.ConnectionTypeWebhook, "ops")
	conn.Enabled = true
	r.NoError(e.db.CreateChannel(t.Context(), conn))
	r.NoError(e.db.CreateCheckConnection(
		t.Context(), models.NewCheckConnection(e.check.UID, conn.UID, e.org.UID),
	))

	objective := e.seedSLO()
	e.enablePolicy(objective.UID, models.SLOAlertPolicyKindFast)

	now := time.Now()
	e.seedRaw(now, time.Hour, 60, 30, false)

	_, err := e.svc.EvaluateBurnRates(context.Background(), now)
	r.NoError(err)
	r.Len(e.activeBurnIncidents(objective.UID), 1)

	var jobs []*models.Job

	r.NoError(e.db.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", e.org.UID).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("deleted_at IS NULL").
		Scan(t.Context()))

	r.NotEmpty(jobs, "a burn alert must fan out to the anchor check's channels")
}
