package activation_test

import (
	"context"
	"testing"
	"time"

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

// TestEmitWithExtraPayloadAndRefs pins the extended Emit contract: extra
// payload data merges into Payload alongside "source", and the two reserved
// keys (_incident_uid, _check_uid) set the event row's IncidentUID/CheckUID
// instead of leaking into the stored payload. Mirrors how
// first_notification_configured (channel_uid/channel_name/channel_type) and
// first_incident_paged (_incident_uid/_check_uid/check_name) call Emit.
func TestEmitWithExtraPayloadAndRefs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("act-extra", "Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// events.incident_uid / events.check_uid are FK columns (references
	// incidents(uid) / checks(uid)) — real rows are required or the insert
	// below fails the FK constraint and Emit swallows the error (best-effort
	// by design), which would make this test pass vacuously on 0 events.
	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))
	incident := models.NewIncident(org.UID, check.UID, time.Now(), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	activation.Emit(ctx, dbSvc, org.UID,
		models.EventTypeOrgActivationFirstIncidentPaged,
		activation.SourceSystem, "", models.JSONMap{
			"_incident_uid": incident.UID,
			"_check_uid":    check.UID,
			"check_name":    "API Health",
		})

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		EventTypes: []models.EventType{
			models.EventTypeOrgActivationFirstIncidentPaged,
		},
	})
	r.NoError(err)
	r.Len(events, 1)

	got := events[0]
	r.NotNil(got.IncidentUID, "extra's _incident_uid must set the row's IncidentUID")
	r.Equal(incident.UID, *got.IncidentUID)
	r.NotNil(got.CheckUID, "extra's _check_uid must set the row's CheckUID")
	r.Equal(check.UID, *got.CheckUID)

	r.Equal("system", got.Payload["source"])
	r.Equal("API Health", got.Payload["check_name"])
	r.NotContains(got.Payload, "_incident_uid",
		"reserved key must not leak into the stored payload")
	r.NotContains(got.Payload, "_check_uid",
		"reserved key must not leak into the stored payload")
}

// TestEmitWithChannelPayload pins the first_notification_configured shape:
// channel_uid/channel_name/channel_type merge into Payload with no row-level
// UID side effects (no reserved keys involved for this milestone).
func TestEmitWithChannelPayload(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("act-channel", "Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	activation.Emit(ctx, dbSvc, org.UID,
		models.EventTypeOrgActivationFirstNotificationConfigured,
		activation.SourceAPI, "", models.JSONMap{
			"channel_uid":  "33333333-3333-3333-3333-333333333333",
			"channel_name": "Ops Slack",
			"channel_type": "slack",
		})

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		EventTypes: []models.EventType{
			models.EventTypeOrgActivationFirstNotificationConfigured,
		},
	})
	r.NoError(err)
	r.Len(events, 1)

	got := events[0]
	r.Nil(got.IncidentUID)
	r.Nil(got.CheckUID)
	r.Equal("api", got.Payload["source"])
	r.Equal("33333333-3333-3333-3333-333333333333", got.Payload["channel_uid"])
	r.Equal("Ops Slack", got.Payload["channel_name"])
	r.Equal("slack", got.Payload["channel_type"])
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
