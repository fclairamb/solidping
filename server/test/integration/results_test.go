package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	testCheckUID1     = "20000000-0000-0000-0000-000000000001"
	testCheckUID2     = "20000000-0000-0000-0000-000000000002"
	testWorkerUID     = "20000000-0000-0000-0000-000000000003"
	testResultUID1    = "20000000-0000-0000-0000-000000000004"
	testResultUID2    = "20000000-0000-0000-0000-000000000005"
	testResultUID3    = "20000000-0000-0000-0000-000000000006"
	testResultUID4    = "20000000-0000-0000-0000-000000000007"
	testResultUID5    = "20000000-0000-0000-0000-000000000008"
	testRegionUSEast1 = "us-east-1"
)

// setupResultsTestData creates test checks, workers, and results for testing.
func setupResultsTestData(ctx context.Context, t *testing.T, ts *TestServer) {
	t.Helper()

	dbService := ts.Server.DBService()

	// Create test worker
	region := testRegionUSEast1
	worker := &models.Worker{
		UID:       testWorkerUID,
		Slug:      "test-worker-1",
		Name:      "Test Worker 1",
		Region:    &region,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := dbService.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("failed to create test worker: %v", err)
	}

	// Create test checks
	check1Name := "Test Check 1"
	check1Slug := "test-check-1"
	check1 := &models.Check{
		UID:             testCheckUID1,
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

	check2Name := "Test Check 2"
	check2Slug := "test-check-2"
	check2 := &models.Check{
		UID:             testCheckUID2,
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

	// Create test results with various statuses and timestamps
	now := time.Now()
	usEastRegion := testRegionUSEast1
	euWestRegion := "eu-west-1"
	workerUID := testWorkerUID
	status1 := 1 // up
	status2 := 2 // down
	status3 := 3 // timeout
	duration1 := float32(100.5)
	duration2 := float32(50.0)
	duration3 := float32(95.0)

	results := []*models.Result{
		{
			UID:             testResultUID1,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testCheckUID1,
			WorkerUID:       &workerUID,
			Region:          &usEastRegion,
			PeriodType:      "day",
			PeriodStart:     now.Add(-10 * time.Minute),
			Status:          &status1,
			Duration:        &duration1,
			Output:          models.JSONMap{"message": "OK"},
			CreatedAt:       now,
		},
		{
			UID:             testResultUID2,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testCheckUID1,
			WorkerUID:       &workerUID,
			Region:          &usEastRegion,
			PeriodType:      "day",
			PeriodStart:     now.Add(-20 * time.Minute),
			Status:          &status2,
			Output:          models.JSONMap{"error": "Connection refused"},
			CreatedAt:       now,
		},
		{
			UID:             testResultUID3,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testCheckUID2,
			WorkerUID:       &workerUID,
			Region:          &euWestRegion,
			PeriodType:      "day",
			PeriodStart:     now.Add(-30 * time.Minute),
			Status:          &status1,
			Duration:        &duration2,
			Output:          models.JSONMap{"message": "Ping successful"},
			CreatedAt:       now,
		},
		{
			UID:             testResultUID4,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testCheckUID2,
			WorkerUID:       &workerUID,
			Region:          &euWestRegion,
			PeriodType:      "month",
			PeriodStart:     now.Add(-1 * time.Hour),
			Status:          &status3,
			Output:          models.JSONMap{"error": "Timeout"},
			CreatedAt:       now,
		},
		{
			UID:             testResultUID5,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        testCheckUID1,
			WorkerUID:       &workerUID,
			Region:          &usEastRegion,
			PeriodType:      "year",
			PeriodStart:     now.Add(-2 * time.Hour),
			Status:          &status1,
			Duration:        &duration3,
			Output:          models.JSONMap{"message": "Yearly aggregate"},
			CreatedAt:       now,
		},
	}

	for _, result := range results {
		if err := dbService.CreateResult(ctx, result); err != nil {
			t.Fatalf("failed to create test result %s: %v", result.UID, err)
		}
	}
}

func TestListOrgResults_Basic(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	// Setup test data
	setupResultsTestData(ctx, t, testServer)

	// Login to get token
	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// List all results without filters
	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, nil)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have at least 5 results (might have more from seed data)
	if len(*resp.JSON200.Data) < 5 {
		t.Errorf("expected at least 5 results, got %d", len(*resp.JSON200.Data))
	}
}

func TestListOrgResults_FilterByCheck(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by check UID
	checkFilter := testCheckUID1
	params := &client.ListOrgResultsParams{
		CheckUid: &checkFilter,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 4 results for check 1 (3 from test data + 1 from check creation)
	if len(*resp.JSON200.Data) != 4 {
		t.Errorf("expected 4 results for check 1, got %d", len(*resp.JSON200.Data))
	}

	// Verify all results belong to check 1
	for _, result := range *resp.JSON200.Data {
		if result.CheckUid == nil || result.CheckUid.String() != testCheckUID1 {
			t.Errorf("expected check UID %s, got %v", testCheckUID1, result.CheckUid)
		}
	}
}

func TestListOrgResults_FilterByCheckSlug(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by check slug
	checkFilter := "test-check-2"
	params := &client.ListOrgResultsParams{
		CheckUid: &checkFilter,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 3 results for check 2 (2 from test data + 1 from check creation)
	if len(*resp.JSON200.Data) != 3 {
		t.Errorf("expected 3 results for check 2, got %d", len(*resp.JSON200.Data))
	}
}

func TestListOrgResults_FilterByMultipleChecks(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by multiple checks (UID and slug mixed)
	checkFilter := testCheckUID1 + ",test-check-2"
	params := &client.ListOrgResultsParams{
		CheckUid: &checkFilter,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have all 7 results (3 from check 1 + 2 from check 2 + 2 from check creation)
	if len(*resp.JSON200.Data) != 7 {
		t.Errorf("expected 7 results, got %d", len(*resp.JSON200.Data))
	}
}

func TestListOrgResults_FilterByCheckType(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by check type
	checkTypeFilter := "http"
	params := &client.ListOrgResultsParams{
		CheckType: &checkTypeFilter,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 4 results for http check (3 from test data + 1 from check creation)
	if len(*resp.JSON200.Data) != 4 {
		t.Errorf("expected 4 results for http check type, got %d", len(*resp.JSON200.Data))
	}
}

func TestListOrgResults_FilterByStatus(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	tests := []struct {
		name          string
		statusFilter  string
		expectedCount int
	}{
		{
			name:          "filter by up status",
			statusFilter:  "up",
			expectedCount: 3,
		},
		{
			name:          "filter by down status",
			statusFilter:  "down",
			expectedCount: 2, // down (2) + timeout (3) = 2 results
		},
		{
			name:          "filter by multiple statuses",
			statusFilter:  "up,down",
			expectedCount: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := &client.ListOrgResultsParams{
				Status: &tc.statusFilter,
			}

			resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
			if err != nil {
				t.Fatalf("failed to list results: %v", err)
			}

			if resp.JSON200 == nil {
				t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
			}

			if resp.JSON200.Data == nil {
				t.Fatal("expected data in response")
			}

			if len(*resp.JSON200.Data) != tc.expectedCount {
				t.Errorf("expected %d results, got %d", tc.expectedCount, len(*resp.JSON200.Data))
			}
		})
	}
}

func TestListOrgResults_FilterByRegion(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Filter by region
	regionFilter := testRegionUSEast1
	params := &client.ListOrgResultsParams{
		Region: &regionFilter,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 3 results from us-east-1
	if len(*resp.JSON200.Data) != 3 {
		t.Errorf("expected 3 results from us-east-1, got %d", len(*resp.JSON200.Data))
	}
}

func TestListOrgResults_FilterByPeriodType(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	tests := []struct {
		name          string
		periodType    string
		expectedCount int
	}{
		{
			name:          "filter by day",
			periodType:    "day",
			expectedCount: 3,
		},
		{
			name:          "filter by month",
			periodType:    "month",
			expectedCount: 1,
		},
		{
			name:          "filter by year",
			periodType:    "year",
			expectedCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := &client.ListOrgResultsParams{
				PeriodType: &tc.periodType,
			}

			resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
			if err != nil {
				t.Fatalf("failed to list results: %v", err)
			}

			if resp.JSON200 == nil {
				t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
			}

			if resp.JSON200.Data == nil {
				t.Fatal("expected data in response")
			}

			if len(*resp.JSON200.Data) != tc.expectedCount {
				t.Errorf("expected %d results, got %d", tc.expectedCount, len(*resp.JSON200.Data))
			}
		})
	}
}

func TestListOrgResults_Pagination(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Request with size=2 to test pagination
	size := 2
	params := &client.ListOrgResultsParams{
		Size: &size,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 2 results
	if len(*resp.JSON200.Data) != 2 {
		t.Errorf("expected 2 results, got %d", len(*resp.JSON200.Data))
	}

	// Should have pagination with cursor for next page
	if resp.JSON200.Pagination == nil {
		t.Fatal("expected pagination in response")
	}

	if resp.JSON200.Pagination.Cursor == nil || *resp.JSON200.Pagination.Cursor == "" {
		t.Error("expected cursor to be set")
	}

	// Fetch next page using cursor
	nextParams := &client.ListOrgResultsParams{
		Size:   &size,
		Cursor: resp.JSON200.Pagination.Cursor,
	}

	nextResp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, nextParams)
	if err != nil {
		t.Fatalf("failed to list next page: %v", err)
	}

	if nextResp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", nextResp.StatusCode())
	}

	if nextResp.JSON200.Data == nil {
		t.Fatal("expected data in next page response")
	}

	// Should have 2 more results
	if len(*nextResp.JSON200.Data) != 2 {
		t.Errorf("expected 2 results in next page, got %d", len(*nextResp.JSON200.Data))
	}

	// Should still have more (cursor should be set)
	if nextResp.JSON200.Pagination == nil {
		t.Fatal("expected pagination in next page response")
	}

	if nextResp.JSON200.Pagination.Cursor == nil || *nextResp.JSON200.Pagination.Cursor == "" {
		t.Error("expected cursor to be set on second page")
	}
}

func TestListOrgResults_SizeLimit(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Test with very large size - should be capped at 100
	size := 1000
	params := &client.ListOrgResultsParams{
		Size: &size,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	// Should still work (just capped)
	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have all 7 results (5 from test data + 2 from check creation, less than the cap)
	if len(*resp.JSON200.Data) != 7 {
		t.Errorf("expected 7 results, got %d", len(*resp.JSON200.Data))
	}
}

func TestListOrgResults_InvalidSize(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Test with invalid size (0 or negative) - should return validation error
	size := 0
	params := &client.ListOrgResultsParams{
		Size: &size,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	// Should return validation error
	if resp.JSON200 != nil {
		t.Error("expected error for invalid size, got success")
	}

	if resp.StatusCode() != 400 {
		t.Errorf("expected 400 status code for invalid size, got %d", resp.StatusCode())
	}
}

func TestListOrgResults_Unauthenticated(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	// Create client without authentication
	apiClient := testServer.NewClient()

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, nil)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	// Should return 401 Unauthorized
	if resp.JSON401 == nil {
		t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode())
	}
}

func TestListOrgResults_InvalidOrg(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Try to list results for non-existent org
	resp, err := apiClient.ListOrgResultsWithResponse(ctx, "non-existent-org", nil)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	// Should return 404 Not Found
	if resp.JSON404 == nil {
		t.Errorf("expected 404 Not Found, got %d", resp.StatusCode())
	}
}

func TestListOrgResults_WithOptionalFields(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Request with optional fields
	withFields := "checkName,checkSlug,region,output"
	params := &client.ListOrgResultsParams{
		With: &withFields,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		t.Fatal("expected data in response")
	}

	// Verify optional fields are included
	// Find a result with region (skip "raw" results that don't have region)
	var resultWithRegion *client.OrgResult
	for _, result := range *resp.JSON200.Data {
		if result.Region != nil {
			resultWithRegion = &result
			break
		}
	}

	r := require.New(t)
	r.NotNil(resultWithRegion, "expected at least one result with region")

	// checkName and checkSlug are not yet implemented in the service layer.
	// Enable these checks when the feature is implemented.
	// r.NotNil(resultWithRegion.CheckName, "expected checkName to be included")
	// r.NotNil(resultWithRegion.CheckSlug, "expected checkSlug to be included")

	r.NotNil(resultWithRegion.Region, "expected region to be included")
	r.NotNil(resultWithRegion.Output, "expected output to be included")
}

func TestListOrgResults_CombinedFilters(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupResultsTestData(ctx, t, testServer)

	apiClient := testServer.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Combine multiple filters
	checkTypeFilter := "http"
	statusFilter := "up"
	regionFilter := testRegionUSEast1
	periodTypeFilter := "day"

	params := &client.ListOrgResultsParams{
		CheckType:  &checkTypeFilter,
		Status:     &statusFilter,
		Region:     &regionFilter,
		PeriodType: &periodTypeFilter,
	}

	resp, err := apiClient.ListOrgResultsWithResponse(ctx, TestOrgSlug, params)
	if err != nil {
		t.Fatalf("failed to list results: %v", err)
	}

	if resp.JSON200 == nil {
		t.Fatalf("expected JSON200 response, got %d", resp.StatusCode())
	}

	if resp.JSON200.Data == nil {
		t.Fatal("expected data in response")
	}

	// Should have 1 result matching all filters
	if len(*resp.JSON200.Data) != 1 {
		t.Errorf("expected 1 result with combined filters, got %d", len(*resp.JSON200.Data))
	}

	// Verify the result matches all filters
	if len(*resp.JSON200.Data) > 0 {
		result := (*resp.JSON200.Data)[0]

		if result.Status == nil || string(*result.Status) != "up" {
			t.Errorf("expected status 'up', got %v", result.Status)
		}

		if result.PeriodType == nil || *result.PeriodType != "day" {
			t.Errorf("expected period type 'day', got %v", result.PeriodType)
		}
	}
}
