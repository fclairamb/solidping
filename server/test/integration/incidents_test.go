package integration

import (
	"context"
	"testing"
	"time"

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
