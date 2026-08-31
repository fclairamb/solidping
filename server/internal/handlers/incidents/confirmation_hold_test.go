package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// The confirmation hold (spec 2026-08-31-06).
//
// The RabbitMQ nonprod outage of 2026-08-30 produced 5 Slack reports where 1
// was correct. Parent and children were configured IDENTICALLY (period 60s,
// confirmation 120s) — so the tempting static invariants
// `parent.period <= child.period` and `parent.confirmation <= child.confirmation`
// both HELD, and four children still paged 8-26 s ahead of the parent. The gap
// came from probe phase offset plus the parent's ~15s connect timeout, and the
// equal confirmation windows preserved it instead of closing it.
//
// The fix is dynamic: a child whose confirmation has elapsed does not open
// while a hard ancestor is itself still `validating`. Every case below drives
// the REAL state machine (ProcessCheckResult) against an injected clock, so
// the ordering is deterministic rather than raced.
//
// portConfirmationHoldPG is distinct from every other embedded-Postgres port
// claimed in the repo.
const portConfirmationHoldPG = 15505

// holdSetup is the dialect-agnostic fixture: everything below runs unchanged
// against in-memory SQLite and real Postgres.
type holdSetup struct {
	svc   *incidents.Service
	dbSvc db.Service
	clk   *clock.Fake
	org   *models.Organization
	base  time.Time
}

func newHoldSetup(t *testing.T, dbSvc db.Service, orgSlug string) *holdSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	base := time.Date(2026, 8, 30, 23, 20, 0, 0, time.UTC)
	clk := clock.NewFake(base)
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization(orgSlug, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &holdSetup{svc: svc, dbSvc: dbSvc, clk: clk, org: org, base: base}
}

// check creates an enabled check with the production-shaped defaults from the
// outage: 60s period, 120s confirmation, 0s recovery (so a single success
// resolves and the reopen case stays readable).
func (s *holdSetup) check(t *testing.T, slug string, tune func(*models.Check)) *models.Check {
	t.Helper()

	check := models.NewCheck(s.org.UID, slug, "tcp")
	check.Status = models.CheckStatusUp
	check.Period = timeutils.Duration(60 * time.Second)
	check.ConfirmationPeriodSeconds = 120
	check.RecoveryPeriodSeconds = 0

	if tune != nil {
		tune(check)
	}

	require.NoError(t, s.dbSvc.CreateCheck(t.Context(), check))

	return check
}

func (s *holdSetup) edge(t *testing.T, parent, child *models.Check, kind models.CheckDependencyKind) {
	t.Helper()

	require.NoError(t, s.dbSvc.CreateCheckDependency(t.Context(),
		models.NewCheckDependency(s.org.UID, parent.UID, child.UID, kind, nil)))
}

func (s *holdSetup) hardEdge(t *testing.T, parent, child *models.Check) {
	t.Helper()
	s.edge(t, parent, child, models.CheckDependencyKindHard)
}

func (s *holdSetup) softEdge(t *testing.T, parent, child *models.Check) {
	t.Helper()
	s.edge(t, parent, child, models.CheckDependencyKindSoft)
}

// at advances the fake clock to base+offset. Every scenario states its
// timeline in offsets from the outage onset, which is how the incident
// timelines in the spec read.
func (s *holdSetup) at(offset time.Duration) {
	if delta := s.base.Add(offset).Sub(s.clk.Now()); delta > 0 {
		s.clk.Advance(delta)
	}
}

func (s *holdSetup) result(t *testing.T, check *models.Check, status models.ResultStatus) *models.Check {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	res := models.NewResult(s.org.UID, check.UID, status, 12)
	res.PeriodStart = s.clk.Now()

	if status != models.ResultStatusUp {
		res.Output["error"] = "connection refused"
	}

	r.NoError(s.dbSvc.CreateResult(ctx, res))
	r.NoError(s.svc.ProcessCheckResult(ctx, check, res))

	reloaded, err := s.dbSvc.GetCheck(ctx, s.org.UID, check.UID)
	r.NoError(err)

	return reloaded
}

func (s *holdSetup) fail(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	return s.result(t, check, models.ResultStatusDown)
}

func (s *holdSetup) succeed(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	return s.result(t, check, models.ResultStatusUp)
}

// freezeValidating forces a check into the wedged state the hold cap exists
// for: `validating` with an armed clock and no further results (paused
// mid-confirmation, dead region, stuck maintenance window).
func (s *holdSetup) freezeValidating(t *testing.T, check *models.Check, firstFailureAt time.Time) {
	t.Helper()

	ff := firstFailureAt
	require.NoError(t, s.dbSvc.UpdateCheckStatusAndClocks(
		t.Context(), check.UID, models.CheckStatusValidating, 1, &ff,
		models.IncidentClockUpdate{FirstFailureAt: &ff},
	))
}

func (s *holdSetup) activeIncident(t *testing.T, check *models.Check) *models.Incident {
	t.Helper()

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), check.UID)
	require.NoError(t, err)
	require.NotNil(t, inc, "expected an active incident for %s", check.UID)

	return inc
}

func (s *holdSetup) noActiveIncident(t *testing.T, check *models.Check) {
	t.Helper()

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), check.UID)
	if err == nil {
		require.Nil(t, inc, "expected no active incident for %s", check.UID)
	}
}

// rolledUpEvents is the fingerprint of a page that ALREADY WENT OUT: the
// retroactive forward walk only ever fires for a child that opened
// un-suppressed and had to be re-attributed afterwards. Every held case below
// asserts this is empty — that is the difference between "one report" and
// "five reports and a retraction".
func (s *holdSetup) rolledUpEvents(t *testing.T, incident *models.Incident) []*models.Event {
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

// createdEvents returns the incident.created events for one incident.
func (s *holdSetup) createdEvents(t *testing.T, incident *models.Incident) []*models.Event {
	t.Helper()

	uid := incident.UID
	events, err := s.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		IncidentUID:     &uid,
		EventTypes:      []models.EventType{models.EventTypeIncidentCreated},
	})
	require.NoError(t, err)

	return events
}

func (s *holdSetup) reload(t *testing.T, incident *models.Incident) *models.Incident {
	t.Helper()

	fresh, err := s.dbSvc.GetIncident(t.Context(), s.org.UID, incident.UID)
	require.NoError(t, err)

	return fresh
}

// holdCase is one scenario in the table. Each one is written against the
// dialect-agnostic fixture so the SQLite and Postgres runners share every
// assertion.
type holdCase struct {
	name string
	run  func(t *testing.T, s *holdSetup)
}

func holdCases() []holdCase {
	return []holdCase{
		{name: "Core_HeldChildNeverPages", run: caseCoreHeldChildNeverPages},
		{name: "PositiveControl_HealthyParentDoesNotGate", run: casePositiveControlHealthyParent},
		{name: "ParentBlip_ReleasesTheHold", run: caseParentBlipReleasesHold},
		{name: "CapExpiry_WedgedParentStopsGating", run: caseCapExpiryWedgedParent},
		{name: "Chain_TwoLevelsHoldForTheRoot", run: caseChainTwoLevels},
		{name: "SoftParentNeverGates", run: caseSoftParentNeverGates},
		{name: "NoParentsUnchanged", run: caseNoParentsUnchanged},
		{name: "Reopen_HeldThenSuppressed", run: caseReopenHeldThenSuppressed},
		{name: "StatusCoherence_StaysValidatingWhileHeld", run: caseStatusCoherence},
	}
}

// 1. Core. The outage, replayed: the child's health endpoint dies first, the
// parent's own probe notices 26 s later, and both wait the same 120 s. With
// the hold in place the child never opens ahead of the parent, so the only
// page is the parent's — and crucially there is NO incident.rolled_up event,
// because nothing had to be walked back.
func caseCoreHeldChildNeverPages(t *testing.T, s *holdSetup) {
	r := require.New(t)

	parent := s.check(t, "rabbitmq-aws-nonprod", nil)
	child := s.check(t, "consumer-api", nil)
	s.hardEdge(t, parent, child)

	// T+0 — the dependent's health endpoint flips to 503.
	s.at(0)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status)

	// T+26s — the parent's probe finally observes its first failure (phase
	// offset plus its own connect timeout).
	s.at(26 * time.Second)
	parent = s.fail(t, parent)
	r.Equal(models.CheckStatusValidating, parent.Status)

	// T+120s — the child's confirmation has elapsed. Without the hold this is
	// exactly where a page escaped.
	s.at(120 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status,
		"a held child must stay validating, never down-without-incident")
	s.noActiveIncident(t, child)

	// T+146s — the parent confirms and pages.
	s.at(146 * time.Second)
	parent = s.fail(t, parent)
	r.Equal(models.CheckStatusDown, parent.Status)

	parentInc := s.activeIncident(t, parent)
	r.False(parentInc.PagingSuppressed, "the parent is the one incident that should page")

	// T+180s — the child's next failing result. The ancestor is Down now, so
	// the gate releases and the backward walk suppresses the open synchronously.
	s.at(180 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status)

	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed, "the child must open already suppressed")
	r.NotNil(childInc.CausedByIncidentUID)
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)

	// Event-level proof: the child announced its open exactly once, and it was
	// never retroactively rolled up — no page had to be walked back.
	r.Len(s.createdEvents(t, childInc), 1)
	r.Empty(s.rolledUpEvents(t, childInc),
		"a rolled_up event means a page already went out; the hold must make it impossible")
	r.Empty(s.rolledUpEvents(t, parentInc))

	// Exactly one paged incident: the parent's.
	r.False(s.reload(t, parentInc).PagingSuppressed)
	r.True(s.reload(t, childInc).PagingSuppressed)
}

// 2. POSITIVE CONTROL. Same topology, healthy parent. If the gate fired here
// the whole feature would be a permanent latency tax — and a green run of the
// case above would prove nothing. The child must open and page at exactly its
// own configured confirmation.
func casePositiveControlHealthyParent(t *testing.T, s *holdSetup) {
	r := require.New(t)

	parent := s.check(t, "rabbitmq-healthy", nil)
	child := s.check(t, "consumer-healthy", nil)
	s.hardEdge(t, parent, child)

	// The parent is demonstrably up, and stays up.
	s.at(0)
	parent = s.succeed(t, parent)
	r.Equal(models.CheckStatusUp, parent.Status)

	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status)
	s.noActiveIncident(t, child)

	// One second before the confirmation elapses: still validating, on its own
	// account (not because of any gate).
	s.at(119 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status)
	s.noActiveIncident(t, child)

	// T+120s: exactly its configured confirmation. It opens, and it pages.
	s.at(120 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status)

	childInc := s.activeIncident(t, child)
	r.False(childInc.PagingSuppressed,
		"a child failing alone must page at its own confirmation — no latency tax")
	r.Nil(childInc.CausedByIncidentUID)
	r.Len(s.createdEvents(t, childInc), 1)
}

// 3. Parent blip. The parent recovers during the hold; the child is still
// failing, and its failure is now demonstrably its own. The gate releases and
// the child opens UN-suppressed.
func caseParentBlipReleasesHold(t *testing.T, s *holdSetup) {
	r := require.New(t)

	parent := s.check(t, "rabbitmq-blip", nil)
	child := s.check(t, "consumer-blip", nil)
	s.hardEdge(t, parent, child)

	s.at(0)
	child = s.fail(t, child)

	s.at(26 * time.Second)
	parent = s.fail(t, parent)
	r.Equal(models.CheckStatusValidating, parent.Status)

	s.at(120 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status, "held while the parent is validating")
	s.noActiveIncident(t, child)

	// T+130s — the parent's blip is over.
	s.at(130 * time.Second)
	parent = s.succeed(t, parent)
	r.Equal(models.CheckStatusUp, parent.Status)

	// T+180s — the child is still down, and nothing is gating it any more.
	s.at(180 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status)

	childInc := s.activeIncident(t, child)
	r.False(childInc.PagingSuppressed, "the parent recovered; this failure is the child's own")
	r.Nil(childInc.CausedByIncidentUID)
}

// 4. Cap expiry. A parent frozen in `validating` — paused, dead region, stuck
// maintenance window — must not be able to hold a child forever. The hold cap
// (`FirstFailureAt + confirmation + period + timeout`) bounds it to one window
// without a single extra query about the parent's state.
//
// The fixture doubles as its own control: the SAME wedged parent gates before
// the cap and stops gating after it, so a broken cap fails one half or the other.
func caseCapExpiryWedgedParent(t *testing.T, s *holdSetup) {
	r := require.New(t)

	parent := s.check(t, "rabbitmq-wedged", nil)
	child := s.check(t, "consumer-wedged", nil)
	s.hardEdge(t, parent, child)

	// The parent stopped producing results at T+0, stuck mid-confirmation.
	// Cap = 0 + 120s (confirmation) + 60s (period) + 15s (default timeout)
	//     = T+195s.
	s.at(0)
	s.freezeValidating(t, parent, s.base)
	child = s.fail(t, child)

	// T+120s — inside the cap: still held.
	s.at(120 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status)
	s.noActiveIncident(t, child)

	// T+194s — one second inside the cap: still held.
	s.at(194 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status)
	s.noActiveIncident(t, child)

	// T+195s — the cap expires. The parent is treated as wedged and stops
	// gating; the child opens and pages on its own.
	s.at(195 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status)

	childInc := s.activeIncident(t, child)
	r.False(childInc.PagingSuppressed,
		"past the cap a wedged parent must not suppress — nor keep holding")
	r.Nil(childInc.CausedByIncidentUID)
}

// 5. Chain. `db -> api -> child`, all hard. While the db is validating BOTH
// descendants hold; when the db confirms, each opens already suppressed under
// it. One page for a three-check cascade.
func caseChainTwoLevels(t *testing.T, s *holdSetup) {
	r := require.New(t)

	// The root confirms slowest — the shape that produced the outage.
	database := s.check(t, "postgres-primary", func(c *models.Check) {
		c.ConfirmationPeriodSeconds = 180
	})
	api := s.check(t, "api-gateway", nil)
	child := s.check(t, "web-frontend", nil)
	s.hardEdge(t, database, api)
	s.hardEdge(t, api, child)

	s.at(0)
	database = s.fail(t, database)
	api = s.fail(t, api)
	child = s.fail(t, child)

	// T+120s — api and child have both confirmed, the db has not (180s).
	// Cap for the db = 0 + 180 + 60 + 15 = T+255s, so both hold.
	s.at(120 * time.Second)
	api = s.fail(t, api)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, api.Status)
	r.Equal(models.CheckStatusValidating, child.Status)
	s.noActiveIncident(t, api)
	s.noActiveIncident(t, child)

	// T+180s — the db confirms and pages.
	s.at(180 * time.Second)
	database = s.fail(t, database)
	dbInc := s.activeIncident(t, database)
	r.False(dbInc.PagingSuppressed)

	// T+185s / T+190s — the descendants open, both already suppressed. The
	// api's own incident is suppressed, so the child attaches to the db two
	// levels up rather than to a silent parent.
	s.at(185 * time.Second)
	api = s.fail(t, api)

	s.at(190 * time.Second)
	child = s.fail(t, child)

	apiInc := s.activeIncident(t, api)
	childInc := s.activeIncident(t, child)

	r.True(apiInc.PagingSuppressed)
	r.Equal(dbInc.UID, *apiInc.CausedByIncidentUID)
	r.True(childInc.PagingSuppressed)
	r.Equal(dbInc.UID, *childInc.CausedByIncidentUID)

	// Nothing was walked back: no page ever escaped.
	r.Empty(s.rolledUpEvents(t, apiInc))
	r.Empty(s.rolledUpEvents(t, childInc))
	r.Empty(s.rolledUpEvents(t, dbInc))
}

// 6a. Soft edges are never consulted, for rollup parity: a soft parent
// mid-confirmation changes nothing about the child.
func caseSoftParentNeverGates(t *testing.T, s *holdSetup) {
	r := require.New(t)

	parent := s.check(t, "cdn-soft", nil)
	child := s.check(t, "site-soft", nil)
	s.softEdge(t, parent, child)

	s.at(0)
	child = s.fail(t, child)
	parent = s.fail(t, parent)
	r.Equal(models.CheckStatusValidating, parent.Status)

	s.at(120 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status)

	childInc := s.activeIncident(t, child)
	r.False(childInc.PagingSuppressed, "a soft parent must neither gate nor suppress")
	r.Nil(childInc.CausedByIncidentUID)
}

// 6b. No parents at all: byte-identical to the behavior before the gate.
func caseNoParentsUnchanged(t *testing.T, s *holdSetup) {
	r := require.New(t)

	check := s.check(t, "standalone", nil)

	s.at(0)
	check = s.fail(t, check)
	r.Equal(models.CheckStatusValidating, check.Status)
	s.noActiveIncident(t, check)

	s.at(120 * time.Second)
	check = s.fail(t, check)
	r.Equal(models.CheckStatusDown, check.Status)

	inc := s.activeIncident(t, check)
	r.False(inc.PagingSuppressed)
	r.Nil(inc.CausedByIncidentUID)
}

// 7. Reopen. The gate sits BEFORE createOrReopenIncident, so a relapse inside
// the reopen cooldown is held by exactly the same rule as a first open — and
// then reopens suppressed once the parent confirms.
func caseReopenHeldThenSuppressed(t *testing.T, s *holdSetup) {
	r := require.New(t)

	// Confirmation 0 on the child: the relapse is instantaneous, which is the
	// case where a reopen could sneak past the gate.
	parent := s.check(t, "rabbitmq-reopen", nil)
	child := s.check(t, "consumer-reopen", func(c *models.Check) {
		c.ConfirmationPeriodSeconds = 0
	})
	s.hardEdge(t, parent, child)

	s.at(0)
	parent = s.succeed(t, parent)
	child = s.fail(t, child)

	firstInc := s.activeIncident(t, child)
	r.False(firstInc.PagingSuppressed, "positive control: the first open pages, parent is up")

	// T+10s — the child recovers; recovery period is 0 so the incident resolves.
	s.at(10 * time.Second)
	child = s.succeed(t, child)
	r.Equal(models.CheckStatusUp, child.Status)
	r.Equal(models.IncidentStateResolved, s.reload(t, firstInc).State)

	// T+20s — NOW the parent starts failing.
	s.at(20 * time.Second)
	parent = s.fail(t, parent)
	r.Equal(models.CheckStatusValidating, parent.Status)

	// T+30s — the child relapses well inside its reopen cooldown. Confirmation
	// is 0, so without the gate this would reopen and page immediately.
	s.at(30 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusValidating, child.Status,
		"a held reopen must keep the check validating, not down")
	s.noActiveIncident(t, child)
	r.Equal(models.IncidentStateResolved, s.reload(t, firstInc).State,
		"the reopen itself must be held, not merely un-paged")

	// T+140s — the parent confirms and pages.
	s.at(140 * time.Second)
	parent = s.fail(t, parent)
	parentInc := s.activeIncident(t, parent)
	r.False(parentInc.PagingSuppressed)

	// T+150s — the child's next failure reopens, suppressed under the parent.
	s.at(150 * time.Second)
	child = s.fail(t, child)

	reopened := s.activeIncident(t, child)
	r.Equal(firstInc.UID, reopened.UID, "the relapse must reattach, not mint a second incident")
	r.True(reopened.PagingSuppressed)
	r.NotNil(reopened.CausedByIncidentUID)
	r.Equal(parentInc.UID, *reopened.CausedByIncidentUID)
	r.Empty(s.rolledUpEvents(t, reopened))
}

// 8. Status coherence. `pickStatus` flips to `down` the moment the
// confirmation window elapses, which — with the open being held — would render
// a check as down with no incident behind it. Several consecutive held ticks
// must all read `validating`.
func caseStatusCoherence(t *testing.T, s *holdSetup) {
	r := require.New(t)

	parent := s.check(t, "rabbitmq-status", nil)
	child := s.check(t, "consumer-status", nil)
	s.hardEdge(t, parent, child)

	s.at(0)
	child = s.fail(t, child)
	parent = s.fail(t, parent)

	// The parent's cap is T+195s; every child tick from T+120s to T+180s is held.
	for _, offset := range []time.Duration{120, 140, 160, 180} {
		s.at(offset * time.Second)
		child = s.fail(t, child)

		r.Equal(models.CheckStatusValidating, child.Status,
			"held at T+%s: the visible status must agree with the deferred open", offset*time.Second)
		s.noActiveIncident(t, child)
	}

	// The parent is the one holding: it has stayed `validating` the whole
	// time because no further result was pushed for it.
	r.Equal(models.CheckStatusValidating, parent.Status)

	// And it is a genuine hold, not a wedged check: the moment the parent's
	// own confirmation lands, the child follows on its very next result.
	s.at(185 * time.Second)
	parent = s.fail(t, parent)
	r.Equal(models.CheckStatusDown, parent.Status)

	s.at(190 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status,
		"once the parent is down the hold releases immediately")

	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed)
	r.Empty(s.rolledUpEvents(t, childInc))
}

// TestConfirmationHold_SQLite runs the whole table on in-memory SQLite.
func TestConfirmationHold_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range holdCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := require.New(t)

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			tc.run(t, newHoldSetup(t, dbSvc, "acme"))
		})
	}
}

// TestConfirmationHold_Postgres runs the identical table against real
// Postgres. The gate's BFS reads check_dependencies and checks through the
// same bun query builder on both dialects, but the confirmation clocks are
// timestamp comparisons and Postgres is the engine production runs on.
//
// Self-skips under `-short` (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring the other Postgres siblings in this package.
func TestConfirmationHold_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portConfirmationHoldPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	for i, tc := range holdCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Sequential: one embedded server, one org per case.
			tc.run(t, newHoldSetup(t, dbSvc, orgSlugForCase(i)))
		})
	}
}

// orgSlugForCase gives each Postgres sub-case its own org so the shared
// embedded database keeps the cases isolated (check slugs are unique per org).
func orgSlugForCase(index int) string {
	return "hold-pg-" + string(rune('a'+index))
}
