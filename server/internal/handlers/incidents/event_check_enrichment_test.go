package incidents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// enrichmentSetup mirrors validatingSetup but names the check so
// check_name enrichment assertions have something to check against
// (NewCheck leaves Name nil; production checks always have one).
type enrichmentSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	check *models.Check
}

func newEnrichmentSetup(t *testing.T, escalationThreshold int) *enrichmentSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("enrich-test", "Enrich Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	name := "API Health"
	check.Name = &name
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0
	if escalationThreshold > 0 {
		check.EscalationThreshold = escalationThreshold
	}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &enrichmentSetup{svc: svc, dbSvc: dbSvc, org: org, check: check}
}

func (s *enrichmentSetup) submit(t *testing.T, status models.ResultStatus) {
	t.Helper()
	result := models.NewResult(s.org.UID, s.check.UID, status, 0)
	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

// activeIncident returns the check's current active incident, failing the
// test if there isn't one.
func (s *enrichmentSetup) activeIncident(t *testing.T) *models.Incident {
	t.Helper()
	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	require.NoError(t, err)
	return inc
}

// requireCheckEnrichment asserts the standard shape every incident lifecycle
// event must carry per the "Recent activity" spec: CheckUID set on the row,
// and check_uid/check_slug/check_name all present in the payload.
func requireCheckEnrichment(t *testing.T, r *require.Assertions, e *models.Event, check *models.Check) {
	t.Helper()
	r.NotNil(e.CheckUID, "event %s must have CheckUID set on the row", e.EventType)
	r.Equal(check.UID, *e.CheckUID)

	r.Equal(check.UID, e.Payload["check_uid"], "event %s payload check_uid", e.EventType)
	r.NotNil(check.Slug)
	r.Equal(*check.Slug, e.Payload["check_slug"], "event %s payload check_slug", e.EventType)
	r.NotNil(check.Name)
	r.Equal(*check.Name, e.Payload["check_name"], "event %s payload check_name", e.EventType)
}

func findEvent(events []*models.Event, eventType models.EventType) *models.Event {
	for _, e := range events {
		if e.EventType == eventType {
			return e
		}
	}
	return nil
}

// TestIncidentCreatedEventIsCheckEnriched pins CheckUID + check_uid/slug/name
// on the incident.created row emitted when a check first fails.
func TestIncidentCreatedEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newEnrichmentSetup(t, 0)

	s.submit(t, models.ResultStatusDown)

	inc := s.activeIncident(t)
	events := listIncidentEvents(t, s.dbSvc, s.org.UID, inc.UID)
	created := findEvent(events, models.EventTypeIncidentCreated)
	r.NotNil(created, "must find an incident.created event")
	requireCheckEnrichment(t, r, created, s.check)
}

// TestIncidentResolvedEventIsCheckEnriched pins the same enrichment on the
// auto-resolve path (a success result closing an open incident).
func TestIncidentResolvedEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newEnrichmentSetup(t, 0)

	s.submit(t, models.ResultStatusDown)
	inc := s.activeIncident(t)

	s.submit(t, models.ResultStatusUp)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, inc.UID)
	resolved := findEvent(events, models.EventTypeIncidentResolved)
	r.NotNil(resolved, "must find an incident.resolved event")
	requireCheckEnrichment(t, r, resolved, s.check)
}

// TestIncidentEscalatedEventIsCheckEnriched pins enrichment on the escalation
// event, which previously had NO check_uid/slug/name at all (only
// failure_count/escalation_threshold).
func TestIncidentEscalatedEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	// Escalate on the 2nd consecutive failure.
	s := newEnrichmentSetup(t, 2)

	s.submit(t, models.ResultStatusDown)
	inc := s.activeIncident(t)
	s.submit(t, models.ResultStatusDown)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, inc.UID)
	escalated := findEvent(events, models.EventTypeIncidentEscalated)
	r.NotNil(escalated, "must find an incident.escalated event")
	requireCheckEnrichment(t, r, escalated, s.check)
}

// TestIncidentReopenedEventIsCheckEnriched drives resolve -> immediate
// re-failure (well within the default reopen cooldown window, which is at
// least 2 minutes) and pins enrichment on the resulting incident.reopened
// event rather than a fresh incident.created.
func TestIncidentReopenedEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newEnrichmentSetup(t, 0)

	s.submit(t, models.ResultStatusDown)
	firstIncidentUID := s.activeIncident(t).UID
	s.submit(t, models.ResultStatusUp)

	// Re-fail immediately - within the default cooldown, so this reopens the
	// same incident instead of creating a new one.
	s.submit(t, models.ResultStatusDown)
	reopened := s.activeIncident(t)
	r.Equal(firstIncidentUID, reopened.UID, "re-failure within cooldown reopens, not recreates")

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, reopened.UID)
	reopenedEvent := findEvent(events, models.EventTypeIncidentReopened)
	r.NotNil(reopenedEvent, "must find an incident.reopened event")
	requireCheckEnrichment(t, r, reopenedEvent, s.check)
}

// TestAcknowledgeEventIsCheckEnriched pins the headline gap called out in the
// spec: incident.acknowledged previously had neither CheckUID on the row nor
// any check_* payload fields at all.
func TestAcknowledgeEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	name := "API Health"
	s.check.Name = &name
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{Name: &name}))

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	ack := findEvent(events, models.EventTypeIncidentAcknowledged)
	r.NotNil(ack, "must find an incident.acknowledged event")
	requireCheckEnrichment(t, r, ack, s.check)
}

// TestUnacknowledgeEventIsCheckEnriched mirrors the ack test for the
// unacknowledge path.
func TestUnacknowledgeEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	name := "API Health"
	s.check.Name = &name
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{Name: &name}))

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	unack := findEvent(events, models.EventTypeIncidentUnacknowledged)
	r.NotNil(unack, "must find an incident.unacknowledged event")
	requireCheckEnrichment(t, r, unack, s.check)
}

// TestSnoozeEventIsCheckEnriched pins enrichment on the snooze path.
func TestSnoozeEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	name := "API Health"
	s.check.Name = &name
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{Name: &name}))

	dur := time.Hour
	_, err := s.svc.SnoozeIncident(ctx, s.org.Slug, &incidents.SnoozeIncidentRequest{
		IncidentUID: s.incident.UID,
		Duration:    &dur,
		Via:         "web",
	})
	r.NoError(err)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	snoozed := findEvent(events, models.EventTypeIncidentSnoozed)
	r.NotNil(snoozed, "must find an incident.snoozed event")
	requireCheckEnrichment(t, r, snoozed, s.check)
}

// TestUnsnoozeEventIsCheckEnriched pins enrichment on the unsnooze path.
func TestUnsnoozeEventIsCheckEnriched(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	name := "API Health"
	s.check.Name = &name
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{Name: &name}))

	dur := time.Hour
	_, err := s.svc.SnoozeIncident(ctx, s.org.Slug, &incidents.SnoozeIncidentRequest{
		IncidentUID: s.incident.UID,
		Duration:    &dur,
		Via:         "web",
	})
	r.NoError(err)

	_, err = s.svc.UnsnoozeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	unsnoozed := findEvent(events, models.EventTypeIncidentUnsnoozed)
	r.NotNil(unsnoozed, "must find an incident.unsnoozed event")
	requireCheckEnrichment(t, r, unsnoozed, s.check)
}

// TestHistoricalIncidentEventDegradesGracefully pins the "no backfill, must
// degrade gracefully" requirement at the model layer: an event row built the
// old way (payload with check_uid/check_slug only, no check_name, no row
// CheckUID) round-trips through ListEvents unchanged — nothing in the
// backend retroactively rewrites old payloads.
func TestHistoricalIncidentEventDegradesGracefully(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	// Simulate a pre-enrichment event: no CheckUID on the row, payload has
	// only check_uid + check_slug (the old shape), no check_name.
	old := models.NewEvent(s.org.UID, models.EventTypeIncidentAcknowledged, models.ActorTypeSystem)
	old.IncidentUID = &s.incident.UID
	old.Payload = models.JSONMap{
		"check_uid":  s.check.UID,
		"check_slug": *s.check.Slug,
	}
	r.NoError(s.dbSvc.CreateEvent(ctx, old))

	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	found := findEvent(events, models.EventTypeIncidentAcknowledged)
	r.NotNil(found)
	r.Nil(found.CheckUID, "old event has no row-level CheckUID — must not be synthesized")
	r.NotContains(found.Payload, "check_name", "old event payload must not gain check_name retroactively")
	r.Equal(s.check.UID, found.Payload["check_uid"])
}
