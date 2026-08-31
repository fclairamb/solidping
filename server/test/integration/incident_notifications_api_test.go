package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// setupIncidentNotificationRows seeds an incident with notification audit rows
// for one user and one connection, then returns the incident UID.
func setupIncidentNotificationRows(t *testing.T, ts *TestServer) string {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	dbSvc := ts.Server.DBService()

	// Reuse the test org from the test server setup.
	orgUID := "10000000-0000-0000-0000-000000000001"

	// Create a check.
	checkName := "notif-test-check"
	checkSlug := "notif-test-check"
	check := &models.Check{
		UID:             "40000000-0000-0000-0000-000000000001",
		OrganizationUID: orgUID,
		Name:            &checkName,
		Slug:            &checkSlug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Create an incident.
	title := "Notification Test Incident"
	incident := &models.Incident{
		UID:             "40000000-0000-0000-0000-000000000002",
		Kind:            models.IncidentKindCheck,
		OrganizationUID: orgUID,
		CheckUID:        check.UID,
		Title:           &title,
		State:           models.IncidentStateActive,
		StartedAt:       time.Now(),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	// Use the test user (admin) as the notification target.
	const testUserUID = "10000000-0000-0000-0000-000000000002" // pre-seeded by testhelper

	// Create a channel (connection).
	conn := models.NewIntegration(orgUID, models.ConnectionTypeWebhook, "notif-test-conn")
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	// Seed a user notification row (status=sent).
	userRow := models.NewIncidentNotificationForUser(
		orgUID, incident.UID, "incident.triggered",
		models.IncidentNotificationSourceEscalationUser,
		testUserUID, "email", nil, nil,
	)
	sentAt := time.Now()
	userRow.Status = models.IncidentNotificationStatusSent
	userRow.SentAt = &sentAt
	r.NoError(dbSvc.CreateIncidentNotification(ctx, userRow))

	// Seed a connection notification row (status=sent).
	connRow := models.NewIncidentNotificationForJob(
		orgUID, incident.UID, "incident.triggered",
		models.IncidentNotificationSourceCheckConnection,
		conn.UID, "fake-job-uid", "webhook", nil, nil,
	)
	connRow.Status = models.IncidentNotificationStatusSent
	connRow.SentAt = &sentAt
	r.NoError(dbSvc.CreateIncidentNotification(ctx, connRow))

	return incident.UID
}

// TestListIncidentNotifications verifies the per-incident listing endpoint.
func TestListIncidentNotifications(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	incidentUID := setupIncidentNotificationRows(t, ts)

	path := "/api/v1/orgs/" + TestOrgSlug + "/incidents/" + incidentUID + "/notifications"
	status, raw := authedRequest(t, ts, http.MethodGet, path)

	r := require.New(t)
	r.Equal(http.StatusOK, status)

	var resp map[string]any
	r.NoError(json.Unmarshal(raw, &resp))

	data, ok := resp["data"].([]any)
	r.True(ok, "expected data array")
	r.Len(data, 2, "expected two notification rows")
}

// TestListIncidentNotificationsStatusFilter verifies the ?status= filter.
func TestListIncidentNotificationsStatusFilter(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	incidentUID := setupIncidentNotificationRows(t, ts)

	path := "/api/v1/orgs/" + TestOrgSlug + "/incidents/" + incidentUID + "/notifications?status=sent"
	status, raw := authedRequest(t, ts, http.MethodGet, path)

	r := require.New(t)
	r.Equal(http.StatusOK, status)

	var resp map[string]any
	r.NoError(json.Unmarshal(raw, &resp))

	data, ok := resp["data"].([]any)
	r.True(ok, "expected data array")
	r.Len(data, 2, "both rows are sent")
}

// TestListUserNotificationsForSelf verifies that the /me/notifications endpoint
// returns the rows targeting the authenticated user.
func TestListUserNotificationsForSelf(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	setupIncidentNotificationRows(t, ts)

	path := "/api/v1/orgs/" + TestOrgSlug + "/me/notifications"
	status, raw := authedRequest(t, ts, http.MethodGet, path)

	r := require.New(t)
	r.Equal(http.StatusOK, status)

	var resp map[string]any
	r.NoError(json.Unmarshal(raw, &resp))

	data, ok := resp["data"].([]any)
	r.True(ok, "expected data array")
	r.NotEmpty(data, "expected at least one notification row for the test user")
}

// TestListUserNotificationsNonAdminForbidden verifies that a non-admin cannot
// query another user's notifications via /users/:uid/notifications.
func TestListUserNotificationsNonAdminForbidden(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	ctx := t.Context()
	r := require.New(t)
	dbSvc := ts.Server.DBService()

	// Create a second, non-admin user.
	now := time.Now()
	nonAdmin := &models.User{
		UID:       "50000000-0000-0000-0000-000000000001",
		Email:     "nonadmin@example.com",
		Name:      "Non Admin",
		CreatedAt: now, UpdatedAt: now,
	}
	r.NoError(dbSvc.CreateUser(ctx, nonAdmin))

	orgUID := "10000000-0000-0000-0000-000000000001"
	membership := &models.OrganizationMember{
		UID:             "50000000-0000-0000-0000-000000000002",
		UserUID:         nonAdmin.UID,
		OrganizationUID: orgUID,
		Role:            models.MemberRoleUser, // not admin
		JoinedAt:        &now, CreatedAt: now, UpdatedAt: now,
	}
	r.NoError(dbSvc.CreateOrganizationMember(ctx, membership))

	// Issue a PAT for the non-admin user.
	pat := &models.UserToken{
		UID:             "50000000-0000-0000-0000-000000000003",
		UserUID:         nonAdmin.UID,
		OrganizationUID: &orgUID,
		Token:           "pat_nonadmin",
		Type:            models.TokenTypePAT,
		Properties:      models.JSONMap{"name": "Non-Admin PAT"},
		CreatedAt:       now, UpdatedAt: now,
	}
	r.NoError(dbSvc.CreateUserToken(ctx, pat))

	// The admin user's UID that the non-admin should not be able to query.
	adminUID := "10000000-0000-0000-0000-000000000002"

	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodGet,
		ts.HTTPServer.URL+"/api/v1/orgs/"+TestOrgSlug+"/users/"+adminUID+"/notifications",
		nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer pat_nonadmin")

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusForbidden, resp.StatusCode, "non-admin should not access other user's notifications")
}
