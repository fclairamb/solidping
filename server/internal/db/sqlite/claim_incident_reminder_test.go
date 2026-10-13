package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newReminderClaimIncident(t *testing.T) (*Service, *models.Incident) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("reminder-claim-org", "Reminder Claim Org")
	r.NoError(svc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "reminder-claim-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Hour), "down")
	r.NoError(svc.CreateIncident(ctx, incident))

	return svc, incident
}

// The compare-and-set wins once per expected count: a second job carrying the
// same seq must lose, or two jobs racing would both send the reminder.
func TestClaimIncidentReminderWinsOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, incident := newReminderClaimIncident(t)
	now := time.Now()

	won, err := svc.ClaimIncidentReminder(ctx, incident.UID, 0, now)
	r.NoError(err)
	r.True(won, "the first claim of seq 0 must win")

	won, err = svc.ClaimIncidentReminder(ctx, incident.UID, 0, now)
	r.NoError(err)
	r.False(won, "a second claim of the same seq must lose")

	stored, err := svc.GetIncident(ctx, incident.OrganizationUID, incident.UID)
	r.NoError(err)
	r.Equal(1, stored.ReminderCount)
	r.NotNil(stored.LastReminderAt)

	won, err = svc.ClaimIncidentReminder(ctx, incident.UID, 1, now)
	r.NoError(err)
	r.True(won, "the next seq wins again")
}

func TestClaimIncidentReminderRefusesHandledIncidents(t *testing.T) {
	t.Parallel()

	now := time.Now()
	later := now.Add(time.Hour)
	earlier := now.Add(-time.Minute)

	cases := []struct {
		name   string
		update models.IncidentUpdate
		want   bool
	}{
		{"open", models.IncidentUpdate{}, true},
		{"acknowledged", models.IncidentUpdate{AcknowledgedAt: &now}, false},
		{"resolved", models.IncidentUpdate{ResolvedAt: &now}, false},
		{"still snoozed", models.IncidentUpdate{SnoozedUntil: &later}, false},
		{"snooze expired", models.IncidentUpdate{SnoozedUntil: &earlier}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := require.New(t)
			svc, incident := newReminderClaimIncident(t)

			r.NoError(svc.UpdateIncident(ctx, incident.UID, &tc.update))

			won, err := svc.ClaimIncidentReminder(ctx, incident.UID, 0, now)
			r.NoError(err)
			r.Equal(tc.want, won)
		})
	}
}
