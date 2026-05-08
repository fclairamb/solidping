package activation_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestEmitIsIdempotent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("act-org", "Act Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	for range 3 {
		activation.Emit(ctx, dbSvc, org.UID,
			models.EventTypeOrgActivationSignupCompleted, activation.SourceSystem, "")
	}

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		EventTypes: []models.EventType{
			models.EventTypeOrgActivationSignupCompleted,
		},
	})
	r.NoError(err)
	r.Len(events, 1)
}

func TestEmitRejectsNonActivationMilestone(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("act-org-bad", "Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Pass a non-activation event type — must be silently dropped, not
	// stored as a check.created row that pollutes the audit log.
	activation.Emit(ctx, dbSvc, org.UID,
		models.EventTypeCheckCreated, activation.SourceAPI, "")

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
	})
	r.NoError(err)
	r.Empty(events)
}

func TestEmitRecordsSourceAndActor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("act-meta", "Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("a@example.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	// Set up the milestones in the order a real activation funnel would.
	activation.Emit(ctx, dbSvc, org.UID,
		models.EventTypeOrgActivationFirstCheckCreated,
		activation.SourceEmptyState, user.UID)

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		EventTypes: []models.EventType{
			models.EventTypeOrgActivationFirstCheckCreated,
		},
	})
	r.NoError(err)
	r.Len(events, 1)

	got := events[0]
	r.Equal(models.ActorTypeUser, got.ActorType)
	r.NotNil(got.ActorUID)
	r.Equal(user.UID, *got.ActorUID)
	r.Equal("empty_state", got.Payload["source"])
}

// Ensure ctx cancellation does not cause panics inside Emit.
func TestEmitWithCancelledContext(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("act-cancel", "Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	// Should not panic — failure is logged, not raised.
	activation.Emit(cancelCtx, dbSvc, org.UID,
		models.EventTypeOrgActivationFirstResultReceived,
		activation.SourceSystem, "")
}
