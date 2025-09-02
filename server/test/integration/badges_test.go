package integration

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	badgeTestCheckUID   = "30000000-0000-0000-0000-000000000001"
	badgeTestWorkerUID  = "30000000-0000-0000-0000-000000000002"
	badgeTestResultUID1 = "30000000-0000-0000-0000-000000000003"
	badgeTestResultUID2 = "30000000-0000-0000-0000-000000000004"
	badgeTestResultUID3 = "30000000-0000-0000-0000-000000000005"
)

// setupBadgesTestData creates test data for badge tests.
func setupBadgesTestData(ctx context.Context, t *testing.T, ts *TestServer) {
	t.Helper()

	dbService := ts.Server.DBService()

	// Create test worker
	region := "us-east-1"
	worker := &models.Worker{
		UID:       badgeTestWorkerUID,
		Slug:      "badge-test-worker",
		Name:      "Badge Test Worker",
		Region:    &region,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := dbService.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("failed to create test worker: %v", err)
	}

	// Create test check
	checkName := "Badge Test Check"
	checkSlug := "badge-test-check"
	check := &models.Check{
		UID:             badgeTestCheckUID,
		OrganizationUID: "10000000-0000-0000-0000-000000000001", // matches test org from testhelper
		Name:            &checkName,
		Slug:            &checkSlug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := dbService.CreateCheck(ctx, check); err != nil {
		t.Fatalf("failed to create test check: %v", err)
	}

	// Create test results with various statuses.
	// Timestamps must be AFTER the check creation result (which is created at "now")
	// to ensure our test results are returned as "most recent" by the badge service.
	now := time.Now()
	workerUID := badgeTestWorkerUID
	statusUp := int(models.ResultStatusUp)
	statusDown := int(models.ResultStatusDown)
	duration := float32(100.0)

	results := []*models.Result{
		{
			UID:             badgeTestResultUID1,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        badgeTestCheckUID,
			WorkerUID:       &workerUID,
			Region:          &region,
			PeriodType:      "raw",
			PeriodStart:     now.Add(1 * time.Second), // Most recent - "up" status
			Status:          &statusUp,
			Duration:        &duration,
			Output:          models.JSONMap{"message": "OK"},
			CreatedAt:       now.Add(1 * time.Second),
		},
		{
			UID:             badgeTestResultUID2,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        badgeTestCheckUID,
			WorkerUID:       &workerUID,
			Region:          &region,
			PeriodType:      "raw",
			PeriodStart:     now.Add(-5 * time.Minute),
			Status:          &statusUp,
			Duration:        &duration,
			Output:          models.JSONMap{"message": "OK"},
			CreatedAt:       now,
		},
		{
			UID:             badgeTestResultUID3,
			OrganizationUID: "10000000-0000-0000-0000-000000000001",
			CheckUID:        badgeTestCheckUID,
			WorkerUID:       &workerUID,
			Region:          &region,
			PeriodType:      "raw",
			PeriodStart:     now.Add(-10 * time.Minute),
			Status:          &statusDown,
			Duration:        &duration,
			Output:          models.JSONMap{"error": "Connection refused"},
			CreatedAt:       now,
		},
	}

	for _, result := range results {
		if err := dbService.CreateResult(ctx, result); err != nil {
			t.Fatalf("failed to create test result %s: %v", result.UID, err)
		}
	}
}

func fetchBadge(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	return http.DefaultClient.Do(req)
}

func TestBadges_StatusBadge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request status badge (public endpoint, no auth required)
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug + "/checks/badge-test-check/badges/status"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))
	r.Contains(resp.Header.Get("Cache-Control"), "max-age=60")

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	r.Contains(svg, "Badge Test Check")
	r.Contains(svg, "up") // Current status should be "up"
}

func TestBadges_StatusBadgeByUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request status badge by UID
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug + "/checks/" + badgeTestCheckUID + "/badges/status"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, "up")
}

func TestBadges_AvailabilityBadge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request availability badge
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/availability?period=24h"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	r.Contains(svg, "%") // Should contain percentage
}

func TestBadges_AvailabilityDurationBadge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request availability-duration badge
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/availability-duration?period=7d"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	r.Contains(svg, "%") // Should contain percentage
	r.Contains(svg, "↑") // Should contain uptime arrow
}

func TestBadges_CustomLabel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request badge with custom label
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/status?label=My%20Service"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, "My Service")
}

func TestBadges_FlatSquareStyle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request badge with flat-square style
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/status?style=flat-square"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `rx="0"`) // Flat-square has no border radius
}

func TestBadges_InvalidFormat(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request badge with invalid format
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/invalid-format"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusBadRequest, resp.StatusCode)
	r.Equal("application/json", resp.Header.Get("Content-Type"))
}

func TestBadges_CheckNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request badge for non-existent check
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/non-existent-check/badges/status"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Equal("application/json", resp.Header.Get("Content-Type"))
}

func TestBadges_OrganizationNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Request badge for non-existent organization
	url := testServer.HTTPServer.URL + "/api/v1/orgs/non-existent-org/checks/badge-test-check/badges/status"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Equal("application/json", resp.Header.Get("Content-Type"))
}

func TestBadges_NoAuthRequired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// Create a new HTTP client without any auth headers
	client := &http.Client{}

	// Request badge without authentication
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug + "/checks/badge-test-check/badges/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	r.NoError(err)

	resp, err := client.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	// Should succeed without authentication
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))
}

func TestBadges_PeriodOptions(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	periods := []string{"1h", "24h", "7d", "30d"}

	for _, period := range periods {
		t.Run("period_"+period, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
				"/checks/badge-test-check/badges/availability?period=" + period
			resp, err := fetchBadge(ctx, url)
			r.NoError(err)
			defer func() { _ = resp.Body.Close() }()

			r.Equal(http.StatusOK, resp.StatusCode)
			r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))
		})
	}
}
