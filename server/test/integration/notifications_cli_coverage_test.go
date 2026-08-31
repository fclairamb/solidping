package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// notifCovSeed is the fixture produced by seedNotificationRows.
type notifCovSeed struct {
	incidentUID   string
	connectionUID string
	userNotifUID  string
	connNotifUID  string
}

// seedNotificationRows inserts a check, incident, connection, and two
// notification audit rows (one user-targeted, one connection-targeted) for the
// primary test user, returning their UIDs.
func seedNotificationRows(ctx context.Context, t *testing.T, ts *TestServer) notifCovSeed {
	t.Helper()

	r := require.New(t)
	dbSvc := ts.Server.DBService()
	const testUserUID = "10000000-0000-0000-0000-000000000002" // seeded admin

	checkUID := "60000000-0000-0000-0000-000000000001"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-notif-check", "http", models.JSONMap{"url": "https://example.com"})

	title := "CLI notification incident"
	now := time.Now()
	incident := &models.Incident{
		UID:             "60000000-0000-0000-0000-000000000002",
		Kind:            models.IncidentKindCheck,
		OrganizationUID: cliCovOrgUID,
		CheckUID:        checkUID,
		Title:           &title,
		State:           models.IncidentStateActive,
		StartedAt:       now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	conn := models.NewIntegration(cliCovOrgUID, models.ConnectionTypeWebhook, "cli-notif-conn")
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	sentAt := time.Now()

	userRow := models.NewIncidentNotificationForUser(
		cliCovOrgUID, incident.UID, "incident.triggered",
		models.IncidentNotificationSourceEscalationUser,
		testUserUID, "email", nil, nil,
	)
	userRow.Status = models.IncidentNotificationStatusSent
	userRow.SentAt = &sentAt
	r.NoError(dbSvc.CreateIncidentNotification(ctx, userRow))

	connRow := models.NewIncidentNotificationForJob(
		cliCovOrgUID, incident.UID, "incident.triggered",
		models.IncidentNotificationSourceCheckConnection,
		conn.UID, "fake-job-uid", "webhook", nil, nil,
	)
	connRow.Status = models.IncidentNotificationStatusSent
	connRow.SentAt = &sentAt
	r.NoError(dbSvc.CreateIncidentNotification(ctx, connRow))

	return notifCovSeed{
		incidentUID:   incident.UID,
		connectionUID: conn.UID,
		userNotifUID:  userRow.UID,
		connNotifUID:  connRow.UID,
	}
}

// TestCLICoverage_NotificationsRead exercises the notification-delivery read
// operations at incident, org-by-connection, user, and self scopes.
func TestCLICoverage_NotificationsRead(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	seed := seedNotificationRows(ctx, t, ts)
	apiClient := cliCovAuthedClient(ctx, t, ts)
	incidentUID := uuid.MustParse(seed.incidentUID)

	// Per-incident list: both rows.
	incResp, err := apiClient.ListIncidentNotificationsWithResponse(
		ctx, TestOrgSlug, incidentUID, &client.ListIncidentNotificationsParams{})
	r.NoError(err)
	r.Equal(200, incResp.StatusCode())
	r.NotNil(incResp.JSON200)
	r.NotNil(incResp.JSON200.Data)
	r.Len(*incResp.JSON200.Data, 2)

	// Per-incident single notification.
	getIncResp, err := apiClient.GetIncidentNotificationWithResponse(
		ctx, TestOrgSlug, incidentUID, seed.userNotifUID)
	r.NoError(err)
	r.Equal(200, getIncResp.StatusCode())
	r.NotNil(getIncResp.JSON200)
	r.NotNil(getIncResp.JSON200.Uid)
	r.Equal(seed.userNotifUID, *getIncResp.JSON200.Uid)

	// Org-scoped single notification (no incident).
	getResp, err := apiClient.GetNotificationWithResponse(ctx, TestOrgSlug, seed.connNotifUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(seed.connNotifUID, *getResp.JSON200.Uid)

	// Org-scoped list by connection.
	byConnResp, err := apiClient.ListNotificationsWithResponse(
		ctx, TestOrgSlug, &client.ListNotificationsParams{ConnectionUid: seed.connectionUID})
	r.NoError(err)
	r.Equal(200, byConnResp.StatusCode())
	r.NotNil(byConnResp.JSON200)
	r.NotNil(byConnResp.JSON200.Data)
	r.Len(*byConnResp.JSON200.Data, 1)

	// Missing connectionUid is a 400.
	badResp, err := apiClient.ListNotificationsWithResponse(
		ctx, TestOrgSlug, &client.ListNotificationsParams{})
	r.NoError(err)
	r.Equal(400, badResp.StatusCode())

	// Self scope: the user-targeted row.
	meResp, err := apiClient.ListMyNotificationsWithResponse(
		ctx, TestOrgSlug, &client.ListMyNotificationsParams{})
	r.NoError(err)
	r.Equal(200, meResp.StatusCode())
	r.NotNil(meResp.JSON200)
	r.NotNil(meResp.JSON200.Data)
	r.NotEmpty(*meResp.JSON200.Data)

	// User scope (admin querying self).
	userResp, err := apiClient.ListUserNotificationsWithResponse(
		ctx, TestOrgSlug, "10000000-0000-0000-0000-000000000002", &client.ListUserNotificationsParams{})
	r.NoError(err)
	r.Equal(200, userResp.StatusCode())
	r.NotNil(userResp.JSON200)
	r.NotNil(userResp.JSON200.Data)
	r.NotEmpty(*userResp.JSON200.Data)
}

// TestCLICoverage_NotificationRoutesAndContacts exercises the per-user
// notification-routes and notification-contacts operations.
func TestCLICoverage_NotificationRoutesAndContacts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// Listing auto-seeds the default email route.
	listResp, err := apiClient.ListMyNotificationRoutesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	r.Len(*listResp.JSON200.Data, 1)
	r.Equal("email", (*listResp.JSON200.Data)[0].Contact.Type)

	// Add a phone contact (and its route).
	label := "Mobile"
	addResp, err := apiClient.AddMyNotificationContactWithResponse(ctx, TestOrgSlug,
		client.AddMyNotificationContactJSONRequestBody{Type: "phone", Value: "+33612345678", Label: &label})
	r.NoError(err)
	r.Equal(201, addResp.StatusCode())
	r.NotNil(addResp.JSON201)
	r.Equal("phone", addResp.JSON201.Contact.Type)
	r.Equal("+33612345678", addResp.JSON201.Contact.Value)

	routeUID := addResp.JSON201.Uid
	contactUID := addResp.JSON201.Contact.Uid

	// Disable the phone route.
	disabled := false
	updResp, err := apiClient.UpdateMyNotificationRouteWithResponse(ctx, TestOrgSlug, routeUID,
		client.UpdateMyNotificationRouteJSONRequestBody{Enabled: &disabled})
	r.NoError(err)
	r.Equal(200, updResp.StatusCode())
	r.NotNil(updResp.JSON200)
	r.False(updResp.JSON200.Enabled)

	// Test the phone route: no SMS provider is configured, so it 422s (the
	// operation and route wiring are still exercised end to end).
	testResp, err := apiClient.TestMyNotificationRouteWithResponse(ctx, TestOrgSlug, routeUID)
	r.NoError(err)
	r.Equal(422, testResp.StatusCode())

	// Remove the phone contact.
	delResp, err := apiClient.RemoveMyNotificationContactWithResponse(ctx, TestOrgSlug, contactUID)
	r.NoError(err)
	r.Equal(204, delResp.StatusCode())

	// The phone contact is gone; only the email route remains.
	afterResp, err := apiClient.ListMyNotificationRoutesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, afterResp.StatusCode())
	r.NotNil(afterResp.JSON200)
	r.NotNil(afterResp.JSON200.Data)

	for i := range *afterResp.JSON200.Data {
		r.NotEqual(contactUID, (*afterResp.JSON200.Data)[i].Contact.Uid)
	}
}
