package jobsvc_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// ListForIncident is what makes an interrupted escalation cycle recoverable
// without a new column: the rows the ack's sweep soft-deleted ARE the record of
// where the cycle stood, and they are only readable because the sweep
// soft-deletes rather than erasing.
//
// It deliberately returns the WHOLE history — every status, canceled rows
// included — because deciding which rungs may be resumed requires correlating
// the generations of one rung against each other. That policy lives in
// incidents.resumableEscalationSteps; this test pins the primitive's contract:
// nothing is filtered out except the incident and the job type.
func TestListForIncident(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("canceled-list", "Canceled List")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	const incidentUID = "incident-1"

	stepConfig := func(stepUID string) json.RawMessage {
		raw, marshalErr := json.Marshal(map[string]any{
			"incidentUid": incidentUID, "stepUid": stepUID, "repeatIndex": 0,
		})
		r.NoError(marshalErr)

		return raw
	}

	later := time.Now().Add(time.Hour)
	evenLater := later.Add(time.Hour)

	fired, err := svc.CreateJob(ctx, org.UID, string(jobdef.JobTypeEscalationStep),
		stepConfig("step-1"), nil)
	r.NoError(err)

	_, err = svc.CreateJob(ctx, org.UID, string(jobdef.JobTypeEscalationStep),
		stepConfig("step-3"), &jobsvc.JobOptions{ScheduledAt: &evenLater})
	r.NoError(err)

	_, err = svc.CreateJob(ctx, org.UID, string(jobdef.JobTypeEscalationStep),
		stepConfig("step-2"), &jobsvc.JobOptions{ScheduledAt: &later})
	r.NoError(err)

	// A notification job for the same incident: the sweep cancels it too, and
	// the resume must not pick it up.
	notifConfig, err := json.Marshal(map[string]string{"incidentUid": incidentUID})
	r.NoError(err)

	_, err = svc.CreateJob(ctx, org.UID, string(jobdef.JobTypeNotification), notifConfig, nil)
	r.NoError(err)

	// Step 1 fires before the acknowledgment lands.
	r.NoError(svc.UpdateJobStatus(ctx, fired, models.JobStatusSuccess, nil))

	_, err = svc.CancelPendingForIncident(ctx, incidentUID, nil)
	r.NoError(err)

	// A step scheduled AFTER the sweep is live, not canceled.
	live, err := svc.CreateJob(ctx, org.UID, string(jobdef.JobTypeEscalationStep),
		stepConfig("step-live"), nil)
	r.NoError(err)

	got, err := svc.ListForIncident(ctx, incidentUID, string(jobdef.JobTypeEscalationStep))
	r.NoError(err)
	r.Len(got, 4, "every generation of every rung comes back, whatever its status")

	uids := map[string]bool{}
	for _, job := range got {
		uids[job.UID] = true
	}

	// The two rows a canceled-only query would have hidden. Both are exactly
	// what the caller needs in order to NOT resume a rung: one already ran, the
	// other is still queued.
	r.True(uids[fired.UID], "a step that already ran must be visible, so it is never resumed")
	r.True(uids[live.UID], "a live step must be visible, so it is never duplicated")

	// Ordered oldest-due first, which is what lets the caller shift the whole
	// remaining cycle as a block.
	for i := 1; i < len(got); i++ {
		r.False(got[i].ScheduledAt.Before(got[i-1].ScheduledAt),
			"steps come back ordered by when they were due")
	}

	// The type filter holds: the same sweep canceled a notification job, and it
	// must not leak into an escalation-step read.
	notifications, err := svc.ListForIncident(ctx, incidentUID, string(jobdef.JobTypeNotification))
	r.NoError(err)
	r.Len(notifications, 1)

	// A different incident sees nothing.
	other, err := svc.ListForIncident(ctx, "incident-other", string(jobdef.JobTypeEscalationStep))
	r.NoError(err)
	r.Empty(other)
}
