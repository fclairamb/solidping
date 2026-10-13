package incidents

// Reminders (spec 2026-10-12-16): which incident transitions schedule, cancel
// and restart the incident_reminder job. The job itself is covered in
// jobtypes/job_incident_reminder_test.go.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

const reminderOrgDefault = 3600

type reminderSetup struct {
	svc   *Service
	db    *sqlite.Service
	jobs  jobsvc.Service
	org   *models.Organization
	check *models.Check
}

// newReminderSetup builds a check that opens and resolves on the first signal,
// bound to one webhook channel, in an org whose reminder default is 1 h.
// checkInterval nil inherits that default.
func newReminderSetup(t *testing.T, checkInterval *int) *reminderSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	org := models.NewOrganization("reminder-test", "Reminder Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, models.ParamKeyIncidentReminderInterval,
		reminderOrgDefault, false))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0
	check.ReminderIntervalSeconds = checkInterval
	r.NoError(dbSvc.CreateCheck(ctx, check))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "ops")
	conn.Enabled = true
	r.NoError(dbSvc.CreateChannel(ctx, conn))
	r.NoError(dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(check.UID, conn.UID, org.UID)))

	return &reminderSetup{
		svc: NewService(dbSvc, jobs, clock.Real{}, nil), db: dbSvc, jobs: jobs, org: org, check: check,
	}
}

func (s *reminderSetup) submit(t *testing.T, status models.ResultStatus) {
	t.Helper()

	result := models.NewResult(s.org.UID, s.check.UID, status, 0)
	require.NoError(t, s.db.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

func (s *reminderSetup) incident(t *testing.T) *models.Incident {
	t.Helper()

	inc, err := s.db.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	require.NoError(t, err)

	return inc
}

func (s *reminderSetup) pending(t *testing.T, jobType jobdef.JobType) []*models.Job {
	t.Helper()

	var jobs []*models.Job
	require.NoError(t, s.db.DB().NewSelect().Model(&jobs).
		Where("organization_uid = ?", s.org.UID).
		Where("type = ?", string(jobType)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context()))

	return jobs
}

func (s *reminderSetup) reminders(t *testing.T) []*models.Job {
	t.Helper()

	return s.pending(t, jobdef.JobTypeIncidentReminder)
}

// requireDueAround asserts the single pending reminder is due the org default after
// `from` (a few seconds of slack for the real clock).
func (s *reminderSetup) requireDueAround(t *testing.T, from time.Time) {
	t.Helper()

	r := require.New(t)
	reminders := s.reminders(t)
	r.Len(reminders, 1)
	r.WithinDuration(from.Add(reminderOrgDefault*time.Second), reminders[0].ScheduledAt, 5*time.Second)
}

func TestReminder_OpenSchedulesFromOrgDefault(t *testing.T) {
	t.Parallel()

	s := newReminderSetup(t, nil)
	openedAt := time.Now()
	s.submit(t, models.ResultStatusDown)

	s.requireDueAround(t, openedAt)
}

func TestReminder_CheckZeroBeatsOrgDefault(t *testing.T) {
	t.Parallel()

	off := 0
	s := newReminderSetup(t, &off)
	s.submit(t, models.ResultStatusDown)

	require.NotNil(t, s.incident(t), "the incident did open")
	require.Empty(t, s.reminders(t), "a check value of 0 turns reminders off whatever the org says")
}

func TestReminder_DegradedAndBurnIncidentsNeverSchedule(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newReminderSetup(t, nil)

	pct := 88.3
	_, err := s.svc.OpenDegradedIncident(ctx, &OpenDegradedIncidentRequest{
		Check:     s.check,
		StartedAt: time.Now().Add(-30 * time.Minute),
		Title:     "api is degraded",
		Snapshot: &DegradedSnapshot{
			Failures: 5, FailuresWindow: 60, FailureSlots: 60, FailureMatches: 7,
			FailuresFired: true, AvailabilityPct: &pct, ResolveWindow: 60, CurrentlyUp: true,
		},
	})
	r.NoError(err)
	r.Empty(s.reminders(t), "a degraded incident never reminds")

	burn := models.NewIncident(s.org.UID, s.check.UID, time.Now(), "budget burning")
	burn.Kind = models.IncidentKindSLOBurn
	r.NoError(s.db.CreateIncident(ctx, burn))
	s.svc.scheduleFollowUps(ctx, s.org.UID, s.check.UID, models.EventTypeIncidentCreated, burn)
	r.Empty(s.reminders(t), "an SLO burn incident never reminds")

	// Positive control: a check outage on the same check does.
	outage := models.NewIncident(s.org.UID, s.check.UID, time.Now(), "api is down")
	r.NoError(s.db.CreateIncident(ctx, outage))
	s.svc.scheduleFollowUps(ctx, s.org.UID, s.check.UID, models.EventTypeIncidentCreated, outage)
	r.Len(s.reminders(t), 1)
}

// Ack cancels the pending reminder. A later auto-resolve still tells the
// channels (spec item 8: resolve after ack is unchanged).
func TestReminder_AckCancelsAndResolveStillNotifies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newReminderSetup(t, nil)

	s.submit(t, models.ResultStatusDown)
	r.Len(s.reminders(t), 1)

	inc := s.incident(t)
	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &AcknowledgeIncidentRequest{IncidentUID: inc.UID, Via: "web"})
	r.NoError(err)
	r.Empty(s.reminders(t), "ack cancels the pending reminder")

	s.submit(t, models.ResultStatusUp)

	resolvedNotices := 0

	for _, job := range s.pending(t, jobdef.JobTypeNotification) {
		if job.Config["eventType"] == string(models.EventTypeIncidentResolved) {
			resolvedNotices++
		}
	}

	r.Equal(1, resolvedNotices, "incident.resolved still goes to the channel after an ack")
	r.Empty(s.reminders(t))
}

func TestReminder_UnackRestartsFromTheUnack(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newReminderSetup(t, nil)

	s.submit(t, models.ResultStatusDown)
	inc := s.incident(t)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &AcknowledgeIncidentRequest{IncidentUID: inc.UID, Via: "web"})
	r.NoError(err)
	r.Empty(s.reminders(t))

	unackedAt := time.Now()
	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, inc.UID, "", "web")
	r.NoError(err)

	s.requireDueAround(t, unackedAt)
}

// Snooze cancels every pending reminder, whatever its due time. Snoozing also
// acknowledges: reminders come back on unsnooze only when that acknowledgment
// was withdrawn in between.
func TestReminder_SnoozeCancelsAndManualUnsnoozeRestarts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newReminderSetup(t, nil)

	s.submit(t, models.ResultStatusDown)
	inc := s.incident(t)
	r.Len(s.reminders(t), 1)

	// A 2 h snooze: the reminder due in 1 h is before `until` and the generic
	// sweep would catch it; extend it past `until` to prove the dedicated
	// cancel does not depend on the due time.
	r.NoError(jobtypes.ScheduleIncidentReminder(ctx, s.jobs, s.db, inc, s.check, time.Now().Add(3*time.Hour)))

	until := time.Now().Add(2 * time.Hour)
	_, err := s.svc.SnoozeIncident(ctx, s.org.Slug, &SnoozeIncidentRequest{
		IncidentUID: inc.UID, Until: &until, Via: "web",
	})
	r.NoError(err)
	r.Empty(s.reminders(t), "snooze cancels every pending reminder")

	// Unsnoozing while the snooze's own acknowledgment stands: nothing.
	_, err = s.svc.UnsnoozeIncident(ctx, s.org.Slug, inc.UID, "", "web")
	r.NoError(err)
	r.Empty(s.reminders(t), "still acknowledged: no reminder")

	// Snooze again, withdraw the acknowledgment, then unsnooze: reminders
	// restart counted from the unsnooze.
	_, err = s.svc.SnoozeIncident(ctx, s.org.Slug, &SnoozeIncidentRequest{
		IncidentUID: inc.UID, Until: &until, Via: "web",
	})
	r.NoError(err)
	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, inc.UID, "", "web")
	r.NoError(err)
	r.Empty(s.reminders(t), "unack while snoozed schedules nothing yet")

	unsnoozedAt := time.Now()
	_, err = s.svc.UnsnoozeIncident(ctx, s.org.Slug, inc.UID, "", "web")
	r.NoError(err)

	s.requireDueAround(t, unsnoozedAt)
}

// The snooze sweep job (which writes to the database directly) restarts
// reminders the same way.
func TestReminder_SnoozeSweepRestarts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newReminderSetup(t, nil)

	s.submit(t, models.ResultStatusDown)
	inc := s.incident(t)

	_, err := jobtypes.CancelIncidentReminders(ctx, s.jobs, inc.UID)
	r.NoError(err)

	expired := time.Now().Add(-time.Minute)
	r.NoError(s.db.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{SnoozedUntil: &expired}))

	runner, err := (&jobtypes.SnoozeSweepJobDefinition{}).CreateJobRun(nil)
	r.NoError(err)

	sweptAt := time.Now()
	r.NoError(runner.Run(ctx, &jobdef.JobContext{
		DBService: s.db, Logger: slog.Default(), Services: &services.Registry{Jobs: s.jobs},
	}))

	stored, err := s.db.GetIncident(ctx, s.org.UID, inc.UID)
	r.NoError(err)
	r.Nil(stored.SnoozedUntil, "the sweep did unsnooze")

	s.requireDueAround(t, sweptAt)
}

func TestReminder_ReopenSchedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newReminderSetup(t, nil)

	s.submit(t, models.ResultStatusDown)
	inc := s.incident(t)
	s.submit(t, models.ResultStatusUp)

	_, err := jobtypes.CancelIncidentReminders(ctx, s.jobs, inc.UID)
	r.NoError(err)
	r.Empty(s.reminders(t))

	reopenedAt := time.Now()
	s.submit(t, models.ResultStatusDown)

	reopened := s.incident(t)
	r.Equal(inc.UID, reopened.UID, "the relapse reopened the same incident")
	s.requireDueAround(t, reopenedAt)
}
