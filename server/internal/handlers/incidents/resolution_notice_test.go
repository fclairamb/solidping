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
)

// pendingResolutionNoticeJobs counts the person-channel resolution notices
// queued for an org. Same direct-table read as pendingNotificationJobs, for the
// same reason (jobsvc.ListJobs cannot run on the in-memory sqlite).
func pendingResolutionNoticeJobs(t *testing.T, dbSvc *sqlite.Service, orgUID string) int {
	t.Helper()

	var jobs []*models.Job

	err := dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeIncidentResolutionNotice)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	return len(jobs)
}

// TestResolveQueuesResolutionNotice is the headline of spec 2026-08-14-01: a
// resolution has to close the loop with the PEOPLE who were paged, not only
// with the channel connections. Channel notifications already fan out; this
// pins the second job.
func TestResolveQueuesResolutionNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	r.Equal(0, pendingResolutionNoticeJobs(t, s.dbSvc, s.org.UID))

	out, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, out.State)

	r.Equal(1, pendingResolutionNoticeJobs(t, s.dbSvc, s.org.UID),
		"resolving must queue exactly one resolution notice job")
}

// A rolled-up child was never paged, so it must not announce a resolution
// either — the notice job must not even be created.
func TestSuppressedResolveQueuesNoResolutionNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	suppressed := true
	r.NoError(s.dbSvc.UpdateIncident(ctx, s.incident.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressed,
	}))

	_, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	r.Equal(0, pendingResolutionNoticeJobs(t, s.dbSvc, s.org.UID),
		"a paging-suppressed child must not queue a resolution notice")
}

// A group incident pages under its own UID and returns early from the lifecycle
// switch through the group branch — the notice must be queued BEFORE that
// branch, or grouped incidents would silently lose the feature.
func TestGroupIncidentResolveQueuesResolutionNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	group := models.NewCheckGroup(s.org.UID, "Platform", "platform")
	r.NoError(s.dbSvc.CreateCheckGroup(ctx, group))

	groupInc := models.NewIncident(s.org.UID, s.check.UID, time.Now().Add(-3*time.Minute), "platform is down")
	groupInc.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateIncident(ctx, groupInc))

	_, err := s.svc.ResolveIncident(ctx, s.org.Slug, &incidents.ResolveIncidentRequest{
		IncidentUID: groupInc.UID,
		Via:         "web",
	})
	r.NoError(err)

	r.Equal(1, pendingResolutionNoticeJobs(t, s.dbSvc, s.org.UID),
		"a group incident resolution must queue the notice job too")
}

// Only a resolution queues the job: an ack or a comment must not.
func TestNonResolveEventsQueueNoResolutionNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	r.Equal(0, pendingResolutionNoticeJobs(t, s.dbSvc, s.org.UID),
		"acknowledging is not resolving")
}
