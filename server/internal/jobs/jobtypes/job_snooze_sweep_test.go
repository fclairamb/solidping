package jobtypes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// newExpiredSnooze is the reminder world with its incident snoozed until a
// minute ago: exactly what the sweep exists to wake up.
func newExpiredSnooze(t *testing.T) *reminderEnv {
	t.Helper()

	env := newReminderEnv(t)
	until := time.Now().Add(-time.Minute)
	reason := "lunch"
	env.update(t, &models.IncidentUpdate{SnoozedUntil: &until, SnoozeReason: &reason})

	return env
}

func (e *reminderEnv) snoozed(t *testing.T) bool {
	t.Helper()

	stored, err := e.db.GetIncident(t.Context(), e.org.UID, e.incident.UID)
	require.NoError(t, err)

	return stored.SnoozedUntil != nil
}

func (e *reminderEnv) unsnoozeEvents(t *testing.T) int {
	t.Helper()

	count, err := e.db.DB().NewSelect().TableExpr("events").
		Where("incident_uid = ?", e.incident.UID).
		Where("event_type = ?", string(models.EventTypeIncidentUnsnoozed)).
		Count(t.Context())
	require.NoError(t, err)

	return count
}

// pendingSweeps counts the pending jobs of an instance-wide (org-less) type.
func (e *reminderEnv) pendingSweeps(t *testing.T, jobType jobdef.JobType) int {
	t.Helper()

	count, err := e.db.DB().NewSelect().TableExpr("jobs").
		Where("type = ?", string(jobType)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Count(t.Context())
	require.NoError(t, err)

	return count
}

// An expired snooze is lifted, recorded as an `incident.unsnoozed` event, and
// the reminder chain the snooze had stopped starts again. One store call fails
// per case: a failure on one incident never fails the sweep, and the sweep
// always re-arms.
func TestSnoozeSweep_UnsnoozesAndSurvivesStoreFailures(t *testing.T) {
	t.Parallel()

	const unspecified = -1

	tests := []struct {
		name         string
		fail         func(env *reminderEnv) []string
		stillSnoozed bool
		events       int
		// reminders is the number of reminder jobs queued, or unspecified.
		reminders int
	}{
		{name: "control: nothing fails", events: 1, reminders: 1},
		{
			// Nothing changed: the next sweep finds the incident again.
			name:         "incident update fails: the incident stays snoozed for the next sweep",
			fail:         func(*reminderEnv) []string { return []string{`UPDATE "incidents"`} },
			stillSnoozed: true,
		},
		{
			name:      "unsnooze event insert fails: the snooze is lifted anyway",
			fail:      func(*reminderEnv) []string { return []string{`INSERT INTO "events"`} },
			reminders: unspecified,
		},
		{
			name: "incident reload fails: unsnoozed, no reminder",
			fail: func(env *reminderEnv) []string {
				return []string{`FROM "incidents"`, "uid = '" + env.incident.UID + "'"}
			},
			events: 1,
		},
		{
			name:   "check lookup fails: unsnoozed, no reminder",
			fail:   func(*reminderEnv) []string { return []string{`FROM "checks"`} },
			events: 1,
		},
		{
			name:   "reminder insert fails: unsnoozed, no reminder",
			fail:   func(*reminderEnv) []string { return []string{`INSERT INTO "jobs"`, `'incident_reminder'`} },
			events: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newExpiredSnooze(t)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(env.db.DB(), tt.fail(env)...)
			}

			r.NoError((&SnoozeSweepJobRun{}).Run(t.Context(), env.jctx))

			if fault != nil {
				fault.requireFired(t)
			}

			r.Equal(tt.stillSnoozed, env.snoozed(t))
			r.Equal(tt.events, env.unsnoozeEvents(t))
			r.Equal(1, env.pendingSweeps(t, jobdef.JobTypeSnoozeSweep), "the sweep re-arms itself")

			if tt.reminders != unspecified {
				r.Len(env.pendingJobs(t, jobdef.JobTypeIncidentReminder), tt.reminders)
			}
		})
	}
}

// The sweep cannot even list what to wake up: it asks for a retry and touches
// nothing.
func TestSnoozeSweep_ListingFailureIsRetryable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newExpiredSnooze(t)
	fault := failQueries(env.db.DB(), `FROM "incidents"`, "snoozed_until <=")

	err := (&SnoozeSweepJobRun{}).Run(t.Context(), env.jctx)
	fault.requireFired(t)

	r.ErrorContains(err, "list expired snoozes")
	r.ErrorIs(err, context.Canceled)
	r.True(jobdef.IsRetryable(err))
	r.True(env.snoozed(t))
	r.Zero(env.unsnoozeEvents(t))
}

// A snooze that has not expired is left alone. The expired one next to it is
// the positive control.
func TestSnoozeSweep_LeavesRunningSnoozesAlone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newExpiredSnooze(t)

	running := models.NewIncident(env.org.UID, env.check.UID, time.Now().Add(-time.Hour), "second outage")
	r.NoError(env.db.CreateIncident(t.Context(), running))

	until := time.Now().Add(time.Hour)
	r.NoError(env.db.UpdateIncident(t.Context(), running.UID, &models.IncidentUpdate{SnoozedUntil: &until}))

	r.NoError((&SnoozeSweepJobRun{}).Run(t.Context(), env.jctx))

	r.False(env.snoozed(t), "positive control: the expired snooze is lifted")

	stored, err := env.db.GetIncident(t.Context(), env.org.UID, running.UID)
	r.NoError(err)
	r.NotNil(stored.SnoozedUntil)
}

// Without a job service the sweep still lifts the snooze: it only loses the
// ability to queue the reminder and its own next run.
func TestSnoozeSweep_WithoutJobService(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newExpiredSnooze(t)
	env.jctx.Services = &services.Registry{Clock: env.clock}

	r.NoError((&SnoozeSweepJobRun{}).Run(t.Context(), env.jctx))

	r.False(env.snoozed(t))
	r.Equal(1, env.unsnoozeEvents(t))
	r.Empty(env.pendingJobs(t, jobdef.JobTypeIncidentReminder))
	r.Zero(env.pendingSweeps(t, jobdef.JobTypeSnoozeSweep))
}
