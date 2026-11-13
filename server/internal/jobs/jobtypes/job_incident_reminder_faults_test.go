package jobtypes

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// runErr runs one reminder and hands back its error instead of requiring
// success.
func (e *reminderEnv) runErr(t *testing.T, incidentUID string) error {
	t.Helper()

	cfg, err := json.Marshal(IncidentReminderJobConfig{
		OrganizationUID: e.org.UID, IncidentUID: incidentUID,
	})
	require.NoError(t, err)

	runner, err := (&IncidentReminderJobDefinition{}).CreateJobRun(cfg)
	require.NoError(t, err)

	return runner.Run(t.Context(), e.jctx)
}

// One store call fails per case. The reminder is a chain: what matters is
// whether the send happened, whether it can happen twice, and whether the
// next link exists.
func TestIncidentReminder_StoreFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// fail holds the SQL fragments of the one query that breaks; empty is
		// the positive control.
		fail          []string
		wantErr       string
		reminderCount int
		notifications int
		audits        int
		nextReminders int
	}{
		{
			name:          "control: nothing fails",
			reminderCount: 1, notifications: 1, audits: 1, nextReminders: 1,
		},
		{
			// A database blip must not silence a reminder.
			name:          "maintenance lookup fails: the reminder is still sent",
			fail:          []string{`FROM "maintenance_windows"`},
			reminderCount: 1, notifications: 1, audits: 1, nextReminders: 1,
		},
		{
			// Nothing was claimed, so the retry sends the reminder.
			name:    "claim fails: retried, nothing sent",
			fail:    []string{`UPDATE "incidents"`, "reminder_count"},
			wantErr: "claim reminder",
		},
		{
			// The claim is spent: the chain goes on, this reminder is lost.
			name:          "channel listing fails: chain continues without a send",
			fail:          []string{`FROM "integrations"`},
			reminderCount: 1, nextReminders: 1,
		},
		{
			name:          "notification job insert fails: chain continues",
			fail:          []string{`INSERT INTO "jobs"`, `'notification'`},
			reminderCount: 1, nextReminders: 1,
		},
		{
			// The audit row is bookkeeping: the page still goes out.
			name:          "audit row insert fails: the notification is still queued",
			fail:          []string{`INSERT INTO "incident_notifications"`},
			reminderCount: 1, notifications: 1, nextReminders: 1,
		},
		{
			name:          "next reminder insert fails: retryable",
			fail:          []string{`INSERT INTO "jobs"`, `'incident_reminder'`},
			wantErr:       "schedule incident reminder",
			reminderCount: 1, notifications: 1, audits: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newReminderEnv(t)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(env.db.DB(), tt.fail...)
			}

			err := env.runErr(t, env.incident.UID)

			if fault != nil {
				fault.requireFired(t)
			}

			if tt.wantErr == "" {
				r.NoError(err)
			} else {
				r.ErrorContains(err, tt.wantErr)
				r.ErrorIs(err, context.Canceled, "the store error is wrapped, not replaced")
				r.True(jobdef.IsRetryable(err), "a store failure is worth a retry")
			}

			r.Equal(tt.reminderCount, env.reminderCount(t))
			r.Len(env.pendingJobs(t, jobdef.JobTypeNotification), tt.notifications)
			r.Len(env.pendingJobs(t, jobdef.JobTypeIncidentReminder), tt.nextReminders)

			var audits int
			audits, err = env.db.DB().NewSelect().TableExpr("incident_notifications").
				Where("incident_uid = ?", env.incident.UID).Count(t.Context())
			r.NoError(err)
			r.Equal(tt.audits, audits)
		})
	}
}

// A reminder whose incident or check no longer exists ends the chain quietly:
// no send, no next reminder, no error for the worker to retry forever.
func TestIncidentReminder_StopsWhenIncidentOrCheckIsGone(t *testing.T) {
	t.Parallel()

	t.Run("unknown incident", func(t *testing.T) {
		t.Parallel()

		env := newReminderEnv(t)
		require.NoError(t, env.runErr(t, "00000000-0000-0000-0000-000000000000"))
		env.assertStopped(t)
	})

	t.Run("deleted check", func(t *testing.T) {
		t.Parallel()

		env := newReminderEnv(t)
		require.NoError(t, env.db.DeleteCheck(t.Context(), env.check.UID))
		require.NoError(t, env.runErr(t, env.incident.UID))
		env.assertStopped(t)
	})

	t.Run("not a check incident", func(t *testing.T) {
		t.Parallel()

		env := newReminderEnv(t)
		_, err := env.db.DB().NewUpdate().TableExpr("incidents").
			Set("kind = ?", string(models.IncidentKindDegraded)).
			Where("uid = ?", env.incident.UID).Exec(t.Context())
		require.NoError(t, err)

		require.NoError(t, env.runErr(t, env.incident.UID))
		env.assertStopped(t)
	})
}

// A process without a job service (a one-shot CLI run) still claims the
// reminder and reports success: it has nowhere to queue the next link.
func TestIncidentReminder_WithoutJobService(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newReminderEnv(t)
	env.jctx.Services = &services.Registry{Clock: env.clock}

	r.NoError(env.runErr(t, env.incident.UID))
	r.Equal(1, env.reminderCount(t), "the claim happened")
	r.Empty(env.pendingJobs(t, jobdef.JobTypeNotification))
	r.Empty(env.pendingJobs(t, jobdef.JobTypeIncidentReminder))

	r.NoError(ScheduleIncidentReminder(t.Context(), nil, env.db, env.incident, env.check, env.clock.Now()))

	canceled, err := CancelIncidentReminders(t.Context(), nil, env.incident.UID)
	r.NoError(err)
	r.Zero(canceled)
}

// An escalation step queues a notification knowing only the channel UID. When
// the channel cannot be loaded for the audit row, the job is already queued,
// so the call still reports it as sent.
func TestQueueNotificationWithAudit_ChannelLoadFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newReminderEnv(t)
	fault := failQueries(env.db.DB(), `FROM "integrations"`)

	cfg := &NotificationJobConfig{
		ConnectionUID: env.webhook.UID, IncidentUID: env.incident.UID, EventType: "incident.created",
	}

	queued := queueNotificationWithAudit(t.Context(), env.jctx, env.jctx.Logger, env.incident, cfg, nil,
		models.IncidentNotificationSourceCheckConnection, nil, nil)

	fault.requireFired(t)
	r.True(queued)
	r.Len(env.pendingJobs(t, jobdef.JobTypeNotification), 1)
}

// reminderJobs is a jobsvc.Service whose listing and cancel results are set by
// the test.
type reminderJobs struct {
	jobsvc.Service

	history   []*models.Job
	listErr   error
	cancelErr map[string]error
	canceled  []string
}

func (j *reminderJobs) ListForIncident(context.Context, string, string) ([]*models.Job, error) {
	return j.history, j.listErr
}

func (j *reminderJobs) CancelJob(_ context.Context, uid string) error {
	if err := j.cancelErr[uid]; err != nil {
		return err
	}

	j.canceled = append(j.canceled, uid)

	return nil
}

func TestCancelIncidentReminders(t *testing.T) {
	t.Parallel()

	deletedAt := time.Now()
	history := []*models.Job{
		{UID: "done", Status: models.JobStatusSuccess},
		{UID: "already-canceled", Status: models.JobStatusPending, DeletedAt: &deletedAt},
		{UID: "first", Status: models.JobStatusPending},
		{UID: "second", Status: models.JobStatusPending},
	}

	tests := []struct {
		name         string
		listErr      error
		cancelErr    map[string]error
		wantCanceled []string
		wantErr      string
	}{
		{name: "cancels only the live pending reminders", wantCanceled: []string{"first", "second"}},
		{name: "listing fails", listErr: errTestBoom, wantErr: "list incident reminders"},
		{
			// A worker picked the job up between the listing and the cancel.
			name:         "a reminder that just started is skipped",
			cancelErr:    map[string]error{"first": jobsvc.ErrJobNotPending},
			wantCanceled: []string{"second"},
		},
		{
			name:         "a cancel failure stops and reports what was done",
			cancelErr:    map[string]error{"second": errTestBoom},
			wantCanceled: []string{"first"},
			wantErr:      "cancel incident reminder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			jobs := &reminderJobs{history: history, listErr: tt.listErr, cancelErr: tt.cancelErr}

			canceled, err := CancelIncidentReminders(t.Context(), jobs, "incident")

			if tt.wantErr == "" {
				r.NoError(err)
			} else {
				r.ErrorContains(err, tt.wantErr)
				r.ErrorIs(err, errTestBoom)
			}

			r.Equal(len(tt.wantCanceled), canceled)
			r.Equal(tt.wantCanceled, jobs.canceled)
		})
	}
}

// A parameter value is whatever the writer stored or a JSON round trip made
// of it. A fractional or unparsable value is refused, never truncated.
func TestReminderParameterInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    any
		want   int
		wantOK bool
	}{
		{"int", 900, 900, true},
		{"int64", int64(900), 900, true},
		{"whole float64", float64(900), 900, true},
		{"fractional float64", 900.5, 0, false},
		{"json number", json.Number("900"), 900, true},
		{"fractional json number", json.Number("900.5"), 0, false},
		{"string", "900", 0, false},
		{"nil", nil, 0, false},
		{"NaN", math.NaN(), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ParameterInt(tt.raw)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// Off is the default: no check, or an org parameter that is not a whole
// number, resolves to no reminder.
func TestReminderInterval_Fallbacks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newReminderEnv(t)

	r.Zero(ReminderInterval(t.Context(), env.db, nil))

	inherit := *env.check
	inherit.ReminderIntervalSeconds = nil

	r.NoError(env.db.SetOrgParameter(t.Context(), env.org.UID, models.ParamKeyIncidentReminderInterval,
		"soon", false))
	r.Zero(ReminderInterval(t.Context(), env.db, &inherit), "an unreadable org value means off")

	r.NoError(env.db.SetOrgParameter(t.Context(), env.org.UID, models.ParamKeyIncidentReminderInterval,
		3600, false))
	r.Equal(time.Hour, ReminderInterval(t.Context(), env.db, &inherit), "positive control")
}
