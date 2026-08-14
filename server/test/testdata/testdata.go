// Package testdata provides utilities for creating test data.
package testdata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// CreateTestData creates deterministic test data for test mode.
func CreateTestData(ctx context.Context, dbService db.Service) error {
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
