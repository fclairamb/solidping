package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// reminderEnv is an in-memory world for the reminder job: one check with a
// 900 s interval, one open incident, and four channels bound to the check:
// an enabled webhook (the only one that must be reminded), a disabled
// webhook, a Twilio channel and a PagerDuty channel.
type reminderEnv struct {
	db       *sqlite.Service
	jobs     jobsvc.Service
	clock    *clock.Fake
	org      *models.Organization
	check    *models.Check
	incident *models.Incident
	webhook  *models.Integration
	jctx     *jobdef.JobContext
}

const reminderTestInterval = 900

func newReminderEnv(t *testing.T) *reminderEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	fakeClock := clock.NewFake(time.Now().Truncate(time.Second))

	org := models.NewOrganization("reminder-org", "Reminder Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	interval := reminderTestInterval
	check.ReminderIntervalSeconds = &interval
	r.NoError(dbSvc.CreateCheck(ctx, check))

	bind := func(connType models.ConnectionType, name string, enabled bool) *models.Integration {
		conn := models.NewIntegration(org.UID, connType, name)
		conn.Enabled = enabled
		r.NoError(dbSvc.CreateChannel(ctx, conn))
		r.NoError(dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(check.UID, conn.UID, org.UID)))

		return conn
	}

	webhook := bind(models.ConnectionTypeWebhook, "ops", true)
	bind(models.ConnectionTypeWebhook, "muted", false)
	bind(models.ConnectionTypeTwilio, "sms", true)
	bind(models.ConnectionTypePagerduty, "pd", true)

	incident := models.NewIncident(org.UID, check.UID, fakeClock.Now().Add(-time.Hour), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	return &reminderEnv{
		db: dbSvc, jobs: jobs, clock: fakeClock, org: org, check: check, incident: incident, webhook: webhook,
		jctx: &jobdef.JobContext{
			DBService: dbSvc,
			Logger:    slog.Default(),
			Services:  &services.Registry{Jobs: jobs, Clock: fakeClock},
		},
	}
}

// pendingJobs returns the pending, non-canceled jobs of one type.
func (e *reminderEnv) pendingJobs(t *testing.T, jobType jobdef.JobType) []*models.Job {
	t.Helper()

	var jobs []*models.Job
	require.NoError(t, e.db.DB().NewSelect().Model(&jobs).
		Where("organization_uid = ?", e.org.UID).
		Where("type = ?", string(jobType)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Order("scheduled_at ASC").
		Scan(t.Context()))

	return jobs
}

func (e *reminderEnv) run(t *testing.T, seq int) {
	t.Helper()

	cfg, err := json.Marshal(IncidentReminderJobConfig{
		OrganizationUID: e.org.UID, IncidentUID: e.incident.UID, Seq: seq,
	})
	require.NoError(t, err)

	runner, err := (&IncidentReminderJobDefinition{}).CreateJobRun(cfg)
	require.NoError(t, err)
	require.NoError(t, runner.Run(t.Context(), e.jctx))
}

func (e *reminderEnv) update(t *testing.T, update *models.IncidentUpdate) {
	t.Helper()
	require.NoError(t, e.db.UpdateIncident(t.Context(), e.incident.UID, update))
}

func (e *reminderEnv) reminderCount(t *testing.T) int {
	t.Helper()

	stored, err := e.db.GetIncident(t.Context(), e.org.UID, e.incident.UID)
	require.NoError(t, err)

	return stored.ReminderCount
}

func reminderSeqOf(t *testing.T, job *models.Job) int {
	t.Helper()

	var cfg IncidentReminderJobConfig
	raw, err := json.Marshal(job.Config)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cfg))

	return cfg.Seq
}

// assertStopped is "no send, no reschedule".
func (e *reminderEnv) assertStopped(t *testing.T) {
	t.Helper()

	r := require.New(t)
	r.Empty(e.pendingJobs(t, jobdef.JobTypeNotification), "no reminder must be sent")
	r.Empty(e.pendingJobs(t, jobdef.JobTypeIncidentReminder), "the chain must end")
	r.Equal(0, e.reminderCount(t))
}

func TestIncidentReminder_RegisteredAndValidated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def, ok := GetJobDefinition(jobdef.JobTypeIncidentReminder)
	r.True(ok)
	r.Equal(jobdef.JobTypeIncidentReminder, def.Type())

	_, err := def.CreateJobRun(json.RawMessage(`{"organizationUid":"o","incidentUid":"i","seq":0}`))
	r.NoError(err)

	_, err = def.CreateJobRun(json.RawMessage(`{"organizationUid":"o"}`))
	r.ErrorIs(err, ErrMissingIncidentUID)

	_, err = def.CreateJobRun(json.RawMessage(`{"incidentUid":"i"}`))
	r.ErrorIs(err, ErrMissingOrganizationUID)
}

// The headline: scheduled at open + interval and not before; on run, one
// `incident.reminder` per enabled reminder-accepting channel, then the next
// reminder at now + interval.
func TestIncidentReminder_SchedulesSendsAndReschedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newReminderEnv(t)
	openedAt := env.clock.Now()

	r.NoError(ScheduleIncidentReminder(t.Context(), env.jobs, env.db, env.incident, env.check, openedAt))

	scheduled := env.pendingJobs(t, jobdef.JobTypeIncidentReminder)
	r.Len(scheduled, 1)
	r.True(scheduled[0].ScheduledAt.Equal(openedAt.Add(reminderTestInterval*time.Second)),
		"the first reminder is due at open + interval, not before (got %s)", scheduled[0].ScheduledAt)
	r.Equal(0, reminderSeqOf(t, scheduled[0]))

	// Fire it (the runner owns the claim; the test plays the worker).
	r.NoError(env.jobs.CancelJob(t.Context(), scheduled[0].UID))
	env.clock.Advance(reminderTestInterval * time.Second)
	env.run(t, 0)

	notifications := env.pendingJobs(t, jobdef.JobTypeNotification)
	r.Len(notifications, 1, "only the enabled webhook is reminded: not the disabled one, not Twilio, not PagerDuty")

	var cfg NotificationJobConfig
	raw, err := json.Marshal(notifications[0].Config)
	r.NoError(err)
	r.NoError(json.Unmarshal(raw, &cfg))
	r.Equal(env.webhook.UID, cfg.ConnectionUID)
	r.Equal("incident.reminder", cfg.EventType)
	r.Equal(1, cfg.ReminderSeq, "the first reminder is reminder 1")

	audit, err := env.db.ListIncidentNotifications(t.Context(), env.org.UID,
		db.ListIncidentNotificationsFilter{IncidentUID: env.incident.UID, Limit: 100})
	r.NoError(err)
	r.Len(audit, 1, "each reminder leaves an audit row")
	r.Equal("incident.reminder", audit[0].EventType)

	r.Equal(1, env.reminderCount(t))

	next := env.pendingJobs(t, jobdef.JobTypeIncidentReminder)
	r.Len(next, 1)
	r.Equal(1, reminderSeqOf(t, next[0]))
	r.True(next[0].ScheduledAt.Equal(env.clock.Now().Add(reminderTestInterval * time.Second)))
}

func TestIncidentReminder_StopsWhenHandled(t *testing.T) {
	t.Parallel()

	cases := map[string]func(env *reminderEnv) *models.IncidentUpdate{
		// No cancel sweep ran: the fire-time guard alone must stop it.
		"acknowledged": func(env *reminderEnv) *models.IncidentUpdate {
			now := env.clock.Now()

			return &models.IncidentUpdate{AcknowledgedAt: &now}
		},
		"snoozed": func(env *reminderEnv) *models.IncidentUpdate {
			until := env.clock.Now().Add(time.Hour)

			return &models.IncidentUpdate{SnoozedUntil: &until}
		},
		// Auto-resolve runs no sweep either.
		"auto-resolved": func(env *reminderEnv) *models.IncidentUpdate {
			now := env.clock.Now()
			state := models.IncidentStateResolved

			return &models.IncidentUpdate{ResolvedAt: &now, State: &state}
		},
		"paging-suppressed child": func(_ *reminderEnv) *models.IncidentUpdate {
			suppressed := true

			return &models.IncidentUpdate{PagingSuppressed: &suppressed}
		},
	}

	for name, update := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := newReminderEnv(t)
			env.update(t, update(env))
			env.run(t, 0)
			env.assertStopped(t)
		})
	}
}

func TestIncidentReminder_StopsWhenTurnedOff(t *testing.T) {
	t.Parallel()

	env := newReminderEnv(t)
	off := 0
	require.NoError(t, env.db.UpdateCheck(t.Context(), env.check.UID,
		&models.CheckUpdate{ReminderIntervalSeconds: &off}))

	env.run(t, 0)
	env.assertStopped(t)
}

// Paused and in-maintenance checks skip the send but keep the chain alive with
// the same seq, so reminders resume on their own afterwards.
func TestIncidentReminder_PausedOrInMaintenanceReschedulesSameSeq(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, env *reminderEnv){
		"paused": func(t *testing.T, env *reminderEnv) {
			t.Helper()

			disabled := false
			require.NoError(t, env.db.UpdateCheck(t.Context(), env.check.UID,
				&models.CheckUpdate{Enabled: &disabled}))
		},
		"maintenance": func(t *testing.T, env *reminderEnv) {
			t.Helper()

			window := models.NewMaintenanceWindow(env.org.UID, "upgrade",
				env.clock.Now().Add(-time.Minute), env.clock.Now().Add(time.Hour))
			require.NoError(t, env.db.CreateMaintenanceWindow(t.Context(), window))
			require.NoError(t, env.db.SetMaintenanceWindowChecks(t.Context(), window.UID,
				[]string{env.check.UID}, nil))
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newReminderEnv(t)
			setup(t, env)

			env.run(t, 2)

			r.Empty(env.pendingJobs(t, jobdef.JobTypeNotification), "no reminder while paused")
			r.Equal(0, env.reminderCount(t))

			next := env.pendingJobs(t, jobdef.JobTypeIncidentReminder)
			r.Len(next, 1)
			r.Equal(2, reminderSeqOf(t, next[0]), "the same seq is kept")
			r.True(next[0].ScheduledAt.Equal(env.clock.Now().Add(reminderTestInterval * time.Second)))
		})
	}
}

// Two jobs with the same seq run back to back: the compare-and-set lets exactly
// one send. The positive control is the first run, which does send.
func TestIncidentReminder_RaceSendsOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newReminderEnv(t)

	env.run(t, 0)
	r.Len(env.pendingJobs(t, jobdef.JobTypeNotification), 1, "positive control: a single job does send")

	env.run(t, 0)
	r.Len(env.pendingJobs(t, jobdef.JobTypeNotification), 1, "the losing job must not send a second set")
	r.Equal(1, env.reminderCount(t), "reminder_count went up by one")
	r.Len(env.pendingJobs(t, jobdef.JobTypeIncidentReminder), 1, "only the winner schedules the next reminder")
}

// Schedule-time filters: off, wrong kind, already handled.
func TestScheduleIncidentReminder_Filters(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := newReminderEnv(t)
	now := env.clock.Now()

	degraded := *env.incident
	degraded.Kind = models.IncidentKindDegraded
	r.NoError(ScheduleIncidentReminder(ctx, env.jobs, env.db, &degraded, env.check, now))

	burn := *env.incident
	burn.Kind = models.IncidentKindSLOBurn
	r.NoError(ScheduleIncidentReminder(ctx, env.jobs, env.db, &burn, env.check, now))

	acked := *env.incident
	acked.AcknowledgedAt = &now
	r.NoError(ScheduleIncidentReminder(ctx, env.jobs, env.db, &acked, env.check, now))

	off := *env.check
	zero := 0
	off.ReminderIntervalSeconds = &zero
	r.NoError(ScheduleIncidentReminder(ctx, env.jobs, env.db, env.incident, &off, now))

	r.Empty(env.pendingJobs(t, jobdef.JobTypeIncidentReminder))

	// Positive control.
	r.NoError(ScheduleIncidentReminder(ctx, env.jobs, env.db, env.incident, env.check, now))
	r.Len(env.pendingJobs(t, jobdef.JobTypeIncidentReminder), 1)

	canceled, err := CancelIncidentReminders(ctx, env.jobs, env.incident.UID)
	r.NoError(err)
	r.Equal(1, canceled)
	r.Empty(env.pendingJobs(t, jobdef.JobTypeIncidentReminder))
}

// The check value wins; nil inherits the org parameter; absent means off.
func TestReminderInterval_Resolution(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := newReminderEnv(t)

	inherit := *env.check
	inherit.ReminderIntervalSeconds = nil
	r.Zero(ReminderInterval(ctx, env.db, &inherit), "absent org parameter means off")

	r.NoError(env.db.SetOrgParameter(ctx, env.org.UID, models.ParamKeyIncidentReminderInterval,
		3600, false))
	r.Equal(time.Hour, ReminderInterval(ctx, env.db, &inherit))

	zero := 0
	inherit.ReminderIntervalSeconds = &zero
	r.Zero(ReminderInterval(ctx, env.db, &inherit), "a check value of 0 beats the org default")

	r.Equal(reminderTestInterval*time.Second, ReminderInterval(ctx, env.db, env.check))
}
