package incidents_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// pendingUnackNoticeJobs returns the pending CHANNEL notices for the unack
// event, decoded, so a test can assert how many went out and what they say.
func pendingUnackNoticeJobs(
	t *testing.T, s *resolveSetup, orgUID string,
) []jobtypes.NotificationJobConfig {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	out := make([]jobtypes.NotificationJobConfig, 0, len(jobs))

	for _, job := range jobs {
		raw, marshalErr := json.Marshal(job.Config)
		require.NoError(t, marshalErr)

		var cfg jobtypes.NotificationJobConfig

		require.NoError(t, json.Unmarshal(raw, &cfg))

		if cfg.EventType == string(models.EventTypeIncidentUnacknowledged) {
			out = append(out, cfg)
		}
	}

	return out
}

// pendingUnackPersonNotices counts the live pending person-contact unack
// notice jobs for an org.
func pendingUnackPersonNotices(t *testing.T, s *resolveSetup, orgUID string) int {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeIncidentUnackNotice)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	return len(jobs)
}

// ackThenUnack drives the incident through a real acknowledgment and back.
func ackThenUnack(t *testing.T, s *resolveSetup) {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)
}

// The headline of spec 2026-08-28-07 piece A: withdrawing an acknowledgment
// used to send NOTHING, so everyone who was paged kept believing the first
// responder had it.
func TestUnackQueuesOneNoticePerDestination(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newResolveSetup(t)

	second := attachChannel(t, s, models.ConnectionTypeSlack, "slack-ops", models.JSONMap{
		"team_id": "T0ACME", "access_token": "xoxb-test", "channel_id": "C1",
	})

	ackThenUnack(t, s)

	notices := pendingUnackNoticeJobs(t, s, s.org.UID)
	r.Len(notices, 2, "every channel the alert reached must hear the retraction, exactly once")

	got := map[string]bool{}
	for _, cfg := range notices {
		r.False(got[cfg.ConnectionUID], "a destination must not be notified twice")
		got[cfg.ConnectionUID] = true
		r.Equal(s.incident.UID, cfg.IncidentUID)
		r.NotNil(cfg.Acknowledgment, "the retraction must carry the attribution, like the ack does")
	}

	r.True(got[s.connUID])
	r.True(got[second.UID])

	r.Equal(1, pendingUnackPersonNotices(t, s, s.org.UID),
		"the people paged over a person contact get their own notice job")
}

// The retraction must not fan out for an incident that was never paged for
// (a rolled-up child): there is no acknowledgment to retract on any channel.
func TestUnackIsSilentWhenPagingIsSuppressed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	suppressed := true
	r.NoError(s.dbSvc.UpdateIncident(ctx, s.incident.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressed,
	}))

	ackThenUnack(t, s)

	r.Empty(pendingUnackNoticeJobs(t, s, s.org.UID),
		"a rolled-up child was never paged, so nothing is owed a retraction")
	r.Equal(0, pendingUnackPersonNotices(t, s, s.org.UID))
}

// Nor for a RESOLVED incident: "this is unowned again, escalation resumes" is
// simply false once the incident is over, and the resolution notice has
// already closed that conversation.
func TestUnackIsSilentOnAResolvedIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	resolvedAt := time.Now()
	resolvedState := models.IncidentStateResolved
	r.NoError(s.dbSvc.UpdateIncident(ctx, s.incident.UID, &models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}))

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	r.Empty(pendingUnackNoticeJobs(t, s, s.org.UID))
	r.Equal(0, pendingUnackPersonNotices(t, s, s.org.UID))
}

// An unack on an incident that was never acknowledged is a no-op: the early
// return must stay ahead of the fan-out, or a stray API call would announce a
// retraction nobody made.
func TestUnackOnAnUnacknowledgedIncidentAnnouncesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newResolveSetup(t)

	_, err := s.svc.UnacknowledgeIncident(context.Background(), s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	r.Empty(pendingUnackNoticeJobs(t, s, s.org.UID))
	r.Equal(0, pendingUnackPersonNotices(t, s, s.org.UID))
}
