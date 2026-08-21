// Package testdata provides utilities for creating test data.
package testdata

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// CreateTestData creates deterministic test data for test mode. cfg is needed
// by the seeders that write real blobs through the files service (the incident
// screenshot fixture); it may be nil, in which case those seeders are skipped.
func CreateTestData(ctx context.Context, dbService db.Service, cfg *config.Config) error {
	// Check if test organization already exists
	count, err := dbService.DB().NewSelect().
		Model((*models.Organization)(nil)).
		Where("uid = ?", "00000000-0000-0000-0000-000000000001").
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for existing test org: %w", err)
	}

	if count > 0 {
		slog.InfoContext(ctx, "Test organization already exists, skipping test data creation")
		return nil
	}

	now := time.Now()

	// Create test organization
	testOrg, err := createTestOrganization(ctx, dbService, now)
	if err != nil {
		return err
	}

	// Create test user
	testUser, err := createTestUser(ctx, dbService, now)
	if err != nil {
		return err
	}

	// Create test membership
	if err := createTestMembership(ctx, dbService, testOrg.UID, testUser.UID, "00000000-0000-0000-0000-000000000004", models.MemberRoleAdmin, now); err != nil {
		return err
	}

	// Create test PAT token
	if err := createTestToken(ctx, dbService, testOrg.UID, testUser.UID, now); err != nil {
		return err
	}

	// Create test2 organization (admin access)
	test2Org, err := createTestOrg2(ctx, dbService, now)
	if err != nil {
		return err
	}

	if err := createTestMembership(ctx, dbService, test2Org.UID, testUser.UID, "00000000-0000-0000-0000-000000000005", models.MemberRoleAdmin, now); err != nil {
		return err
	}

	// Create test3 organization (user access, no admin)
	test3Org, err := createTestOrg3(ctx, dbService, now)
	if err != nil {
		return err
	}

	if err := createTestMembership(ctx, dbService, test3Org.UID, testUser.UID, "00000000-0000-0000-0000-000000000006", models.MemberRoleUser, now); err != nil {
		return err
	}

	// Seed a deterministic, unpromoted discovered check (with a scan) so the
	// discovery promote flow is exercisable in test mode / e2e.
	if err := createTestDiscoveryData(ctx, dbService, testOrg.UID, now); err != nil {
		return err
	}

	// Seed a status page so status-update E2E tests can select one in the form.
	if err := createTestStatusPage(ctx, dbService, testOrg.UID, now); err != nil {
		return err
	}

	// Seed an active incident carrying one notification so the
	// incident-notification click-through E2E has deterministic data to drive.
	if err := createTestIncidentNotification(ctx, dbService, testOrg.UID, now); err != nil {
		return err
	}

	// Seed incidents carrying an opt-in failure-response capture so the
	// "What the probe saw" E2E has deterministic data for the text,
	// truncated and binary renderings (spec 2026-08-20-01).
	if err := createTestFailureResponseCaptures(ctx, dbService, testOrg.UID, now); err != nil {
		return err
	}

	// Seed a browser check whose incident carries a real screenshot attachment
	// (spec 2026-08-21-01), so the incident screenshot card has deterministic
	// data — bytes included, so the <img> actually loads.
	if err := createTestIncidentScreenshot(ctx, dbService, cfg, testOrg.UID, now); err != nil {
		return err
	}

	// Seed an objective over a check with a month rollup, so the SLO list and
	// detail pages have deterministic non-null attainment (spec 2026-08-20-01).
	if err := createTestSLOData(ctx, dbService, testOrg.UID, now); err != nil {
		return err
	}

	slog.InfoContext(ctx, "Test data creation completed successfully")

	return nil
}

// createTestDiscoveryData seeds a successful network-discovery scan and one
// unpromoted group (127.0.0.1) carrying a TCP and an ICMP suggested check, so the
// promote flow can be driven end-to-end without running a real scan (which can't
// find deterministic hosts in test mode).
func createTestDiscoveryData(ctx context.Context, dbService db.Service, orgUID string, now time.Time) error {
	const (
		jobUID  = "00000000-0000-0000-0000-000000000007"
		tcpUID  = "00000000-0000-0000-0000-000000000008"
		icmpUID = "00000000-0000-0000-0000-000000000009"
	)

	job := &models.Job{
		UID:             jobUID,
		OrganizationUID: &orgUID,
		Type:            string(jobdef.JobTypeNetworkDiscovery),
		Status:          models.JobStatusSuccess,
		ScheduledAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if _, err := dbService.DB().NewInsert().Model(job).Exec(ctx); err != nil {
		return fmt.Errorf("failed to create test discovery job: %w", err)
	}

	meta := json.RawMessage(`{"openPorts":[8080],"icmpReachable":true}`)

	tcp := models.NewDiscoveredCheck(
		orgUID, jobUID, models.DiscoverySourceLAN,
		"127.0.0.1", "127.0.0.1", "127.0.0.1 · TCP/8080", "tcp-127-0-0-1-8080", "tcp",
		json.RawMessage(`{"host":"127.0.0.1","port":8080}`), meta,
	)
	tcp.UID = tcpUID
	tcp.DiscoveredAt = now

	icmp := models.NewDiscoveredCheck(
		orgUID, jobUID, models.DiscoverySourceLAN,
		"127.0.0.1", "127.0.0.1", "127.0.0.1 · ICMP", "icmp-127-0-0-1", "ping",
		json.RawMessage(`{"host":"127.0.0.1"}`), meta,
	)
	icmp.UID = icmpUID
	icmp.DiscoveredAt = now

	if _, err := dbService.DB().NewInsert().Model(&[]*models.DiscoveredCheck{tcp, icmp}).Exec(ctx); err != nil {
		return fmt.Errorf("failed to create test discovered checks: %w", err)
	}

	slog.InfoContext(ctx, "Created test discovery data", "jobUID", jobUID)

	return nil
}

func createTestOrganization(ctx context.Context, dbService db.Service, now time.Time) (*models.Organization, error) {
	testOrg := &models.Organization{
		UID:       "00000000-0000-0000-0000-000000000001",
		Slug:      "test",
		Name:      "Test Organization",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := dbService.CreateOrganization(ctx, testOrg); err != nil {
		return nil, fmt.Errorf("failed to create test organization: %w", err)
	}

	slog.InfoContext(ctx, "Created test organization", "uid", testOrg.UID, "slug", testOrg.Slug)

	return testOrg, nil
}

func createTestUser(
	ctx context.Context, dbService db.Service, now time.Time,
) (*models.User, error) {
	passwordHash, err := passwords.Hash("test")
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	testUser := &models.User{
		UID:          "00000000-0000-0000-0000-000000000002",
		Email:        "test@test.com",
		PasswordHash: &passwordHash,
		SuperAdmin:   true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := dbService.CreateUser(ctx, testUser); err != nil {
		return nil, fmt.Errorf("failed to create test user: %w", err)
	}

	slog.InfoContext(ctx, "Created test user", "uid", testUser.UID, "email", testUser.Email)

	return testUser, nil
}

func createTestOrg2(ctx context.Context, dbService db.Service, now time.Time) (*models.Organization, error) {
	org := &models.Organization{
		UID:       "00000000-0000-0000-0000-000000000010",
		Slug:      "test2",
		Name:      "Test Org 2",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := dbService.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create test2 organization: %w", err)
	}

	slog.InfoContext(ctx, "Created test2 organization", "uid", org.UID, "slug", org.Slug)

	return org, nil
}

func createTestOrg3(ctx context.Context, dbService db.Service, now time.Time) (*models.Organization, error) {
	org := &models.Organization{
		UID:       "00000000-0000-0000-0000-000000000011",
		Slug:      "test3",
		Name:      "Test Org 3",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := dbService.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create test3 organization: %w", err)
	}

	slog.InfoContext(ctx, "Created test3 organization", "uid", org.UID, "slug", org.Slug)

	return org, nil
}

func createTestMembership(
	ctx context.Context, dbService db.Service, orgUID, userUID, membershipUID string, role models.MemberRole, now time.Time,
) error {
	membership := &models.OrganizationMember{
		UID:             membershipUID,
		UserUID:         userUID,
		OrganizationUID: orgUID,
		Role:            role,
		JoinedAt:        &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := dbService.CreateOrganizationMember(ctx, membership); err != nil {
		return fmt.Errorf("failed to create test membership: %w", err)
	}

	slog.InfoContext(ctx, "Created test membership", "uid", membership.UID, "role", membership.Role)

	return nil
}

func createTestToken(
	ctx context.Context, dbService db.Service, orgUID, userUID string, now time.Time,
) error {
	testToken := &models.UserToken{
		UID:             "00000000-0000-0000-0000-000000000003",
		UserUID:         userUID,
		OrganizationUID: &orgUID,
		Token:           "test",
		Type:            models.TokenTypePAT,
		Properties:      make(models.JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := dbService.CreateUserToken(ctx, testToken); err != nil {
		return fmt.Errorf("failed to create test PAT token: %w", err)
	}

	slog.InfoContext(ctx, "Created test PAT token", "uid", testToken.UID, "token", testToken.Token)

	return nil
}

func createTestStatusPage(ctx context.Context, dbService db.Service, orgUID string, now time.Time) error {
	const uid = "00000000-0000-0000-0000-000000000009"

	page := &models.StatusPage{
		UID:              uid,
		OrganizationUID:  orgUID,
		Name:             "Test Status Page",
		Slug:             "test-status-page",
		Visibility:       "public",
		IsDefault:        true,
		Enabled:          true,
		ShowAvailability: true,
		ShowResponseTime: true,
		HistoryDays:      90,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := dbService.CreateStatusPage(ctx, page); err != nil {
		return fmt.Errorf("failed to create test status page: %w", err)
	}

	slog.InfoContext(ctx, "Created test status page", "uid", page.UID, "slug", page.Slug)

	return nil
}

// createTestIncidentNotification seeds a check, an active incident on that
// check, and one failed-webhook notification on the incident. This gives the
// incident-notification click-through E2E deterministic data: the incident
// appears in the list, its detail page renders a notification row, and clicking
// that row opens the per-notification detail page (which also exercises the
// error + delivery surfaces because the row is a failed delivery).
func createTestIncidentNotification(ctx context.Context, dbService db.Service, orgUID string, now time.Time) error {
	const (
		checkUID    = "00000000-0000-0000-0000-000000000012"
		incidentUID = "00000000-0000-0000-0000-000000000013"
		notifUID    = "00000000-0000-0000-0000-000000000014"
	)

	check := models.NewCheck(orgUID, "notified-check", "http")
	check.UID = checkUID
	name := "Notified Check"
	check.Name = &name
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.Status = models.CheckStatusDown
	check.StatusChangedAt = &now
	check.CreatedAt = now
	check.UpdatedAt = now
	if err := dbService.CreateCheck(ctx, check); err != nil {
		return fmt.Errorf("failed to create test notified check: %w", err)
	}

	incident := models.NewIncident(orgUID, checkUID, now, "Notified Check is down")
	incident.UID = incidentUID
	incident.CreatedAt = now
	incident.UpdatedAt = now
	// Same shape the incidents service's failureDetails() writes at open time
	// (spec 2026-08-13-11) — seeded by hand here since this is deterministic
	// fixture data, not exercising the real write path. Gives the dash0 E2E a
	// stable incident whose detail page renders the "First failure" card.
	incident.Details = models.JSONMap{
		"failure_reason": "HTTP request failed: 503 Service Unavailable",
		"first_result": models.JSONMap{
			"resultUid":   "00000000-0000-0000-0000-000000000015",
			"status":      "DOWN",
			"region":      "eu",
			"duration":    float32(1.42),
			"periodStart": now,
			"output": models.JSONMap{
				"error":       "HTTP request failed: 503 Service Unavailable",
				"status_code": float64(503),
			},
		},
	}
	if err := dbService.CreateIncident(ctx, incident); err != nil {
		return fmt.Errorf("failed to create test incident: %w", err)
	}

	failedAt := now.Add(time.Second)
	errMsg := "webhook request failed: status 503"
	notif := &models.IncidentNotification{
		UID:             notifUID,
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		EventType:       "incident.created",
		Source:          models.IncidentNotificationSourceCheckConnection,
		ChannelType:     "webhook",
		Status:          models.IncidentNotificationStatusFailed,
		Error:           &errMsg,
		DeliveryDetails: &models.DeliveryDetails{
			HTTPStatusCode:  503,
			RequestURL:      "https://hooks.example.com/incidents",
			RequestBody:     `{"type":"incident.created","data":{}}`,
			ResponseBody:    `{"error":"service unavailable"}`,
			DurationMs:      1234,
			ResponseHeaders: map[string]string{"Retry-After": "120"},
		},
		CreatedAt: now,
		FailedAt:  &failedAt,
	}
	if err := dbService.CreateIncidentNotification(ctx, notif); err != nil {
		return fmt.Errorf("failed to create test incident notification: %w", err)
	}

	slog.InfoContext(ctx, "Created test incident notification",
		"checkUID", checkUID, "incidentUID", incidentUID, "notifUID", notifUID)

	return nil
}

// Fixture UIDs for the failure-response capture incidents (spec 2026-08-20-01).
// Each incident gets its own check so the three renderings stay independent and
// one test's navigation can never land on another's data.
const (
	captureTextCheckUID     = "00000000-0000-0000-0000-000000000016"
	captureTextIncidentUID  = "00000000-0000-0000-0000-000000000017"
	captureTruncCheckUID    = "00000000-0000-0000-0000-000000000018"
	captureTruncIncidentUID = "00000000-0000-0000-0000-000000000019"
	captureBinCheckUID      = "00000000-0000-0000-0000-000000000020"
	captureBinIncidentUID   = "00000000-0000-0000-0000-000000000021"
)

// createTestFailureResponseCaptures seeds three down checks, each with an active
// incident whose Details carry a `failureResponse` block — the exact shape
// incidents.failureDetails() writes when an HTTP check with
// `capture_failure_response` fails (spec 2026-08-20-01).
//
// Three, because the card has three visually distinct states the E2E must pin
// independently: an ordinary text capture (status line + headers table +
// collapsed body), a truncated one (explicit notice), and a binary one
// (metadata only, no body). The capture-free incident seeded by
// createTestIncidentNotification is the negative case — its detail page must
// NOT render the card at all.
//
// Seeded by hand rather than through the real write path: this is deterministic
// fixture data, and the write path has its own Go tests.
func createTestFailureResponseCaptures(
	ctx context.Context, dbService db.Service, orgUID string, now time.Time,
) error {
	// Headers deliberately include an already-redacted Set-Cookie: the
	// redaction happens in the checker, so what reaches the UI is the redacted
	// value, and the E2E asserts the UI renders it rather than a real cookie.
	sharedHeaders := models.JSONMap{
		"Content-Type": "text/html; charset=utf-8",
		"Server":       "acme-lb",
		"Set-Cookie":   "[redacted]",
		"X-Request-Id": "req-4711",
	}

	captures := []struct {
		checkUID    string
		incidentUID string
		slug        string
		name        string
		capture     models.JSONMap
	}{
		{
			checkUID:    captureTextCheckUID,
			incidentUID: captureTextIncidentUID,
			slug:        "captured-check",
			name:        "Captured Check",
			capture: models.JSONMap{
				"url":           "https://acme.com/health",
				"statusLine":    "HTTP/1.1 503 Service Unavailable",
				"statusCode":    float64(503),
				"headers":       sharedHeaders,
				"body":          "<html><body>acme origin unreachable</body></html>",
				"contentType":   "text/html; charset=utf-8",
				"contentLength": float64(48),
				"bodyBytes":     float64(48),
				"bodySha256":    "1e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7",
				"capturedAt":    now,
				"remoteAddr":    "203.0.113.10:443",
				"region":        "eu",
			},
		},
		{
			checkUID:    captureTruncCheckUID,
			incidentUID: captureTruncIncidentUID,
			slug:        "captured-truncated-check",
			name:        "Captured Truncated Check",
			capture: models.JSONMap{
				"url":           "https://acme.com/big",
				"statusLine":    "HTTP/1.1 500 Internal Server Error",
				"statusCode":    float64(500),
				"headers":       sharedHeaders,
				"body":          "aaaaaaaaaaaaaaaaaaaa\n…[truncated]",
				"truncated":     true,
				"contentType":   "text/plain",
				"contentLength": float64(49152),
				"bodyBytes":     float64(49152),
				"bodySha256":    "2e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7",
				"capturedAt":    now,
				"remoteAddr":    "203.0.113.11:443",
				"region":        "eu",
			},
		},
		{
			checkUID:    captureBinCheckUID,
			incidentUID: captureBinIncidentUID,
			slug:        "captured-binary-check",
			name:        "Captured Binary Check",
			capture: models.JSONMap{
				"url":         "https://acme.com/image",
				"statusLine":  "HTTP/1.1 502 Bad Gateway",
				"statusCode":  float64(502),
				"headers":     models.JSONMap{"Content-Type": "image/png", "Server": "acme-lb"},
				"binary":      true,
				"contentType": "image/png",
				"bodyBytes":   float64(2048),
				"bodySha256":  "3e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7",
				"capturedAt":  now,
				"remoteAddr":  "203.0.113.12:443",
				"region":      "us",
			},
		},
	}

	for _, entry := range captures {
		check := models.NewCheck(orgUID, entry.slug, "http")
		check.UID = entry.checkUID
		name := entry.name
		check.Name = &name
		check.Config = models.JSONMap{
			"url":                      "https://acme.com/health",
			"capture_failure_response": true,
		}
		check.Status = models.CheckStatusDown
		check.StatusChangedAt = &now
		check.CreatedAt = now
		check.UpdatedAt = now

		if err := dbService.CreateCheck(ctx, check); err != nil {
			return fmt.Errorf("failed to create capture test check %s: %w", entry.slug, err)
		}

		incident := models.NewIncident(orgUID, entry.checkUID, now, entry.name+" is down")
		incident.UID = entry.incidentUID
		incident.CreatedAt = now
		incident.UpdatedAt = now
		incident.Details = models.JSONMap{
			"failure_reason": "unexpected status code",
			"first_result": models.JSONMap{
				"resultUid":   entry.incidentUID,
				"status":      "DOWN",
				"region":      "eu",
				"duration":    float32(0.75),
				"periodStart": now,
				"output":      models.JSONMap{"error": "unexpected status code"},
			},
			"failureResponse": entry.capture,
		}

		if err := dbService.CreateIncident(ctx, incident); err != nil {
			return fmt.Errorf("failed to create capture test incident %s: %w", entry.slug, err)
		}
	}

	slog.InfoContext(ctx, "Created test failure-response capture incidents",
		"count", len(captures))

	return nil
}

// Fixture UIDs for the SLO happy path (spec 2026-08-20-01).
const (
	sloCheckUID       = "00000000-0000-0000-0000-000000000022"
	sloUID            = "00000000-0000-0000-0000-000000000023"
	sloMonthResultUID = "00000000-0000-0000-0000-000000000024"

	// 9995/10000 = 99.95%, comfortably above the 99.9% target, so the fixture
	// renders "healthy" with a distinctive attainment nobody could produce by
	// accident (100.000% would also be what a broken "no data reads as perfect"
	// bug produces, which is exactly the failure this feature must not have).
	sloFixtureTotalChecks      = 10000
	sloFixtureSuccessfulChecks = 9995
	sloFixtureTargetPct        = 99.9
)

// createTestSLOData seeds one check carrying a month rollup for the CURRENT
// calendar month, plus an objective over it — so the SLO list and detail pages
// have deterministic, non-null attainment to assert on without waiting for a
// probe or an aggregation run.
//
// The rollup is written directly as a `month` row: month rollups are terminal
// (never rolled further, never deleted), so the aggregation job will not touch
// it, and uptimebar.WindowAvailability reads the month tier for exactly this
// kind of window.
func createTestSLOData(ctx context.Context, dbService db.Service, orgUID string, now time.Time) error {
	check := models.NewCheck(orgUID, "slo-api", "http")
	check.UID = sloCheckUID
	name := "SLO API"
	check.Name = &name
	check.Config = models.JSONMap{"url": "https://acme.com/api/health"}
	check.Status = models.CheckStatusUp
	check.StatusChangedAt = &now
	// Disabled on purpose: an enabled fixture check would be probed live in test
	// mode, and every real (failing) probe lands in the SAME calendar month as
	// the seeded rollup — dragging attainment off the fixture value within
	// seconds and making any assertion on it a flake by construction.
	check.Enabled = false
	// Created before the window opens, so the objective reports a full month
	// rather than a partial one clamped to "the check did not exist yet".
	check.CreatedAt = now.AddDate(0, -2, 0)
	check.UpdatedAt = now

	if err := dbService.CreateCheck(ctx, check); err != nil {
		return fmt.Errorf("failed to create SLO test check: %w", err)
	}

	utcNow := now.UTC()
	monthStart := time.Date(utcNow.Year(), utcNow.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)

	status := int(models.ResultStatusUp)
	total := sloFixtureTotalChecks
	successful := sloFixtureSuccessfulChecks
	maintenance := 0
	durationAvg := float32(120)

	rollup := &models.Result{
		UID:              sloMonthResultUID,
		OrganizationUID:  orgUID,
		CheckUID:         sloCheckUID,
		PeriodType:       models.PeriodTypeMonth,
		PeriodStart:      monthStart,
		PeriodEnd:        &monthEnd,
		Status:           &status,
		TotalChecks:      &total,
		SuccessfulChecks: &successful,
		// Explicitly zero rather than nil: this rollup postdates maintenance
		// tagging, so "no maintenance" is a known fact, not missing evidence.
		MaintenanceChecks:           &maintenance,
		MaintenanceSuccessfulChecks: &maintenance,
		DurationAvg:                 &durationAvg,
		CreatedAt:                   now,
	}

	if err := dbService.CreateResult(ctx, rollup); err != nil {
		return fmt.Errorf("failed to create SLO test rollup: %w", err)
	}

	objective := models.NewSLO(orgUID, "API availability", "api-availability", sloFixtureTargetPct)
	objective.UID = sloUID
	objective.CheckUID = &check.UID
	objective.CreatedAt = now
	objective.UpdatedAt = now

	if err := dbService.CreateSLO(ctx, objective); err != nil {
		return fmt.Errorf("failed to create test SLO: %w", err)
	}

	slog.InfoContext(ctx, "Created test SLO", "sloUID", sloUID, "checkUID", sloCheckUID)

	return nil
}

// Fixture UIDs for the incident screenshot attachment (spec 2026-08-21-01).
const (
	shotCheckUID    = "00000000-0000-0000-0000-000000000025"
	shotIncidentUID = "00000000-0000-0000-0000-000000000026"
)

// shotFixturePNG is a tiny but REAL PNG (a 2x2 image), base64-encoded. Real
// bytes rather than a stub: the E2E asserts the <img> actually renders, which a
// blob that is not a decodable image would fail — and that failure would be
// indistinguishable from the card being broken.
const shotFixturePNG = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAF0lEQVR4nGP8z8Dwn4" +
	"GBgYmBgYGBgQEAGgcCAV3wQOsAAAAASUVORK5CYII="

// createTestIncidentScreenshot seeds a down browser check with an active
// incident and a real screenshot attachment written through the normal
// attachment path — topic, details bag, storage blob and all.
//
// Written through the REAL path (unlike the failure-response fixture, which is
// hand-rolled JSON) precisely because the bytes have to exist in the storage
// backend for the signed download URL to serve anything.
func createTestIncidentScreenshot(
	ctx context.Context, dbService db.Service, cfg *config.Config, orgUID string, now time.Time,
) error {
	if cfg == nil {
		slog.InfoContext(ctx, "Skipping incident screenshot fixture: no configuration")

		return nil
	}

	check := models.NewCheck(orgUID, "shot-check", "browser")
	check.UID = shotCheckUID
	name := "Screenshot Check"
	check.Name = &name
	check.Config = models.JSONMap{"url": "https://acme.com/app", "screenshot": true}
	check.Status = models.CheckStatusDown
	check.StatusChangedAt = &now
	check.CreatedAt = now
	check.UpdatedAt = now

	if err := dbService.CreateCheck(ctx, check); err != nil {
		return fmt.Errorf("failed to create screenshot test check: %w", err)
	}

	incident := models.NewIncident(orgUID, shotCheckUID, now, "Screenshot Check is down")
	incident.UID = shotIncidentUID
	incident.CreatedAt = now
	incident.UpdatedAt = now
	incident.Details = models.JSONMap{
		"failure_reason": "keyword check failed",
		"first_result": models.JSONMap{
			"resultUid":   shotIncidentUID,
			"status":      "DOWN",
			"region":      "eu-west",
			"duration":    float32(2.5),
			"periodStart": now,
			"output":      models.JSONMap{"error": "keyword check failed"},
		},
	}

	if err := dbService.CreateIncident(ctx, incident); err != nil {
		return fmt.Errorf("failed to create screenshot test incident: %w", err)
	}

	png, err := base64.StdEncoding.DecodeString(shotFixturePNG)
	if err != nil {
		return fmt.Errorf("failed to decode screenshot fixture: %w", err)
	}

	svc := attachments.NewService(files.NewService(dbService, cfg), dbService, cfg)

	fileUID, err := svc.PutIncidentScreenshot(ctx, orgUID, shotIncidentUID, png, models.JSONMap{
		attachments.DetailKeyCapturedAt: now.UTC().Format(time.RFC3339),
		attachments.DetailKeyRegion:     "eu-west",
		attachments.DetailKeyCheckUID:   shotCheckUID,
		attachments.DetailKeyTrigger:    attachments.TriggerIncidentOpen,
	})
	if err != nil {
		return fmt.Errorf("failed to write screenshot test attachment: %w", err)
	}

	slog.InfoContext(ctx, "Created test incident screenshot",
		"incidentUID", shotIncidentUID, "fileUID", fileUID)

	return nil
}
