package incidents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// resolveSetup spins up an in-memory sqlite, an org, a check, an active
// incident, and one bound notification connection. Returns the bits the
// resolve tests need: service, db, jobs, the ids.
type resolveSetup struct {
	svc      *incidents.Service
	dbSvc    *sqlite.Service
	org      *models.Organization
	check    *models.Check
	incident *models.Incident
	connUID  string
	// jobs is the same job service the incidents service writes through, so a
	// test can plant a pending job and watch the cancellation sweep reach it.
	jobs jobsvc.Service
}

func newResolveSetup(t *testing.T) *resolveSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("resolve-test", "Resolve Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "ops")
	conn.Enabled = true
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	cc := models.NewCheckConnection(check.UID, conn.UID, org.UID)
	r.NoError(dbSvc.CreateCheckConnection(ctx, cc))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	return &resolveSetup{
		svc: svc, dbSvc: dbSvc, jobs: jobs,
		org: org, check: check, incident: inc, connUID: conn.UID,
	}
}

// pendingNotificationJobs returns the count of pending notification jobs
// in the org. Queries the table directly because jobsvc.ListJobs uses a
// bun pattern (Model(typed-nil) + Scan-with-no-destination) that errors
// on the in-memory sqlite — the production code paths read jobs through
// GetJobWait and ListPending, not ListJobs.
func pendingNotificationJobs(t *testing.T, dbSvc *sqlite.Service, orgUID string) int {
	t.Helper()
	var jobs []*models.Job
	err := dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)
	return len(jobs)
}

// listIncidentEvents reads every event the persistence layer has recorded
// for the given incident, ordered by created_at desc.
func listIncidentEvents(
	t *testing.T, dbSvc *sqlite.Service, orgUID, incidentUID string,
) []*models.Event {
	t.Helper()
	events, err := dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: orgUID,
		IncidentUID:     &incidentUID,
		Limit:           100,
	})
	require.NoError(t, err)
	return events
}

// TestManualResolveQueuesNotification verifies the headline fix: a manual
// resolve must fan out a notification to every channel bound to the check,
// matching the auto-resolve path.
func TestManualResolveQueuesNotification(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	r.Equal(0, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"no notification jobs queued before resolve")

	out, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
		Note:        "fixed",
	})
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, out.State)

	r.Equal(1, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"manual resolve must queue one notification per bound channel")
}

// TestManualResolveActorAttribution pins the contract that the resolve
// event row records the user who clicked Resolve, not the system actor.
// emitEvent gained payload-driven actor attribution to make this work
// without a parallel event-creation path.
func TestManualResolveActorAttribution(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	user := models.NewUser("alice@example.com")
	r.NoError(s.dbSvc.CreateUser(ctx, user))

	_, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
		ActorUID:    user.UID,
	})
	r.NoError(err)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	r.NotEmpty(events, "manual resolve must create an event row")

	var resolved *models.Event
	for _, e := range events {
		if e.EventType == models.EventTypeIncidentResolved {
			resolved = e
			break
		}
	}
	r.NotNil(resolved, "must find a resolved event")
	r.Equal(models.ActorTypeUser, resolved.ActorType,
		"manual resolve carries an actor user, not the system")
	r.NotNil(resolved.ActorUID)
	r.Equal(user.UID, *resolved.ActorUID, "actor UID round-trips through emitEvent")
}

// TestManualResolveIdempotent pins the early-return at line ~1924: a second
// resolve call on an already-resolved incident is a no-op. We must not
// emit a duplicate event or queue a duplicate notification.
func TestManualResolveIdempotent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID, Via: "web",
	})
	r.NoError(err)

	firstCount := pendingNotificationJobs(t, s.dbSvc, s.org.UID)
	r.Equal(1, firstCount)

	// Second call: incident already resolved, must be a no-op.
	_, err = s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID, Via: "web",
	})
	r.NoError(err)
	r.Equal(firstCount, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"second resolve is a no-op; no extra notification queued")

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	resolved := 0
	for _, e := range events {
		if e.EventType == models.EventTypeIncidentResolved {
			resolved++
		}
	}
	r.Equal(1, resolved, "exactly one resolved event regardless of duplicate calls")
}

// countResolvedEvents returns how many incident.resolved events the
// persistence layer recorded for the given incident.
func countResolvedEvents(t *testing.T, dbSvc *sqlite.Service, orgUID, incidentUID string) int {
	t.Helper()
	events := listIncidentEvents(t, dbSvc, orgUID, incidentUID)
	count := 0
	for _, e := range events {
		if e.EventType == models.EventTypeIncidentResolved {
			count++
		}
	}
	return count
}

// TestManualResolveSuppressedRolledUpChildStillEmitsResolved pins the fixed
// contract for grouped/rolled-up incidents: a paging-suppressed child that is
// resolved must still RECORD its incident.resolved event (the dashboard
// timeline shows closure) but must NOT page any channel. This is the inverse
// of the pre-fix behavior, where the suppressed child's resolve fanned out a
// notification and produced a context-free orphan "resolved" message.
func TestManualResolveSuppressedRolledUpChildStillEmitsResolved(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	// Mark the seeded incident as a rolled-up, paging-suppressed child.
	suppressed := true
	r.NoError(s.dbSvc.UpdateIncident(ctx, s.incident.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressed,
	}))

	r.Equal(0, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"no notification jobs before resolve")

	out, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, out.State)

	r.Equal(1, countResolvedEvents(t, s.dbSvc, s.org.UID, s.incident.UID),
		"suppressed child still records exactly one incident.resolved event row")
	r.Equal(0, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"suppressed child resolve must NOT page any channel")
}

// TestRolledUpChildResolveDoesNotPageWhenSuppressed exercises the same
// invariant through the real rollup-attribution path: a child check with a
// hard dependency on a parent that already has an open incident rolls up
// (paging_suppressed=true, caused_by_incident_uid=parent) via applyRollup.
// Resolving that still-suppressed child records the event but pages nothing.
func TestRolledUpChildResolveDoesNotPageWhenSuppressed(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	// A parent check with an open (non-suppressed) incident inside the window.
	parent := models.NewCheck(s.org.UID, "parent", "http")
	r.NoError(s.dbSvc.CreateCheck(ctx, parent))
	parentInc := models.NewIncident(s.org.UID, parent.UID, time.Now().Add(-2*time.Minute), "parent is down")
	r.NoError(s.dbSvc.CreateIncident(ctx, parentInc))

	// A child check that hard-depends on the parent.
	child := models.NewCheck(s.org.UID, "child", "http")
	r.NoError(s.dbSvc.CreateCheck(ctx, child))
	dep := models.NewCheckDependency(s.org.UID, parent.UID, child.UID, models.CheckDependencyKindHard, nil)
	r.NoError(s.dbSvc.CreateCheckDependency(ctx, dep))
	// Bind a channel to the child so a fan-out (if it wrongly happened) would
	// produce a job we can detect.
	childConn := models.NewIntegration(s.org.UID, models.ConnectionTypeWebhook, "child-ops")
	childConn.Enabled = true
	r.NoError(s.dbSvc.CreateChannel(ctx, childConn))
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(child.UID, childConn.UID, s.org.UID)))

	// Build the child incident and let applyRollup attribute + suppress it.
	childInc := models.NewIncident(s.org.UID, child.UID, time.Now(), "child is down")
	s.svc.ApplyRollupForTest(ctx, child, childInc)
	r.True(childInc.PagingSuppressed, "child must be paging-suppressed after rollup")
	r.NotNil(childInc.CausedByIncidentUID, "child must be attributed to the parent")
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID)
	r.NoError(s.dbSvc.CreateIncident(ctx, childInc))

	before := pendingNotificationJobs(t, s.dbSvc, s.org.UID)

	out, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: childInc.UID,
		Via:         "web",
	})
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, out.State)

	r.Equal(1, countResolvedEvents(t, s.dbSvc, s.org.UID, childInc.UID),
		"rolled-up child still records its incident.resolved event")
	r.Equal(before, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"suppressed rolled-up child resolve enqueues zero notification jobs")
}

// TestUnsuppressedChildResolveStillPages is the regression guard for the fix:
// once a child has been un-suppressed (the state reEvaluateChild leaves it in
// when it is still down at parent resolution), it is a real standalone
// incident and MUST page on its own subsequent resolution. Gating on the
// current PagingSuppressed value (false here) must not silence it.
func TestUnsuppressedChildResolveStillPages(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	// The seeded incident is not suppressed (the post-reEvaluateChild state).
	r.False(s.incident.PagingSuppressed)

	r.Equal(0, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"no notification jobs before resolve")

	out, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, out.State)

	r.Equal(1, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"an un-suppressed child resolve pages one notification per bound channel")
}
