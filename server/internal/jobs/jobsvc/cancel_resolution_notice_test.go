package jobsvc_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestCancelPendingExemptsResolutionNotice pins the exemption that keeps the
// all-clear alive: ack / snooze / resolve cancel every PAGE still queued for an
// incident, but the incident_resolution_notice job is the message telling the
// people who were already paged that it is over. Sweeping it would mean an ack
// arriving between the resolution and the job's pickup silently swallows the
// notice — which is the very bug the job exists to fix, reached through the
// Acknowledge button the notice is supposed to remove.
//
// This also pins the string literal the sweep uses against jobdef's constant:
// jobsvc cannot import jobdef (jobdef → app/services → jobsvc), so the two
// would otherwise be free to drift apart silently.
func TestCancelPendingExemptsResolutionNotice(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("cancel-exempt", "Cancel Exempt")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	const incidentUID = "incident-1"

	config, err := json.Marshal(map[string]string{"incidentUid": incidentUID})
	r.NoError(err)

	notice, err := svc.CreateJob(ctx, org.UID,
		string(jobdef.JobTypeIncidentResolutionNotice), config, nil)
	r.NoError(err)

	escalation, err := svc.CreateJob(ctx, org.UID,
		string(jobdef.JobTypeEscalationStep), config, nil)
	r.NoError(err)

	notification, err := svc.CreateJob(ctx, org.UID,
		string(jobdef.JobTypeNotification), config, nil)
	r.NoError(err)

	canceled, err := svc.CancelPendingForIncident(ctx, incidentUID, nil)
	r.NoError(err)
	r.Equal(int64(2), canceled, "only the two paging jobs may be canceled")

	deleted, err := svc.IsJobDeleted(ctx, notice.UID)
	r.NoError(err)
	r.False(deleted, "the resolution notice must survive an ack/snooze/resolve sweep")

	deleted, err = svc.IsJobDeleted(ctx, escalation.UID)
	r.NoError(err)
	r.True(deleted, "a pending escalation step is still canceled")

	deleted, err = svc.IsJobDeleted(ctx, notification.UID)
	r.NoError(err)
	r.True(deleted, "a pending channel notification is still canceled")
}
