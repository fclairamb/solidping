package incidents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func newHealthSetup(t *testing.T, checkType string) *validatingSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("components-test", "Components Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "app", checkType)
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &validatingSetup{svc: svc, dbSvc: dbSvc, org: org, check: check}
}

func (s *validatingSetup) submitFailed(t *testing.T, failed ...string) {
	t.Helper()

	result := models.NewResult(s.org.UID, s.check.UID, models.ResultStatusDown, 0)
	result.Output = models.JSONMap{
		checkerdef.OutputKeyError: "failing",
		"failed":                  failed,
	}

	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

func (s *validatingSetup) componentEvents(t *testing.T) []*models.Event {
	t.Helper()

	events, err := s.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		EventTypes:      []models.EventType{models.EventTypeIncidentComponentsChanged},
		Limit:           100,
	})
	require.NoError(t, err)

	return events
}

// A second component going down while the incident is open is an incident
// update, not silence; an unchanged set is not.
func TestHealthIncidentRecordsFailedComponentChanges(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newHealthSetup(t, "health")

	s.submitFailed(t, "Database")
	r.True(s.hasActiveIncident(t))

	incident, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	r.NoError(err)
	r.Equal([]any{"Database"}, incident.Details["failedComponents"])
	r.Empty(s.componentEvents(t), "the opening result is not a change")

	s.submitFailed(t, "Database")
	r.Empty(s.componentEvents(t), "the same set again is silent")

	s.submitFailed(t, "Cache", "Database")

	events := s.componentEvents(t)
	r.Len(events, 1)
	r.Equal([]any{"Cache"}, events[0].Payload["added"])
	r.Empty(events[0].Payload["removed"])
	r.Equal([]any{"Cache", "Database"}, events[0].Payload["failed"])

	incident, err = s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	r.NoError(err)
	r.Equal([]any{"Cache", "Database"}, incident.Details["failedComponents"])

	s.submitFailed(t, "Cache")

	events = s.componentEvents(t)
	r.Len(events, 2)
	r.Equal([]any{"Database"}, events[0].Payload["removed"], "newest first")
}

func TestComponentChangeIsHealthOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newHealthSetup(t, "http")

	s.submitFailed(t, "Database")
	s.submitFailed(t, "Cache")

	r.Empty(s.componentEvents(t))

	incident, err := s.dbSvc.FindActiveIncidentByCheckUID(t.Context(), s.check.UID)
	r.NoError(err)
	r.NotContains(incident.Details, "failedComponents")
}
