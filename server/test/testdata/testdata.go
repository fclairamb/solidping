// Package testdata provides utilities for creating test data.
package testdata

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
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

	slog.InfoContext(ctx, "Test data creation completed successfully")

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
