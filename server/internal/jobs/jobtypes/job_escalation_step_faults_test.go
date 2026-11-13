package jobtypes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// repeatingEscalation is a one-step policy that repeats after a minute and
// pages a webhook channel plus one user (who has no notification route, so
// they are reached by email).
type repeatingEscalation struct {
	*escalationFixture

	step *models.EscalationPolicyStep
}

func newRepeatingEscalation(t *testing.T) *repeatingEscalation {
	t.Helper()

	after := 60
	fixture := newEscalationFixture(t, 2, &after)

	return &repeatingEscalation{
		escalationFixture: fixture,
		step: fixture.setStep(t, nil,
			models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, uidPtr(fixture.channel.UID), 0),
			models.NewEscalationPolicyTarget("", models.EscalationTargetUser, uidPtr(fixture.user.UID), 1),
		),
	}
}

func (e *repeatingEscalation) escalatedEvents(t *testing.T) int {
	t.Helper()

	count := 0

	for _, eventType := range e.eventTypes(t) {
		if eventType == models.EventTypeIncidentEscalated {
			count++
		}
	}

	return count
}

// One store call fails per case. Loading what the step needs is fatal; from
// the fan-out on, each target, the audit event and the next cycle are
// independent, so one failure never costs the others.
func TestEscalationStep_StoreFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		fail          func(world *repeatingEscalation) []string
		wantErr       string
		emails        int
		notifications int
		nextCycles    int
		events        int
	}{
		{name: "control: nothing fails", emails: 1, notifications: 1, nextCycles: 1, events: 1},
		{
			name:    "policy lookup fails",
			fail:    func(*repeatingEscalation) []string { return []string{`FROM "escalation_policies"`} },
			wantErr: "load policy",
		},
		{
			name:    "target listing fails",
			fail:    func(*repeatingEscalation) []string { return []string{`FROM "escalation_policy_targets"`} },
			wantErr: "list targets",
		},
		{
			name:          "user lookup fails: the channel is still paged",
			fail:          func(*repeatingEscalation) []string { return []string{`FROM "users"`} },
			notifications: 1, nextCycles: 1, events: 1,
		},
		{
			name:   "notification job insert fails: the user is still paged",
			fail:   func(*repeatingEscalation) []string { return []string{`INSERT INTO "jobs"`, `'notification'`} },
			emails: 1, nextCycles: 1, events: 1,
		},
		{
			name:   "escalated event insert fails: everybody is paged anyway",
			fail:   func(*repeatingEscalation) []string { return []string{`INSERT INTO "events"`} },
			emails: 1, notifications: 1, nextCycles: 1,
		},
		{
			// The page went out; a missing repeat is logged, not returned,
			// or the retry would page everybody a second time.
			name: "step listing for the next cycle fails: this cycle is not retried",
			fail: func(world *repeatingEscalation) []string {
				return []string{`FROM "escalation_policy_steps"`, "policy_uid = '" + world.policy.UID + "'"}
			},
			emails: 1, notifications: 1, events: 1,
		},
		{
			name: "next cycle insert fails: this cycle is not retried",
			fail: func(*repeatingEscalation) []string {
				return []string{`INSERT INTO "jobs"`, `'escalation_step'`}
			},
			emails: 1, notifications: 1, events: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			world := newRepeatingEscalation(t)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(world.env.db.DB(), tt.fail(world)...)
			}

			err := world.run(world.step.UID, 0, true)

			if fault != nil {
				fault.requireFired(t)
			}

			if tt.wantErr == "" {
				r.NoError(err)
			} else {
				r.ErrorContains(err, tt.wantErr)
				r.ErrorIs(err, context.Canceled)
			}

			r.Equal(tt.emails, world.env.emails.sends())
			r.Equal(tt.notifications, world.jobCount(t, jobdef.JobTypeNotification))
			r.Equal(tt.nextCycles, world.jobCount(t, jobdef.JobTypeEscalationStep))
			r.Equal(tt.events, world.escalatedEvents(t))
		})
	}
}

// The injected clock decides both whether an incident still needs paging and
// when the next cycle fires.
func TestEscalationStep_UsesTheInjectedClock(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	world := newRepeatingEscalation(t)
	ctx := t.Context()

	// Snoozed for the next hour by the wall clock.
	until := time.Now().Add(time.Hour)
	r.NoError(world.env.db.UpdateIncident(ctx, world.env.incident.UID, &models.IncidentUpdate{SnoozedUntil: &until}))

	r.NoError(world.run(world.step.UID, 0, true))
	r.Zero(world.env.emails.sends(), "positive control: a snoozed incident pages nobody")

	// Two hours later by the injected clock the snooze is over.
	later := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	world.env.jctx.Services.Clock = clock.NewFake(later)

	r.NoError(world.run(world.step.UID, 0, true))
	r.Equal(1, world.env.emails.sends())

	var next []*models.Job
	r.NoError(world.env.db.DB().NewSelect().Model(&next).
		Where("type = ?", string(jobdef.JobTypeEscalationStep)).Scan(ctx))
	r.Len(next, 1)
	r.True(next[0].ScheduledAt.Equal(later.Add(time.Minute)),
		"the next cycle fires repeat_after after the injected now (got %s)", next[0].ScheduledAt)
}

// refusingEmail fails every send.
type refusingEmail struct{ calls int }

func (m *refusingEmail) Send(context.Context, *email.Message) (*email.SendResult, error) {
	m.calls++

	return nil, errTestBoom
}

// An email that cannot be sent marks its audit row failed with the reason,
// and the rest of the step goes on.
func TestEscalationStep_EmailFailureIsAudited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	world := newRepeatingEscalation(t)
	refusing := &refusingEmail{}
	world.env.jctx.Services.EmailSender = refusing

	r.NoError(world.run(world.step.UID, 0, true))
	r.Equal(1, refusing.calls)

	var rows []struct {
		Status string  `bun:"status"`
		Error  *string `bun:"error"`
	}

	r.NoError(world.env.db.DB().NewSelect().TableExpr("incident_notifications").
		Column("status", "error").
		Where("user_uid = ?", world.user.UID).Scan(t.Context(), &rows))
	r.Len(rows, 1)
	r.Equal(models.IncidentNotificationStatusFailed, rows[0].Status)
	r.NotNil(rows[0].Error)
	r.Contains(*rows[0].Error, errTestBoom.Error())

	r.Equal(1, world.jobCount(t, jobdef.JobTypeNotification), "the channel is still paged")
	r.Equal(1, world.jobCount(t, jobdef.JobTypeEscalationStep), "the next cycle is still queued")
}

// Without an email sender or formatter there is no way to reach the user: the
// step still succeeds and leaves no audit row claiming a page.
func TestEscalationStep_WithoutEmailSender(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	world := newRepeatingEscalation(t)
	world.env.jctx.Services.EmailSender = nil

	r.NoError(world.run(world.step.UID, 0, true))

	audits, err := world.env.db.DB().NewSelect().TableExpr("incident_notifications").
		Where("user_uid = ?", world.user.UID).Count(t.Context())
	r.NoError(err)
	r.Zero(audits)
	r.Equal(1, world.jobCount(t, jobdef.JobTypeNotification))
}

// An "all admins" target whose member listing fails pages nobody and does not
// fail the step.
func TestEscalationStep_AllAdminsListingFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fixture := newEscalationFixture(t, 0, nil)
	ctx := t.Context()

	r.NoError(fixture.env.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(fixture.env.org.UID, fixture.user.UID, models.MemberRoleAdmin)))

	step := fixture.setStep(t, nil,
		models.NewEscalationPolicyTarget("", models.EscalationTargetAllAdmins, nil, 0))

	r.NoError(fixture.run(step.UID, 0, false))
	r.Equal(1, fixture.env.emails.sends(), "positive control: the admin is emailed")

	fault := failQueries(fixture.env.db.DB(), `FROM "organization_members"`)
	r.NoError(fixture.run(step.UID, 1, false))
	fault.requireFired(t)
	r.Equal(1, fixture.env.emails.sends(), "no second email")
}

func TestScheduleEscalationCycle_StopsAtTheFirstRefusedStep(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jobs := &recordingJobs{err: errTestBoom}
	steps := []*models.EscalationPolicyStep{{UID: "one"}, {UID: "two"}}

	err := ScheduleEscalationCycle(t.Context(), jobs, &models.Incident{UID: "incident"},
		&models.EscalationPolicy{UID: "policy"}, steps, time.Now(), 1, sweepJctx(nil).Logger)
	r.ErrorIs(err, errTestBoom)
	r.Len(jobs.calls, 1, "the second step is not queued behind a missing first one")
}
