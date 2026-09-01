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
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Rollup detach keeps the cascade's audit trail (spec
// 2026-08-31-07-rollup-detach-erases-attribution).
//
// Before this fix, markRollupDetached cleared BOTH paging_suppressed and
// caused_by_incident_uid the moment a suppressed child's check recovered
// ahead of its rollup parent — erasing the only record of which cascade the
// incident belonged to, and never emitting the incident.rollup_detached
// event the wiki already (wrongly) documented. Of the 11 dependent incidents
// in the RabbitMQ nonprod outage of 2026-08-30, 10 lost their attribution
// this way.
//
// Every case below drives the REAL state machine (ProcessCheckResult)
// against an injected clock, so the ordering is deterministic. Confirmation
// is 0 seconds throughout — the confirmation hold (spec 2026-08-31-06) is
// orthogonal to this spec and deliberately kept out of the way.
//
// portRollupDetachPG is distinct from every other embedded-Postgres port
// claimed in the repo.
const portRollupDetachPG = 15507

// detachSetup is the dialect-agnostic fixture: every case below runs
// unchanged against in-memory SQLite and real Postgres.
type detachSetup struct {
	svc   *incidents.Service
	dbSvc db.Service
	clk   *clock.Fake
	org   *models.Organization
	base  time.Time
}

func newDetachSetup(t *testing.T, dbSvc db.Service, orgSlug string) *detachSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	base := time.Date(2026, 8, 30, 23, 20, 0, 0, time.UTC)
	clk := clock.NewFake(base)
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization(orgSlug, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &detachSetup{svc: svc, dbSvc: dbSvc, clk: clk, org: org, base: base}
}

// check creates an enabled check that opens an incident on the very first
// failing result (0s confirmation) and never auto-resolves unless a success
// arrives (0s recovery by default — tune overrides it).
func (s *detachSetup) check(t *testing.T, slug string, tune func(*models.Check)) *models.Check {
	t.Helper()

	check := models.NewCheck(s.org.UID, slug, "tcp")
	check.Status = models.CheckStatusUp
	check.Period = timeutils.Duration(60 * time.Second)
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0

	if tune != nil {
		tune(check)
	}

	require.NoError(t, s.dbSvc.CreateCheck(t.Context(), check))

	return check
}

func (s *detachSetup) edge(t *testing.T, parent, child *models.Check, kind models.CheckDependencyKind) {
	t.Helper()

	require.NoError(t, s.dbSvc.CreateCheckDependency(t.Context(),
		models.NewCheckDependency(s.org.UID, parent.UID, child.UID, kind, nil)))
}

func (s *detachSetup) hardEdge(t *testing.T, parent, child *models.Check) {
	t.Helper()
	s.edge(t, parent, child, models.CheckDependencyKindHard)
}

// at advances the fake clock to base+offset. Every scenario states its
// timeline in offsets from the outage onset.
func (s *detachSetup) at(offset time.Duration) {
	if delta := s.base.Add(offset).Sub(s.clk.Now()); delta > 0 {
		s.clk.Advance(delta)
	}
}

func (s *detachSetup) result(t *testing.T, check *models.Check, status models.ResultStatus) *models.Check {
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

func (s *detachSetup) fail(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	return s.result(t, check, models.ResultStatusDown)
}

func (s *detachSetup) succeed(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	return s.result(t, check, models.ResultStatusUp)
}

func (s *detachSetup) activeIncident(t *testing.T, check *models.Check) *models.Incident {
	t.Helper()

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), check.UID)
	require.NoError(t, err)
	require.NotNil(t, inc, "expected an active incident for %s", check.UID)

	return inc
}

func (s *detachSetup) reload(t *testing.T, incident *models.Incident) *models.Incident {
	t.Helper()

	fresh, err := s.dbSvc.GetIncident(t.Context(), s.org.UID, incident.UID)
	require.NoError(t, err)

	return fresh
}

func (s *detachSetup) eventsOfType(
	t *testing.T, incident *models.Incident, eventType models.EventType,
) []*models.Event {
	t.Helper()

	uid := incident.UID
	events, err := s.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		IncidentUID:     &uid,
		EventTypes:      []models.EventType{eventType},
	})
	require.NoError(t, err)

	return events
}

// bindChannel attaches an enabled webhook connection to check, so "no
// notification queued" is a meaningful assertion rather than a vacuous one —
// there is a real channel behind the check that WOULD receive a job if the
// paging pipeline decided to fire.
func (s *detachSetup) bindChannel(t *testing.T, check *models.Check, name string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	conn := models.NewIntegration(s.org.UID, models.ConnectionTypeWebhook, name)
	conn.Enabled = true
	r.NoError(s.dbSvc.CreateChannel(ctx, conn))

	cc := models.NewCheckConnection(check.UID, conn.UID, s.org.UID)
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, cc))
}

// pendingNotificationJobs counts pending notification jobs for the org.
// Reads the jobs table directly rather than jobsvc.ListJobs, which uses a
// bun pattern that errors on the in-memory sqlite (see resolve_test.go for
// the same workaround).
func (s *detachSetup) pendingNotificationJobs(t *testing.T) int {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", s.org.UID).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	return len(jobs)
}

// detachCase is one scenario in the table. Each one is written against the
// dialect-agnostic fixture so the SQLite and Postgres runners share every
// assertion.
type detachCase struct {
	name string
	run  func(t *testing.T, s *detachSetup)
}

func detachCases() []detachCase {
	return []detachCase{
		{name: "Detach_RecoveredChild_KeepsAttributionAndEmitsEvent", run: caseDetachKeepsAttribution},
		{name: "Relapse_BeforeResolve_StillPages", run: caseRelapseBeforeResolveStillPages},
		{name: "StillDown_Unchanged_ReopensAndPages", run: caseStillDownUnchangedReopensAndPages},
		{name: "Reopen_PreviouslyDetached_NoFailingParent_AttributionCleared", run: caseReopenClearsAttribution},
	}
}

// 1. The core fix: a child whose check recovered ahead of its parent keeps
// caused_by_incident_uid on detach, flips paging_suppressed to false, emits
// incident.rollup_detached with the parent's identity in the payload, and
// never pages — even though a real channel is bound to the check.
func caseDetachKeepsAttribution(t *testing.T, s *detachSetup) {
	t.Helper()

	r := require.New(t)

	parent := s.check(t, "parent-detach", nil)
	child := s.check(t, "child-detach", func(c *models.Check) {
		// Long recovery window: the child's incident must stay ACTIVE
		// through the whole scenario, so detach — not an ordinary
		// resolve — is what's under test.
		c.RecoveryPeriodSeconds = 300
	})
	s.hardEdge(t, parent, child)
	s.bindChannel(t, child, "child-channel")

	// T+0 — parent opens first, unsuppressed.
	s.at(0)
	parent = s.fail(t, parent)
	parentInc := s.activeIncident(t, parent)
	r.False(parentInc.PagingSuppressed)

	// T+30s — child opens with the parent already down: the backward walk
	// suppresses it synchronously.
	s.at(30 * time.Second)
	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed)
	r.NotNil(childInc.CausedByIncidentUID)
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)

	// T+60s — the child's own check recovers. Status flips to up, but the
	// 300s recovery window keeps its incident active — this is precisely the
	// "child has recovered" branch reEvaluateChild inspects.
	s.at(60 * time.Second)
	child = s.succeed(t, child)
	r.Equal(models.CheckStatusUp, child.Status)
	r.Equal(models.IncidentStateActive, s.reload(t, childInc).State,
		"the recovery window has not elapsed — the incident stays open")

	// T+90s — the parent resolves, triggering reEvaluateRollupChildren.
	s.at(90 * time.Second)
	parent = s.succeed(t, parent)
	r.Equal(models.IncidentStateResolved, s.reload(t, parentInc).State)

	detached := s.reload(t, childInc)
	r.False(detached.PagingSuppressed, "paging_suppressed clears on detach")
	r.NotNil(detached.CausedByIncidentUID, "attribution must survive the detach")
	r.Equal(parentInc.UID, *detached.CausedByIncidentUID)
	r.Equal(models.IncidentStateActive, detached.State,
		"detaching does not itself resolve the child's incident")

	events := s.eventsOfType(t, childInc, models.EventTypeIncidentRollupDetached)
	r.Len(events, 1, "incident.rollup_detached must be emitted exactly once")
	r.Equal(parentInc.UID, events[0].Payload["parent_incident_uid"])
	r.Equal(parent.UID, events[0].Payload["parent_check_uid"])

	r.Zero(s.pendingNotificationJobs(t),
		"a detach must never page, even with a real channel bound to the check")
}

// 2. The preserved property (why the clearing existed in the first place): a
// detached child whose incident is still active can relapse before it
// resolves, and that relapse must page — nothing may re-read the KEPT
// caused_by_incident_uid as "still suppressed". Suppression is decided by
// paging_suppressed alone.
func caseRelapseBeforeResolveStillPages(t *testing.T, s *detachSetup) {
	t.Helper()

	r := require.New(t)

	parent := s.check(t, "parent-relapse", nil)
	child := s.check(t, "child-relapse", func(c *models.Check) {
		c.RecoveryPeriodSeconds = 300
		// The relapse below is this check's SECOND failing result on the
		// same incident row (FailureCount 1 -> 2): threshold 2 makes it
		// escalate immediately, which is the event that actually pages.
		c.EscalationThreshold = 2
	})
	s.hardEdge(t, parent, child)
	s.bindChannel(t, child, "child-channel")

	s.at(0)
	parent = s.fail(t, parent)

	s.at(30 * time.Second)
	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed)

	s.at(60 * time.Second)
	child = s.succeed(t, child)
	r.Equal(models.CheckStatusUp, child.Status)

	// Detach.
	s.at(90 * time.Second)
	s.succeed(t, parent)
	detached := s.reload(t, childInc)
	r.False(detached.PagingSuppressed)
	r.NotNil(detached.CausedByIncidentUID)
	r.Zero(s.pendingNotificationJobs(t), "the detach itself still must not page")

	// T+120s — relapse, well before the child's own 300s recovery window
	// would have elapsed. Same incident row, second failure, crosses the
	// escalation threshold.
	s.at(120 * time.Second)
	child = s.fail(t, child)
	r.Equal(models.CheckStatusDown, child.Status)

	relapsed := s.reload(t, childInc)
	r.Equal(childInc.UID, relapsed.UID, "the relapse continues the SAME incident, not a new one")
	r.Equal(2, relapsed.FailureCount)
	r.NotNil(relapsed.EscalatedAt)

	escalated := s.eventsOfType(t, childInc, models.EventTypeIncidentEscalated)
	r.Len(escalated, 1)

	r.Positive(s.pendingNotificationJobs(t),
		"the relapse must page now that paging_suppressed is false — "+
			"the kept caused_by_incident_uid must not be misread as still-suppressed")
}

// 3. Regression guard: the still-down branch of reEvaluateChild is untouched
// by this spec. It already kept attribution before this fix (only the
// recovered branch cleared it) — verify that stays true, and that this is
// still the reopened+pages branch, not the detach branch.
func caseStillDownUnchangedReopensAndPages(t *testing.T, s *detachSetup) {
	t.Helper()

	r := require.New(t)

	parent := s.check(t, "parent-stilldown", nil)
	child := s.check(t, "child-stilldown", nil) // never recovers in this scenario
	s.hardEdge(t, parent, child)
	s.bindChannel(t, child, "child-channel")

	s.at(0)
	parent = s.fail(t, parent)
	parentInc := s.activeIncident(t, parent)

	s.at(30 * time.Second)
	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed)
	r.NotNil(childInc.CausedByIncidentUID)
	r.Zero(s.pendingNotificationJobs(t), "suppressed open must not page")

	// T+60s — the parent resolves while the child is STILL down.
	s.at(60 * time.Second)
	s.succeed(t, parent)
	r.Equal(models.IncidentStateResolved, s.reload(t, parentInc).State)

	reEvaluated := s.reload(t, childInc)
	r.False(reEvaluated.PagingSuppressed, "un-suppressed so the child can page on its own now")
	r.NotNil(reEvaluated.CausedByIncidentUID, "still-down branch keeps attribution too — unchanged behavior")
	r.Equal(parentInc.UID, *reEvaluated.CausedByIncidentUID)
	r.Equal(models.IncidentStateActive, reEvaluated.State)

	reopened := s.eventsOfType(t, childInc, models.EventTypeIncidentReopened)
	r.Len(reopened, 1)
	r.Empty(s.eventsOfType(t, childInc, models.EventTypeIncidentRollupDetached),
		"the still-down branch is the reopen branch, not the detach branch")

	r.Positive(s.pendingNotificationJobs(t), "an un-suppressed still-down child pages immediately")
}

// 4. Reopen from scratch, as a regression guard: once a previously-detached
// child's own incident finally resolves, a later relapse with NO parent
// currently failing is a fresh rollup decision. It must not inherit the kept
// caused_by_incident_uid from its detached predecessor.
func caseReopenClearsAttribution(t *testing.T, s *detachSetup) {
	t.Helper()

	r := require.New(t)

	parent := s.check(t, "parent-reopen", nil)
	child := s.check(t, "child-reopen", func(c *models.Check) {
		c.RecoveryPeriodSeconds = 60
	})
	s.hardEdge(t, parent, child)

	s.at(0)
	parent = s.fail(t, parent)

	s.at(30 * time.Second)
	child = s.fail(t, child)
	childInc := s.activeIncident(t, child)
	r.True(childInc.PagingSuppressed)

	// T+60s — child recovers (arms the 60s recovery clock).
	s.at(60 * time.Second)
	child = s.succeed(t, child)

	// T+90s — parent resolves: detach. Attribution kept, per case 1.
	s.at(90 * time.Second)
	s.succeed(t, parent)
	detached := s.reload(t, childInc)
	r.False(detached.PagingSuppressed)
	r.NotNil(detached.CausedByIncidentUID)

	// T+160s — a second success, 100s after the recovery clock armed at
	// T+60s: the 60s recovery window has now elapsed, so the child's OWN
	// incident finally resolves, still carrying its historical attribution.
	s.at(160 * time.Second)
	child = s.succeed(t, child)
	resolved := s.reload(t, childInc)
	r.Equal(models.IncidentStateResolved, resolved.State)
	r.NotNil(resolved.CausedByIncidentUID, "a resolved incident keeps its historical record")

	// T+170s — the child fails again, well inside its reopen cooldown
	// (5 * 60s period, floored at 2 minutes -> well over 10s). The parent is
	// healthy: no candidate for applyRollup to attach to.
	s.at(170 * time.Second)
	child = s.fail(t, child)

	reopened := s.activeIncident(t, child)
	r.Equal(childInc.UID, reopened.UID, "reopen reuses the same incident row")
	r.Nil(reopened.CausedByIncidentUID,
		"a reopen with no live parent is a fresh decision — it must not inherit the detached attribution")
	r.False(reopened.PagingSuppressed)
}

// TestRollupDetach_SQLite runs the whole table on in-memory SQLite.
func TestRollupDetach_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range detachCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := require.New(t)

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			tc.run(t, newDetachSetup(t, dbSvc, "acme"))
		})
	}
}

// TestRollupDetach_Postgres runs the identical table against real Postgres.
//
// Self-skips under `-short` (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring the other Postgres siblings in this
// package.
func TestRollupDetach_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portRollupDetachPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	for i, tc := range detachCases() {
		t.Run(tc.name, func(t *testing.T) {
			// One embedded server, one org per case — the cases share no
			// rows, so they run concurrently against it.
			t.Parallel()

			tc.run(t, newDetachSetup(t, dbSvc, detachOrgSlugForCase(i)))
		})
	}
}

// detachOrgSlugForCase gives each Postgres sub-case its own org so the
// shared embedded database keeps the cases isolated (check slugs are unique
// per org).
func detachOrgSlugForCase(index int) string {
	return "detach-pg-" + string(rune('a'+index))
}
