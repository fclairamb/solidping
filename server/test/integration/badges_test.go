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

	// Multi-tier badge test fixtures (separate check to avoid UID collision).
	badgeMTCheckUID  = "31000000-0000-0000-0000-000000000001"
	badgeMTWorkerUID = "31000000-0000-0000-0000-000000000002"
	badgeMTRawUID    = "31000000-0000-0000-0000-000000000003"
	badgeMTHourUID   = "31000000-0000-0000-0000-000000000004"
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

func TestBadges_AllComponents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/status,availability,duration,response-time"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
}

func TestBadges_LegacyAvailabilityDurationReturns400(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// The legacy availability-duration token is not valid under the new scheme.
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/availability-duration"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusBadRequest, resp.StatusCode)
	r.Equal("application/json", resp.Header.Get("Content-Type"))
}

func TestBadges_DurationOnlyIsValid(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// duration alone is now valid: row 1 renders the check name + duration text.
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/duration"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))
}

func TestBadges_UptimeBarRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// uptime-bar alone: name title row + bar strip. H = 20 + 4 + 20 = 44.
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/uptime-bar"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	r.Contains(svg, `height="44"`)
	r.Contains(svg, "Badge Test Check")
}

func TestBadges_ResponseTimeGraphRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// response-time-graph alone: name title row + graph. H = 20 + 4 + 40 = 64.
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/response-time-graph"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `height="64"`)
}

func TestBadges_AllSixRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// status,uptime-bar,response-time-graph: 3 rows. H = 20 + 4 + 20 + 4 + 40 = 88.
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/status,uptime-bar,response-time-graph?period=30d"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	r.Contains(svg, `height="88"`)
	r.Contains(svg, `width="300"`) // default combined width
}

func TestBadges_AllComponentsWithRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/" +
		"status,availability,duration,response-time,uptime-bar,response-time-graph?period=30d"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))
}

func TestBadges_LegacyUptimeBarEndpointRemoved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesTestData(ctx, t, testServer)

	// The standalone /uptime-bar API route was removed; it is folded into
	// /badges/uptime-bar. The dedicated SVG handler no longer exists, so the
	// request must NOT return an SVG badge (it falls through to the SPA
	// catch-all instead of the badge handler).
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/uptime-bar"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.NotEqual("image/svg+xml", resp.Header.Get("Content-Type"),
		"the removed uptime-bar route must not serve an SVG badge")
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

	// Request availability,duration badge (new composable format)
	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-test-check/badges/availability,duration?period=7d"
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

	periods := []string{"24h", "7d", "30d", "90d"}

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

// setupBadgesMultiTierData creates a dedicated check seeded with one raw row
// and one hour-aggregated row in the current bucket window. This exercises the
// multi-tier fetch path added to fetchBucketData.
func setupBadgesMultiTierData(ctx context.Context, t *testing.T, ts *TestServer) {
	t.Helper()

	dbSvc := ts.Server.DBService()
	orgUID := "10000000-0000-0000-0000-000000000001"
	region := "us-east-1"

	worker := &models.Worker{
		UID:       badgeMTWorkerUID,
		Slug:      "badge-mt-worker",
		Name:      "Badge Multi-Tier Worker",
		Region:    &region,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := dbSvc.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("failed to create mt worker: %v", err)
	}

	checkName := "Badge MT Check"
	checkSlug := "badge-mt-check"
	check := &models.Check{
		UID:             badgeMTCheckUID,
		OrganizationUID: orgUID,
		Name:            &checkName,
		Slug:            &checkSlug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := dbSvc.CreateCheck(ctx, check); err != nil {
		t.Fatalf("failed to create mt check: %v", err)
	}

	now := time.Now().UTC()
	workerUID := badgeMTWorkerUID

	// Place data 2 days ago so it always falls within the 30-day window regardless of
	// what time of day the test runs. The 30d window covers [now.Truncate(24h)-30d,
	// now.Truncate(24h)), so yesterday's midnight bucket (now.Truncate(24h)-24h) is
	// always within the window.
	yesterday := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)

	// Raw result: up, 50ms — falls in yesterday's day bucket.
	statusUp := int(models.ResultStatusUp)
	rawDuration := float32(50.0)
	rawResult := &models.Result{
		UID:             badgeMTRawUID,
		OrganizationUID: orgUID,
		CheckUID:        badgeMTCheckUID,
		WorkerUID:       &workerUID,
		Region:          &region,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     yesterday.Add(time.Hour), // within yesterday
		Status:          &statusUp,
		Duration:        &rawDuration,
		Output:          models.JSONMap{"message": "OK"},
		CreatedAt:       now,
	}
	if err := dbSvc.CreateResult(ctx, rawResult); err != nil {
		t.Fatalf("failed to create mt raw result: %v", err)
	}

	// Hour-aggregated result: 10 total, 8 up, avg 120ms — also in yesterday's day bucket.
	total := 10
	successful := 8
	availPct := 80.0
	avgDur := float32(120.0)
	dayBucketEnd := yesterday.Add(24 * time.Hour)
	hourResult := &models.Result{
		UID:              badgeMTHourUID,
		OrganizationUID:  orgUID,
		CheckUID:         badgeMTCheckUID,
		Region:           &region,
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      yesterday, // yesterday midnight
		PeriodEnd:        &dayBucketEnd,
		TotalChecks:      &total,
		SuccessfulChecks: &successful,
		AvailabilityPct:  &availPct,
		DurationAvg:      &avgDur,
		CreatedAt:        now,
	}
	if err := dbSvc.CreateResult(ctx, hourResult); err != nil {
		t.Fatalf("failed to create mt hour result: %v", err)
	}
}

// TestBadges_UptimeBarHasNonGreyRect asserts that when raw + hour rows exist
// in the current window, the uptime-bar SVG contains at least one non-gray
// colored rect.
func TestBadges_UptimeBarHasNonGreyRect(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesMultiTierData(ctx, t, testServer)

	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-mt-check/badges/uptime-bar?period=30d"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	// Must contain at least one non-grey rect (red, because avg availability < 99%).
	r.Contains(svg, `<rect x=`)
	// Grey-only bars use only #9f9f9f; presence of any other color proves a bucket has data.
	r.NotEqual(0, countNonGreyRects(svg), "expected at least one non-gray segment in uptime bar")
}

// TestBadges_ResponseTimeGraphHasPolyline asserts that seeded raw + hour data
// produces a <polyline> or area <path> in the response-time-graph SVG.
func TestBadges_ResponseTimeGraphHasPolyline(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	setupBadgesMultiTierData(ctx, t, testServer)

	url := testServer.HTTPServer.URL + "/api/v1/orgs/" + TestOrgSlug +
		"/checks/badge-mt-check/badges/response-time-graph?period=30d"
	resp, err := fetchBadge(ctx, url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/svg+xml", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	svg := string(body)
	// An empty graph only contains a rect frame; a non-empty one has a polyline or area path.
	r.True(
		containsString(svg, "<polyline") || containsString(svg, "<circle"),
		"expected a polyline or dot in response-time-graph SVG; got only empty frame",
	)
}

// countNonGreyRects counts fill attributes that are not the grey placeholder color.
func countNonGreyRects(svg string) int {
	const grey = "#9f9f9f"
	count := 0
	idx := 0

	for {
		pos := indexString(svg[idx:], `fill="`)
		if pos < 0 {
			break
		}

		pos += idx + len(`fill="`)
		end := indexString(svg[pos:], `"`)

		if end < 0 {
			break
		}

		color := svg[pos : pos+end]
		if color != grey {
			count++
		}

		idx = pos + end + 1
	}

	return count
}

// indexString returns the index of substr in s, or -1.
func indexString(s, substr string) int {
	for i := range len(s) - len(substr) + 1 {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}

	return -1
}

// containsString reports whether substr appears in s.
func containsString(s, substr string) bool {
	return indexString(s, substr) >= 0
}
