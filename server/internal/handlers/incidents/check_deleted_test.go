package incidents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// TestNotifyCheckDeletedIncidentsQueuesOneResolved pins the resolved open
// question of spec 2026-10-08-02: a single-check delete sends exactly one
// "resolved" notification per incident it closed, recorded with
// resolution_type check_deleted.
func TestNotifyCheckDeletedIncidentsQueuesOneResolved(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	r.NoError(s.dbSvc.DeleteCheck(ctx, s.check.UID))
	r.Equal(0, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"the DB layer resolves silently: notifying is the service's job")

	s.svc.NotifyCheckDeletedIncidents(ctx, s.org.UID, s.check, []string{s.incident.UID})

	r.Equal(1, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"one resolved notification for the one bound channel")

	var resolved []*models.Event
	for _, event := range listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID) {
		if event.EventType == models.EventTypeIncidentResolved {
			resolved = append(resolved, event)
		}
	}

	r.Len(resolved, 1)
	r.Equal(models.ResolutionTypeCheckDeleted, resolved[0].Payload["resolution_type"])
	r.Equal(s.check.UID, resolved[0].Payload["check_uid"])
}

// TestNotifyCheckDeletedIncidentsSkipsOtherResolutions: an incident resolved
// some other way in between was already announced; it is not told twice.
func TestNotifyCheckDeletedIncidentsSkipsOtherResolutions(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	// Still active: nothing to say.
	s.svc.NotifyCheckDeletedIncidents(ctx, s.org.UID, s.check, []string{s.incident.UID})
	r.Equal(0, pendingNotificationJobs(t, s.dbSvc, s.org.UID))

	_, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{IncidentUID: s.incident.UID})
	r.NoError(err)

	before := pendingNotificationJobs(t, s.dbSvc, s.org.UID)

	r.NoError(s.dbSvc.DeleteCheck(ctx, s.check.UID))
	s.svc.NotifyCheckDeletedIncidents(ctx, s.org.UID, s.check, []string{s.incident.UID})

	r.Equal(before, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"a manually resolved incident is not re-announced as check deleted")
}

// TestResultForDeletedCheckOpensNoIncident closes the race of spec
// 2026-10-08-02: a result in flight when its check was deleted must not open
// an incident that nothing would ever close.
func TestResultForDeletedCheckOpensNoIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Positive control: the same failure on a live check opens an incident.
	live := newFlapSetup(t)
	live.submit(t, models.ResultStatusDown)
	r.NotNil(live.activeIncident(t), "a live check's failure opens an incident")

	s := newFlapSetup(t)
	r.NoError(s.dbSvc.DeleteCheck(t.Context(), s.check.UID))

	// s.check is the worker's stale snapshot, taken before the delete.
	s.submit(t, models.ResultStatusDown)
	r.Nil(s.activeIncident(t), "a deleted check's in-flight result opens no incident")
}

// TestResultForDeletedCheckDoesNotReopen: the incident DeleteCheck resolved
// must not be reopened by a failure that was already in flight.
func TestResultForDeletedCheckDoesNotReopen(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newFlapSetup(t)

	s.submit(t, models.ResultStatusDown)
	inc := s.activeIncident(t)
	r.NotNil(inc)

	r.NoError(s.dbSvc.DeleteCheck(t.Context(), s.check.UID))

	s.submit(t, models.ResultStatusDown)
	r.Nil(s.activeIncident(t), "the check_deleted incident stays resolved")

	got, err := s.dbSvc.GetIncident(t.Context(), s.org.UID, inc.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, got.State)
	r.Equal(models.ResolutionTypeCheckDeleted, *got.ResolutionType)
}
