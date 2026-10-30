package postgres

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestClaimIncidentReminder_Postgres pins the reminder compare-and-set on a
// real PostgreSQL backend: concurrent claims of one seq produce exactly one
// winner, and a handled incident is never claimed (spec 2026-10-12-16).
func TestClaimIncidentReminder_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	org := models.NewOrganization("reminder-claim-org", "Reminder Claim Org")
	r.NoError(svc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "reminder-claim-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	newIncident := func() *models.Incident {
		incident := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Hour), "down")
		r.NoError(svc.CreateIncident(ctx, incident))

		return incident
	}

	now := time.Now()
	open := newIncident()

	const goroutines = 20

	var (
		wins atomic.Int64
		wg   sync.WaitGroup
	)

	for range goroutines {
		wg.Add(1)

		go func() {
			defer wg.Done()

			won, claimErr := svc.ClaimIncidentReminder(ctx, open.UID, 0, now)
			if claimErr == nil && won {
				wins.Add(1)
			}
		}()
	}

	wg.Wait()
	r.Equal(int64(1), wins.Load(), "exactly one concurrent claim of seq 0 wins")

	won, err := svc.ClaimIncidentReminder(ctx, open.UID, 0, now)
	r.NoError(err)
	r.False(won, "the same seq never wins twice")

	later := now.Add(time.Hour)

	for name, update := range map[string]models.IncidentUpdate{
		"acknowledged":  {AcknowledgedAt: &now},
		"resolved":      {ResolvedAt: &now},
		"still snoozed": {SnoozedUntil: &later},
	} {
		incident := newIncident()
		r.NoError(svc.UpdateIncident(ctx, incident.UID, &update))

		won, err := svc.ClaimIncidentReminder(ctx, incident.UID, 0, now)
		r.NoError(err)
		r.False(won, name)
	}
}
