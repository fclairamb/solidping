package checks_test

// Disabling a check with resolveOpenIncidents closes its open incidents with
// resolution type check_disabled (spec 2026-10-23-01).

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func TestDisableResolvesOpenIncidents(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	world := newDegradedDisableWorld(t, "disable-resolves")
	world.checks.SetCheckDisableIncidentResolver(world.incidents)

	check := models.NewCheck(world.org.UID, "acme", "http")
	r.NoError(world.dbSvc.CreateCheck(ctx, check))

	outage := models.NewIncident(world.org.UID, check.UID, time.Now().Add(-time.Hour), "acme is down")
	r.NoError(world.dbSvc.CreateIncident(ctx, outage))
	degraded := world.openDegraded(t, check.UID)

	// Positive control: disabling without the flag leaves both active.
	_, err := world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		Enabled: boolPtr(false),
	})
	r.NoError(err)
	r.Equal(models.IncidentStateActive, world.incident(t, outage.UID).State)
	r.Equal(models.IncidentStateActive, world.incident(t, degraded.UID).State)

	_, err = world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		Enabled:              boolPtr(false),
		ResolveOpenIncidents: boolPtr(true),
	})
	r.NoError(err)

	for _, uid := range []string{outage.UID, degraded.UID} {
		got := world.incident(t, uid)
		r.Equal(models.IncidentStateResolved, got.State)
		r.NotNil(got.ResolutionType)
		r.Equal(models.ResolutionTypeCheckDisabled, *got.ResolutionType)
		r.NotNil(got.ResolvedAt)

		events, err := world.dbSvc.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: world.org.UID,
			IncidentUID:     &uid,
			EventTypes:      []models.EventType{models.EventTypeIncidentResolved},
		})
		r.NoError(err)
		r.Len(events, 1, "one incident.resolved event per incident")
	}

	// A retry with nothing open is a no-op, and emits no second event.
	_, err = world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		Enabled:              boolPtr(false),
		ResolveOpenIncidents: boolPtr(true),
	})
	r.NoError(err)
}

func TestResolveOpenIncidentsWithoutDisableIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	world := newDegradedDisableWorld(t, "disable-rejects")
	world.checks.SetCheckDisableIncidentResolver(world.incidents)

	check := models.NewCheck(world.org.UID, "acme", "http")
	r.NoError(world.dbSvc.CreateCheck(ctx, check))
	outage := models.NewIncident(world.org.UID, check.UID, time.Now().Add(-time.Hour), "acme is down")
	r.NoError(world.dbSvc.CreateIncident(ctx, outage))

	for _, req := range []*checks.UpdateCheckRequest{
		{ResolveOpenIncidents: boolPtr(true)},
		{Enabled: boolPtr(true), ResolveOpenIncidents: boolPtr(true)},
	} {
		_, err := world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, req)
		r.Error(err)
	}

	r.Equal(models.IncidentStateActive, world.incident(t, outage.UID).State)
}
