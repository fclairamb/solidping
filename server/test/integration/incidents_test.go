package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	testIncidentCheckUID1 = "30000000-0000-0000-0000-000000000001"
	testIncidentCheckUID2 = "30000000-0000-0000-0000-000000000002"
	testIncidentUID1      = "30000000-0000-0000-0000-000000000003"
	testIncidentUID2      = "30000000-0000-0000-0000-000000000004"
	testIncidentUID3      = "30000000-0000-0000-0000-000000000005"
)

// setupIncidentsTestData creates test checks and incidents for testing.
func setupIncidentsTestData(ctx context.Context, t *testing.T, ts *TestServer) {
	t.Helper()

	dbService := ts.Server.DBService()

	// Create test checks
	check1Name := "Incident Test Check 1"
	check1Slug := "incident-test-check-1"
	check1 := &models.Check{
		UID:             testIncidentCheckUID1,
		OrganizationUID: "10000000-0000-0000-0000-000000000001", // matches test org from testhelper
		Name:            &check1Name,
		Slug:            &check1Slug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := dbService.CreateCheck(ctx, check1); err != nil {
		t.Fatalf("failed to create test check 1: %v", err)
	}

	check2Name := "Incident Test Check 2"
	check2Slug := "incident-test-check-2"
	check2 := &models.Check{
		UID:             testIncidentCheckUID2,
		OrganizationUID: "10000000-0000-0000-0000-000000000001",
		Name:            &check2Name,
		Slug:            &check2Slug,
		Type:            "icmp",
		Config:          models.JSONMap{"host": "example.org"},
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := dbService.CreateCheck(ctx, check2); err != nil {
		t.Fatalf("failed to create test check 2: %v", err)
	}

	// Create test incidents
	now := time.Now()
	title1 := "Incident 1 for Check 1"
	title2 := "Incident 2 for Check 1"
	title3 := "Incident 3 for Check 2"

	incidents := []*models.Incident{
		{
			UID:             testIncidentUID1,
			Kind:            models.IncidentKindCheck,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testIncidentCheckUID1,
			Title:           &title1,
			State:           models.IncidentStateActive,
			FailureCount:    3,
			StartedAt:       now.Add(-1 * time.Hour),
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		{
			UID:             testIncidentUID2,
			Kind:            models.IncidentKindCheck,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testIncidentCheckUID1,
			Title:           &title2,
			State:           models.IncidentStateResolved,
			FailureCount:    5,
			StartedAt:       now.Add(-24 * time.Hour),
			ResolvedAt:      ptrTime(now.Add(-12 * time.Hour)),
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		{
			UID:             testIncidentUID3,
			Kind:            models.IncidentKindCheck,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testIncidentCheckUID2,
			Title:           &title3,
			State:           models.IncidentStateActive,
			FailureCount:    1,
			StartedAt:       now.Add(-30 * time.Minute),
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}

	for _, incident := range incidents {
		if err := dbService.CreateIncident(ctx, incident); err != nil {
			t.Fatalf("failed to create test incident %s: %v", incident.UID, err)
		}
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func TestListIncidents_FilterByCheckUid(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by check UID - should only return incidents for check 1
	checkUIDFilter := testIncidentCheckUID1
	params := &client.ListIncidentsParams{
		CheckUid: &checkUIDFilter,
	}

	resp, err := apiClient.ListIncidentsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list incidents: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 2 incidents for check 1
	if len(*resp.JSON200.Data) != 2 {
		t.Errorf("expected 2 incidents for check 1, got %d", len(*resp.JSON200.Data))
	}

	// Verify all incidents belong to check 1
	for _, incident := range *resp.JSON200.Data {
		if incident.CheckUid == nil || incident.CheckUid.String() != testIncidentCheckUID1 {
			t.Errorf("expected check UID %s, got %v", testIncidentCheckUID1, incident.CheckUid)
		}
	}
}

func TestListIncidents_FilterByCheckUid_NoResults(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by non-existent check UID
	nonExistentCheckUID := "99999999-9999-9999-9999-999999999999"
	params := &client.ListIncidentsParams{
		CheckUid: &nonExistentCheckUID,
	}

	resp, err := apiClient.ListIncidentsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list incidents: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 0 incidents
	if len(*resp.JSON200.Data) != 0 {
		t.Errorf("expected 0 incidents for non-existent check, got %d", len(*resp.JSON200.Data))
	}
}

func TestListIncidents_FilterByCheckUid_SecondCheck(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by check 2 UID - should only return incident for check 2
	checkUIDFilter := testIncidentCheckUID2
	params := &client.ListIncidentsParams{
		CheckUid: &checkUIDFilter,
	}

	resp, err := apiClient.ListIncidentsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list incidents: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 1 incident for check 2
	if len(*resp.JSON200.Data) != 1 {
		t.Errorf("expected 1 incident for check 2, got %d", len(*resp.JSON200.Data))
	}

	// Verify the incident belongs to check 2
	if len(*resp.JSON200.Data) > 0 {
		incident := (*resp.JSON200.Data)[0]
		if incident.CheckUid == nil || incident.CheckUid.String() != testIncidentCheckUID2 {
			t.Errorf("expected check UID %s, got %v", testIncidentCheckUID2, incident.CheckUid)
		}
	}
}

func TestListIncidents_FilterByCheckUidAndState(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by check 1 UID and active state
	checkUIDFilter := testIncidentCheckUID1
	stateFilter := "active"
	params := &client.ListIncidentsParams{
		CheckUid: &checkUIDFilter,
		State:    &stateFilter,
	}

	resp, err := apiClient.ListIncidentsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list incidents: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 1 active incident for check 1
	if len(*resp.JSON200.Data) != 1 {
		t.Errorf("expected 1 active incident for check 1, got %d", len(*resp.JSON200.Data))
	}

	// Verify the incident matches both filters
	if len(*resp.JSON200.Data) > 0 {
		incident := (*resp.JSON200.Data)[0]
		if incident.CheckUid == nil || incident.CheckUid.String() != testIncidentCheckUID1 {
			t.Errorf("expected check UID %s, got %v", testIncidentCheckUID1, incident.CheckUid)
		}
		if incident.State == nil || string(*incident.State) != "active" {
			t.Errorf("expected state 'active', got %v", incident.State)
		}
	}
}

func TestListIncidents_NoFilter_ReturnsAll(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// No filter - should return all incidents
	resp, err := apiClient.ListIncidentsWithResponse(ctx, TestOrgSlug, nil)
	if err != nil {
		t.Fatalf("failed to list incidents: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have at least 3 incidents (from our test data)
	if len(*resp.JSON200.Data) < 3 {
		t.Errorf("expected at least 3 incidents, got %d", len(*resp.JSON200.Data))
	}
}

// loginForToken logs in and returns the bearer token. Mutation endpoints are
// not yet exposed via the generated OpenAPI client, so the dashboard click
// flow is exercised with raw HTTP using the returned token.
func loginForToken(ctx context.Context, t *testing.T, ts *TestServer) string {
	t.Helper()

	apiClient := ts.NewClient()

	resp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	require.NoError(t, err)
	require.NotNil(t, resp.AccessToken)

	return *resp.AccessToken
}

// doIncidentAction POSTs to one of the incident mutation endpoints and
// returns the status code plus the decoded body.
func doIncidentAction(
	ctx context.Context, t *testing.T, ts *TestServer, token, orgSlug, incidentUID, action string, body any,
) (int, map[string]any) {
	t.Helper()

	var bodyReader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)

		bodyReader = bytes.NewReader(raw)
	}

	url := ts.HTTPServer.URL + "/api/v1/orgs/" + orgSlug + "/incidents/" + incidentUID + "/" + action

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // best-effort

	respBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	out := map[string]any{}
	if len(respBytes) > 0 {
		_ = json.Unmarshal(respBytes, &out)
	}

	return resp.StatusCode, out
}

func TestIncidentDashboardClickFlow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)
	token := loginForToken(ctx, t, testServer)

	// Acknowledge — was returning 404 before the fix.
	status, body := doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID1, "ack", map[string]string{
		"note": "checked",
	})
	r.Equal(http.StatusOK, status, "ack should succeed; got body=%v", body)

	// Snooze with a 1h duration.
	status, body = doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID1, "snooze", map[string]string{
		"duration": "1h",
		"reason":   "scheduled maintenance",
	})
	r.Equal(http.StatusOK, status, "snooze should succeed; got body=%v", body)

	// Unsnooze.
	status, body = doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID1, "unsnooze", nil)
	r.Equal(http.StatusOK, status, "unsnooze should succeed; got body=%v", body)

	// Unack.
	status, body = doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID1, "unack", nil)
	r.Equal(http.StatusOK, status, "unack should succeed; got body=%v", body)

	// Resolve.
	status, body = doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID1, "resolve", map[string]string{
		"note": "all clear",
	})
	r.Equal(http.StatusOK, status, "resolve should succeed; got body=%v", body)

	// Unknown org: the non-super-admin's token is scoped to TestOrgSlug, so
	// RequireOrgAccess denies the foreign org (403) before the handler's
	// org-resolution runs — returning 404 would leak org existence to any
	// member (spec 2026-07-15-01).
	status, _ = doIncidentAction(ctx, t, testServer, token, "no-such-org", testIncidentUID1, "ack", nil)
	r.Equal(http.StatusForbidden, status, "ack on unknown org should be 403 (token scoped elsewhere)")

	// Unknown incident returns 404 (exercises the GetIncident branch).
	status, _ = doIncidentAction(ctx, t, testServer, token, TestOrgSlug,
		"30000000-0000-0000-0000-0000000099ff", "ack", nil)
	r.Equal(http.StatusNotFound, status, "ack on unknown incident should be 404")
}

func TestIncidentSnoozeValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupIncidentsTestData(ctx, t, testServer)
	token := loginForToken(ctx, t, testServer)

	// Use the third (active) incident so prior tests can't pollute its state.
	status, _ := doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID3, "snooze", map[string]string{
		"duration": "garbage",
	})
	r.Equal(http.StatusBadRequest, status, "invalid duration should be 400")

	pastTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	status, _ = doIncidentAction(ctx, t, testServer, token, TestOrgSlug, testIncidentUID3, "snooze", map[string]string{
		"until": pastTime,
	})
	r.Equal(http.StatusBadRequest, status, "until in the past should be 400")
}
