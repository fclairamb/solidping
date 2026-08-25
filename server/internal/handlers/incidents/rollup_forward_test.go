package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// The other half of the RabbitMQ outage of 2026-08-23 (spec 2026-08-24-15).
//
// Cascade rollup was evaluated exactly once, at child-incident creation,
// looking BACKWARD. The parent's own probe polls the management console and
// confirmed at 23:50:14 — after 55 dependents' health endpoints had already
// died and opened their own incidents. Every one of them paged for a cause
// that was, by then, perfectly well known.
//
// These tests drive the REAL state machine (ProcessCheckResult) against an
// in-memory SQLite and an injected clock, so every correlation-window
// assertion is deterministic instead of racing time.Now().

type rollupSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	clk   *clock.Fake
	org   *models.Organization
}

func newRollupSetup(t *testing.T) *rollupSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	clk := clock.NewFake(time.Date(2026, 8, 23, 23, 40, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &rollupSetup{svc: svc, dbSvc: dbSvc, clk: clk, org: org}
}

// check creates an enabled check that opens an incident on the first failing
// result. Default period is 1 minute, so correlationWindow clamps to the
// 5-minute floor for every check here — the window is stated explicitly in
// each test that depends on it.
func (s *rollupSetup) check(t *testing.T, slug string) *models.Check {
	t.Helper()

	return s.checkWith(t, slug, nil)
}

// checkWith is the same, with a hook to adjust the check before it is stored.
func (s *rollupSetup) checkWith(t *testing.T, slug string, tune func(*models.Check)) *models.Check {
	t.Helper()

	check := models.NewCheck(s.org.UID, slug, "tcp")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0

	if tune != nil {
		tune(check)
	}

	require.NoError(t, s.dbSvc.CreateCheck(t.Context(), check))

	return check
}

// hardEdge / softEdge wire the dependency graph.
func (s *rollupSetup) edge(t *testing.T, parent, child *models.Check, kind models.CheckDependencyKind) {
	t.Helper()

	require.NoError(t, s.dbSvc.CreateCheckDependency(t.Context(),
		models.NewCheckDependency(s.org.UID, parent.UID, child.UID, kind, nil)))
}

func (s *rollupSetup) hardEdge(t *testing.T, parent, child *models.Check) {
	t.Helper()
	s.edge(t, parent, child, models.CheckDependencyKindHard)
}

// fail pushes one failing result through the real state machine at the
// current fake-clock instant.
func (s *rollupSetup) fail(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	result := models.NewResult(s.org.UID, check.UID, models.ResultStatusDown, 12)
	result.PeriodStart = s.clk.Now()
	result.Output["error"] = "connection refused"
	r.NoError(s.dbSvc.CreateResult(ctx, result))
	r.NoError(s.svc.ProcessCheckResult(ctx, check, result))

	reloaded, err := s.dbSvc.GetCheck(ctx, s.org.UID, check.UID)
	r.NoError(err)

	return reloaded
}

// succeed pushes one passing result through the real state machine.
func (s *rollupSetup) succeed(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	result := models.NewResult(s.org.UID, check.UID, models.ResultStatusUp, 12)
	result.PeriodStart = s.clk.Now()
	r.NoError(s.dbSvc.CreateResult(ctx, result))
	r.NoError(s.svc.ProcessCheckResult(ctx, check, result))

	reloaded, err := s.dbSvc.GetCheck(ctx, s.org.UID, check.UID)
	r.NoError(err)

	return reloaded
}

func (s *rollupSetup) activeIncident(t *testing.T, check *models.Check) *models.Incident {
	t.Helper()

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), check.UID)
	require.NoError(t, err)
	require.NotNil(t, inc, "expected an active incident for %s", check.UID)

	return inc
}

func (s *rollupSetup) reload(t *testing.T, incident *models.Incident) *models.Incident {
	t.Helper()

	fresh, err := s.dbSvc.GetIncident(t.Context(), s.org.UID, incident.UID)
	require.NoError(t, err)

	return fresh
}

// rolledUpEvents returns the incident.rolled_up events recorded for one
// incident — the observability half of the spec, and the thing that must not
// double-fire when both evaluations run.
func (s *rollupSetup) rolledUpEvents(t *testing.T, incident *models.Incident) []*models.Event {
	t.Helper()

	uid := incident.UID
	events, err := s.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		IncidentUID:     &uid,
		EventTypes:      []models.EventType{models.EventTypeIncidentRolledUp},
	})
	require.NoError(t, err)

	return events
}

// TestLateConfirmingParentSuppressesAlreadyOpenChild is the headline case and
// a genuine positive control: with the backward-only implementation the child
// opens while the parent is still UP, nothing ever revisits it, and
// paging_suppressed stays false for the whole incident. The first assertion
// below pins that starting state explicitly, so the test cannot pass by
// accident on a fixture where the child was suppressed at creation.
func TestLateConfirmingParentSuppressesAlreadyOpenChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	child := s.check(t, "consumer-api")
	s.hardEdge(t, parent, child)

	// 23:40 — the consumer's health endpoint dies first. The parent is still
	// UP: its own probe has not even seen a failure yet.
	child = s.fail(t, child)

	childInc := s.activeIncident(t, child)
	r.False(childInc.PagingSuppressed,
		"positive control: at open time there is no parent incident to roll up under")
	r.Nil(childInc.CausedByIncidentUID)
	r.Empty(s.rolledUpEvents(t, childInc))

	// 23:42 — the parent finally confirms, well inside the child's 5-minute
	// correlation window.
	s.clk.Advance(2 * time.Minute)
	parent = s.fail(t, parent)

	parentInc := s.activeIncident(t, parent)

	childInc = s.reload(t, childInc)
	r.True(childInc.PagingSuppressed,
		"a parent that confirms after its dependent must still suppress it")
	r.NotNil(childInc.CausedByIncidentUID)
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)

	// The timeline says so, at depth 1.
	events := s.rolledUpEvents(t, childInc)
	r.Len(events, 1, "exactly one retroactive-rollup event")
	r.Equal(parentInc.UID, events[0].Payload["parent_incident_uid"])
	r.Equal(parent.UID, events[0].Payload["parent_check_uid"])

	// The parent itself is untouched — it is the one that should page.
	r.False(s.reload(t, parentInc).PagingSuppressed)
}

// TestParentBeforeChildStillRollsUpBackward guards the pre-existing ordering:
// the backward walk at child-open must keep working, and must NOT start
// emitting a retroactive-rollup event for an attachment that happened at
// creation time (nothing changed mid-flight, so there is nothing to announce).
func TestParentBeforeChildStillRollsUpBackward(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	child := s.check(t, "consumer-api")
	s.hardEdge(t, parent, child)

	parent = s.fail(t, parent)
	parentInc := s.activeIncident(t, parent)

	s.clk.Advance(2 * time.Minute)
	child = s.fail(t, child)

	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed, "the backward walk still suppresses")
	r.NotNil(childInc.CausedByIncidentUID)
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)
	r.Empty(s.rolledUpEvents(t, childInc),
		"suppression decided at open time is not a retroactive rollup")
}

// TestSimultaneousOnsetAttachesExactlyOnce covers the race the spec calls out:
// parent-open and child-open evaluating from two workers at the same instant.
// Both bounds are inclusive, so the attachment must happen — and running the
// forward walk a second time (standing in for the other worker's evaluation
// arriving late) must not produce a second attachment or a second event. The
// guard lives in the UPDATE's WHERE clause.
func TestSimultaneousOnsetAttachesExactlyOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	child := s.check(t, "consumer-api")
	s.hardEdge(t, parent, child)

	// Same clock instant for both onsets — the clock is never advanced.
	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)
	r.False(childInc.PagingSuppressed)

	parent = s.fail(t, parent)
	parentInc := s.activeIncident(t, parent)

	r.Equal(parentInc.StartedAt.UTC(), childInc.StartedAt.UTC(),
		"fixture must really be a same-instant race")

	childInc = s.reload(t, childInc)
	r.True(childInc.PagingSuppressed, "inclusive bounds: a same-instant parent still counts")
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)
	r.Len(s.rolledUpEvents(t, childInc), 1)

	// The other worker's evaluation lands a moment later.
	s.svc.RollUpExistingChildrenForTest(ctx, parent, parentInc, parentInc.StartedAt)

	childInc = s.reload(t, childInc)
	r.True(childInc.PagingSuppressed)
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)
	r.Len(s.rolledUpEvents(t, childInc), 1,
		"the compare-and-set makes the second evaluation a no-op, not a second event")
}

// TestForwardRollupWalksChainsDeeperThanOneLevel proves the BFS really
// descends: the intermediate node never fails at all, so the only way the
// grandchild is reached is by walking through it.
func TestForwardRollupWalksChainsDeeperThanOneLevel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	db := s.check(t, "postgres-primary")
	api := s.check(t, "api")
	dashboard := s.check(t, "dashboard")
	s.hardEdge(t, db, api)
	s.hardEdge(t, api, dashboard)

	dashboard = s.fail(t, dashboard)
	dashInc := s.activeIncident(t, dashboard)
	r.False(dashInc.PagingSuppressed)

	s.clk.Advance(90 * time.Second)
	db = s.fail(t, db)
	dbInc := s.activeIncident(t, db)

	// `api` never failed — no incident exists for it, so the only way to reach
	// the dashboard is to walk THROUGH it.
	apiIncidents, err := s.dbSvc.CountActiveIncidentsByCheckUID(t.Context(), api.UID)
	r.NoError(err)
	r.Zero(apiIncidents)

	dashInc = s.reload(t, dashInc)
	r.True(dashInc.PagingSuppressed, "a depth-2 descendant must be reached")
	r.Equal(dbInc.UID, *dashInc.CausedByIncidentUID)

	events := s.rolledUpEvents(t, dashInc)
	r.Len(events, 1)
	r.EqualValues(2, events[0].Payload["rollup_depth"])
}

// TestForwardRollupIgnoresSoftEdges — soft edges are documentation, not paging
// policy, in both directions.
func TestForwardRollupIgnoresSoftEdges(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	child := s.check(t, "reporting-batch")
	s.edge(t, parent, child, models.CheckDependencyKindSoft)

	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)

	s.clk.Advance(2 * time.Minute)
	s.fail(t, parent)

	childInc = s.reload(t, childInc)
	r.False(childInc.PagingSuppressed, "a soft dependent keeps paging for itself")
	r.Nil(childInc.CausedByIncidentUID)
	r.Empty(s.rolledUpEvents(t, childInc))
}

// TestForwardRollupRespectsTheCorrelationWindow — the window is the child's
// own (max(2 * period, 5min) = 5min here). A child that has been down for
// twenty minutes is not somebody else's cascade.
func TestForwardRollupRespectsTheCorrelationWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	inWindow := s.check(t, "consumer-api")
	stale := s.check(t, "long-dead-consumer")
	s.hardEdge(t, parent, inWindow)
	s.hardEdge(t, parent, stale)

	stale = s.fail(t, stale)
	staleInc := s.activeIncident(t, stale)

	// 20 minutes of unrelated downtime, well past the 5-minute floor.
	s.clk.Advance(20 * time.Minute)
	inWindow = s.fail(t, inWindow)
	inWindowInc := s.activeIncident(t, inWindow)

	s.clk.Advance(time.Minute)
	parent = s.fail(t, parent)
	parentInc := s.activeIncident(t, parent)

	r.True(s.reload(t, inWindowInc).PagingSuppressed,
		"the recent dependent rolls up")
	r.Equal(parentInc.UID, *s.reload(t, inWindowInc).CausedByIncidentUID)

	staleInc = s.reload(t, staleInc)
	r.False(staleInc.PagingSuppressed,
		"a dependent that has been down for 21 minutes is outside its own window")
	r.Nil(staleInc.CausedByIncidentUID)
	r.Empty(s.rolledUpEvents(t, staleInc))
}

// TestParentResolveStillDetachesRecoveredChild pins the pre-existing
// parent-resolve re-evaluation against a child that was attached by the NEW
// forward path — the two halves have to compose. The child's check recovers
// before the parent's incident closes, so it must be detached silently rather
// than paged.
func TestParentResolveStillDetachesRecoveredChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	// A long recovery period keeps the child's incident ACTIVE after its check
	// flips back to UP, which is exactly the state reEvaluateChild inspects.
	child := s.checkWith(t, "consumer-api", func(c *models.Check) {
		c.RecoveryPeriodSeconds = 3600
	})
	s.hardEdge(t, parent, child)

	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)

	s.clk.Advance(time.Minute)
	parent = s.fail(t, parent)

	childInc = s.reload(t, childInc)
	r.True(childInc.PagingSuppressed, "attached by the forward walk")

	// The child recovers first; its incident stays open pending the recovery
	// period.
	s.clk.Advance(time.Minute)
	child = s.succeed(t, child)
	r.Equal(models.CheckStatusUp, child.Status)
	r.Equal(models.IncidentStateActive, s.reload(t, childInc).State)

	// Now the parent recovers and resolves.
	s.clk.Advance(time.Minute)
	parent = s.succeed(t, parent)
	r.Equal(models.CheckStatusUp, parent.Status)

	childInc = s.reload(t, childInc)
	r.False(childInc.PagingSuppressed, "suppression is cleared on parent resolve")
	r.Nil(childInc.CausedByIncidentUID,
		"a child that recovered along with its parent is detached, not paged")
}

// TestParentResolveUnsuppressesStillFailingChild is the other branch: the
// child is down for its own reason, so when the parent's incident closes it
// has to start paging.
func TestParentResolveUnsuppressesStillFailingChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := newRollupSetup(t)

	parent := s.check(t, "rabbitmq-aws-prod")
	child := s.check(t, "consumer-api")
	s.hardEdge(t, parent, child)

	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)

	s.clk.Advance(time.Minute)
	parent = s.fail(t, parent)

	r.True(s.reload(t, childInc).PagingSuppressed)

	// The parent recovers; the child is still failing.
	s.clk.Advance(time.Minute)
	s.fail(t, child)
	s.succeed(t, parent)

	childInc = s.reload(t, childInc)
	r.Equal(models.IncidentStateActive, childInc.State)
	r.False(childInc.PagingSuppressed,
		"the child's own outage must page once the cause it was attributed to is gone")
	r.NotNil(childInc.CausedByIncidentUID,
		"attribution is kept for the timeline; only the paging gate is released")
}
