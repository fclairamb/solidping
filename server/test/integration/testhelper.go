// Package integration provides integration tests for the SolidPing API.
package integration

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/app"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	// TestOrgSlug is the slug for the test organization.
	TestOrgSlug = "test-org"
	// TestOrgName is the name for the test organization.
	TestOrgName = "Test Organization"
	// TestOrg2Slug is the slug for the second test organization.
	TestOrg2Slug = "test-org-2"
	// TestOrg2Name is the name for the second test organization.
	TestOrg2Name = "Test Org 2"
	// TestUserEmail is the email for the test user.
	TestUserEmail = "test@example.com"
	// TestUserPassword is the password for the test user.
	TestUserPassword = "testpassword123"
	// TestUserName is the display name for the test user.
	TestUserName = "Test User"
	// TestUserAvatarURL is the avatar URL for the test user.
	TestUserAvatarURL = "https://example.com/avatar.jpg"
)

// TestServer wraps a test server and provides helpers.
type TestServer struct {
	Server     *app.Server
	HTTPServer *httptest.Server
	Client     *client.SolidPingClient
}

// NewTestServer creates a new test server with in-memory SQLite database.
func NewTestServer(t *testing.T) *TestServer {
	t.Helper()

	ctx := t.Context()

	const refreshTokenExpiryHours = 24

	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen: ":0",
		},
		Database: config.DatabaseConfig{
			Type: "sqlite-memory",
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-secret-key-for-integration-tests",
			AccessTokenExpiry:  1 * time.Hour,
			RefreshTokenExpiry: refreshTokenExpiryHours * time.Hour,
		},
	}

	server, err := app.NewServer(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	if initErr := server.Initialize(ctx); initErr != nil {
		t.Fatalf("failed to initialize database: %v", initErr)
	}

	t.Cleanup(func() {
		_ = server.Close(context.Background())
	})

	httpServer := httptest.NewServer(server.Handler())

	apiClient, err := client.New(client.Config{
		BaseURL: httpServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create API client: %v", err)
	}

	testServer := &TestServer{
		Server:     server,
		HTTPServer: httpServer,
		Client:     apiClient,
	}

	// Create test organization and user
	testServer.setupTestData(ctx, t)

	return testServer
}

// setupTestData creates test organizations, user, memberships, and tokens.
func (ts *TestServer) setupTestData(ctx context.Context, t *testing.T) {
	t.Helper()

	dbService := ts.Server.DBService()
	now := time.Now()

	org := ts.createTestOrgs(ctx, t, dbService, now)
	user := ts.createTestUser(ctx, t, dbService, now)
	ts.createTestMemberships(ctx, t, dbService, now, org, user.UID)
}

// createTestOrgs creates the test organizations and returns the primary org.
func (ts *TestServer) createTestOrgs(
	ctx context.Context, t *testing.T, dbService db.Service, now time.Time,
) *models.Organization {
	t.Helper()

	org := &models.Organization{
		UID:  "10000000-0000-0000-0000-000000000001",
		Slug: TestOrgSlug, Name: TestOrgName,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := dbService.CreateOrganization(ctx, org); err != nil {
		t.Fatalf("failed to create test organization: %v", err)
	}

	org2 := &models.Organization{
		UID:  "10000000-0000-0000-0000-000000000010",
		Slug: TestOrg2Slug, Name: TestOrg2Name,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := dbService.CreateOrganization(ctx, org2); err != nil {
		t.Fatalf("failed to create test organization 2: %v", err)
	}

	return org
}

// createTestUser creates the test user and returns it.
func (ts *TestServer) createTestUser(
	ctx context.Context, t *testing.T, dbService db.Service, now time.Time,
) *models.User {
	t.Helper()

	passwordHash, err := passwords.Hash(TestUserPassword)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	user := &models.User{
		UID:          "10000000-0000-0000-0000-000000000002",
		Email:        TestUserEmail,
		Name:         TestUserName,
		AvatarURL:    TestUserAvatarURL,
		PasswordHash: &passwordHash,
		CreatedAt:    now, UpdatedAt: now,
	}
	if err := dbService.CreateUser(ctx, user); err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	return user
}

// createTestMemberships creates org memberships and a PAT token.
func (ts *TestServer) createTestMemberships(
	ctx context.Context, t *testing.T, dbService db.Service,
	now time.Time, org *models.Organization, userUID string,
) {
	t.Helper()

	membership := &models.OrganizationMember{
		UID:             "10000000-0000-0000-0000-000000000004",
		UserUID:         userUID,
		OrganizationUID: org.UID,
		Role:            models.MemberRoleAdmin,
		JoinedAt:        &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := dbService.CreateOrganizationMember(ctx, membership); err != nil {
		t.Fatalf("failed to create test membership: %v", err)
	}

	membership2 := &models.OrganizationMember{
		UID:             "10000000-0000-0000-0000-000000000005",
		UserUID:         userUID,
		OrganizationUID: "10000000-0000-0000-0000-000000000010",
		Role:            models.MemberRoleUser,
		JoinedAt:        &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := dbService.CreateOrganizationMember(ctx, membership2); err != nil {
		t.Fatalf("failed to create test membership 2: %v", err)
	}

	patToken := &models.UserToken{
		UID:             "10000000-0000-0000-0000-000000000003",
		UserUID:         userUID,
		OrganizationUID: &org.UID,
		Token:           "pat_test",
		Type:            models.TokenTypePAT,
		Properties:      models.JSONMap{"name": "Test PAT"},
		ExpiresAt:       nil,
		CreatedAt:       now, UpdatedAt: now,
	}
	if err := dbService.CreateUserToken(ctx, patToken); err != nil {
		t.Fatalf("failed to create test PAT token: %v", err)
	}
}

// Close closes the test server.
func (ts *TestServer) Close() {
	ts.HTTPServer.Close()

	if ts.Server != nil {
		_ = ts.Server.Close(context.Background())
	}
}

// NewClient creates a new API client connected to the test server.
func (ts *TestServer) NewClient() *client.SolidPingClient {
	apiClient, err := client.New(client.Config{
		BaseURL: ts.HTTPServer.URL,
	})
	if err != nil {
		panic(err) // Should never happen in tests
	}

	return apiClient
}

// NewAuthenticatedClient creates a new API client with the given token.
func (ts *TestServer) NewAuthenticatedClient(token string) *client.SolidPingClient {
	apiClient, err := client.New(client.Config{
		BaseURL: ts.HTTPServer.URL,
		Token:   token,
	})
	if err != nil {
		panic(err) // Should never happen in tests
	}

	return apiClient
}
