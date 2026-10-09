package checks_test

import (
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

// TestDeleteCheckResolvesAndNotifiesOnce pins spec 2026-10-08-02 on the
// service path: deleting one check resolves its active incident with
// resolution_type check_deleted and, when the notifier is wired, queues exactly
// one resolved notification. Unwired, the incident still resolves (the DB
// layer guarantees it), silently.
func TestDeleteCheckResolvesAndNotifiesOnce(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		wired    bool
		wantJobs int
	}{
		{"wired notifier", true, 1},
		{"no notifier", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()

			svc, dbSvc, org := newStatsService(t, "delete-resolves")

			if tc.wired {
				sqliteSvc, ok := dbSvc.(*sqlite.Service)
				r.True(ok)
				jobs := jobsvc.NewService(sqliteSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
				svc.SetCheckDeletedIncidentNotifier(incidents.NewService(dbSvc, jobs, clock.Real{}, nil))
			}

			check, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
			r.NoError(err)

			conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "ops")
			conn.Enabled = true
			r.NoError(dbSvc.CreateChannel(ctx, conn))
			r.NoError(dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(check.UID, conn.UID, org.UID)))

			incident := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "api is down")
			r.NoError(dbSvc.CreateIncident(ctx, incident))

			r.NoError(svc.DeleteCheck(ctx, org.Slug, check.UID))

			got, err := dbSvc.GetIncident(ctx, org.UID, incident.UID)
			r.NoError(err)
			r.Equal(models.IncidentStateResolved, got.State)
			r.NotNil(got.ResolvedAt)
			r.Equal(models.ResolutionTypeCheckDeleted, *got.ResolutionType)

			sqliteSvc, ok := dbSvc.(*sqlite.Service)
			r.True(ok)

			var pending []*models.Job
			r.NoError(sqliteSvc.DB().NewSelect().
				Model(&pending).
				Where("organization_uid = ?", org.UID).
				Where("type = ?", string(jobdef.JobTypeNotification)).
				Where("status = ?", string(models.JobStatusPending)).
				Scan(ctx))
			r.Len(pending, tc.wantJobs, "one resolved notification per bound channel, only when wired")

			events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{OrganizationUID: org.UID, Limit: 100})
			r.NoError(err)

			found := false

			for _, event := range events {
				if event.EventType == models.EventTypeCheckDeleted {
					found = true
					r.EqualValues(1, event.Payload["active_incidents_count"])
				}
			}

			r.True(found, "check.deleted is emitted")
		})
	}
}
