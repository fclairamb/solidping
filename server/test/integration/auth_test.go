package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	loginActionOrgRedirect = "orgRedirect"
	loginActionOrgChoice   = "orgChoice"
	loginActionNoOrg       = "noOrg"
)

func TestLogin(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()

	tests := []struct {
		name        string
		email       string
		password    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid credentials",
			email:    TestUserEmail,
			password: TestUserPassword,
			wantErr:  false,
		},
		{
			name:        "invalid email",
			email:       "wrong@example.com",
			password:    TestUserPassword,
			wantErr:     true,
			errContains: "Invalid credentials",
		},
		{
			name:        "invalid password",
			email:       TestUserEmail,
			password:    "wrongpassword",
			wantErr:     true,
			errContains: "Invalid credentials",
		},
		{
			name:        "empty email",
			email:       "",
			password:    TestUserPassword,
			wantErr:     true,
			errContains: "email",
		},
		{
			name:        "empty password",
			email:       TestUserEmail,
			password:    "",
			wantErr:     true,
			errContains: "422",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			testLoginCase(ctx, t, testServer, testCase.email, testCase.password, testCase.wantErr, testCase.errContains)
		})
	}
}

func testLoginCase(
	ctx context.Context,
	t *testing.T,
	testServer *TestServer,
	email, password string,
	wantErr bool,
	errContains string,
) {
	t.Helper()

	apiClient := testServer.NewClient()

	resp, err := apiClient.Login(ctx, TestOrgSlug, email, password)
	if wantErr {
		if err == nil {
			t.Error("expected error, got nil")

			return
		}

		if errContains != "" && !strings.Contains(err.Error(), errContains) {
			t.Errorf("error %q does not contain %q", err.Error(), errContains)
		}

		return
	}

	if err != nil {
		t.Errorf("unexpected error: %v", err)

		return
	}

	if resp.AccessToken == nil || *resp.AccessToken == "" {
		t.Error("expected access token, got empty")
	}

	if resp.RefreshToken == nil || *resp.RefreshToken == "" {
		t.Error("expected refresh token, got empty")
	}

	if resp.ExpiresIn == nil || *resp.ExpiresIn <= 0 {
		t.Error("expected positive expires_in")
	}
}

func TestLoginWrongOrg(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// With org-as-preference, logging in with a non-existent org should succeed
	// and redirect to available orgs (orgChoice since the test user has 2 orgs)
	resp, err := apiClient.Login(ctx, "non-existent-org", TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("expected successful login with org preference, got error: %v", err)
	}

	if resp.LoginAction == nil || string(*resp.LoginAction) != loginActionOrgChoice {
		t.Errorf("expected loginAction 'orgChoice', got %v", resp.LoginAction)
	}

	if resp.Organizations == nil || len(*resp.Organizations) != 2 {
		t.Errorf("expected 2 available organizations, got %v", resp.Organizations)
	}

	if resp.Organization == nil || resp.Organization.Slug == nil {
		t.Error("expected an organization to be selected")
	}
}

func TestMe(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()

	// First login to get a token
	apiClient := testServer.NewClient()

	loginResp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Now use the token to get user info
	meResp, err := apiClient.Me(ctx)
	if err != nil {
		t.Fatalf("failed to get user info: %v", err)
	}

	if meResp.User == nil || meResp.User.Email == nil || string(*meResp.User.Email) != TestUserEmail {
		t.Errorf("expected user email %q, got %v", TestUserEmail, meResp.User)
	}

	if meResp.Organization == nil || meResp.Organization.Slug == nil || *meResp.Organization.Slug != TestOrgSlug {
		t.Errorf("expected org slug %q, got %v", TestOrgSlug, meResp.Organization)
	}

	if meResp.User == nil || meResp.User.Role == nil || string(*meResp.User.Role) != "admin" {
		t.Errorf("expected role 'admin', got %v", meResp.User)
	}

	// Verify user name and avatar
	if meResp.User.Name == nil || *meResp.User.Name != TestUserName {
		t.Errorf("expected user name %q, got %v", TestUserName, meResp.User.Name)
	}

	if meResp.User.AvatarUrl == nil || *meResp.User.AvatarUrl != TestUserAvatarURL {
		t.Errorf("expected avatar URL %q, got %v", TestUserAvatarURL, meResp.User.AvatarUrl)
	}

	// Verify organization name
	if meResp.Organization.Name == nil || *meResp.Organization.Name != TestOrgName {
		t.Errorf("expected org name %q, got %v", TestOrgName, meResp.Organization.Name)
	}

	// Verify organizations list
	if meResp.Organizations == nil || len(*meResp.Organizations) < 2 {
		t.Errorf("expected at least 2 organizations, got %v", meResp.Organizations)
	}

	// The token was set automatically during login
	_ = loginResp
}

func TestLoginIncludesUserInfo(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	loginResp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Verify user name in login response
	if loginResp.User == nil || loginResp.User.Name == nil || *loginResp.User.Name != TestUserName {
		t.Errorf("expected user name %q in login response, got %v", TestUserName, loginResp.User)
	}

	// Verify avatar URL in login response
	if loginResp.User == nil || loginResp.User.AvatarUrl == nil || *loginResp.User.AvatarUrl != TestUserAvatarURL {
		t.Errorf("expected avatar URL %q in login response, got %v", TestUserAvatarURL, loginResp.User)
	}

	// Verify organization name in login response
	if loginResp.Organization == nil || loginResp.Organization.Name == nil || *loginResp.Organization.Name != TestOrgName {
		t.Errorf("expected org name %q in login response, got %v", TestOrgName, loginResp.Organization)
	}
}

func TestLoginOrgPreference(t *testing.T) {
	t.Parallel()

	t.Run("normal login when member of requested org", func(t *testing.T) {
		t.Parallel()

		testServer := NewTestServer(t)
		ctx := t.Context()
		apiClient := testServer.NewClient()

		resp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
		if err != nil {
			t.Fatalf("failed to login: %v", err)
		}

		// loginAction should be empty for normal login
		if resp.LoginAction != nil && string(*resp.LoginAction) != "" {
			t.Errorf("expected empty loginAction, got %v", *resp.LoginAction)
		}

		if resp.Organization == nil || resp.Organization.Slug == nil || *resp.Organization.Slug != TestOrgSlug {
			t.Errorf("expected org slug %q, got %v", TestOrgSlug, resp.Organization)
		}

		// Should include organizations list
		if resp.Organizations == nil || len(*resp.Organizations) < 1 {
			t.Error("expected organizations list to be populated")
		}
	})

	t.Run("auto-redirect to only org when not member of requested", func(t *testing.T) {
		t.Parallel()

		testServer := NewTestServer(t)
		ctx := t.Context()
		dbService := testServer.Server.DBService()
		now := time.Now()

		// Create a user with only one org membership (different from "nonexistent-org")
		passwordHash, err := passwords.Hash("singleorgpass")
		if err != nil {
			t.Fatalf("failed to hash password: %v", err)
		}

		singleOrgUser := &models.User{
			UID:          "20000000-0000-0000-0000-000000000001",
			Email:        "singleorg@example.com",
			Name:         "Single Org User",
			PasswordHash: &passwordHash,
			CreatedAt:    now, UpdatedAt: now,
		}
		if err := dbService.CreateUser(ctx, singleOrgUser); err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		member := &models.OrganizationMember{
			UID:             "20000000-0000-0000-0000-000000000002",
			UserUID:         singleOrgUser.UID,
			OrganizationUID: "10000000-0000-0000-0000-000000000001", // test-org
			Role:            models.MemberRoleUser,
			JoinedAt:        &now, CreatedAt: now, UpdatedAt: now,
		}
		if err := dbService.CreateOrganizationMember(ctx, member); err != nil {
			t.Fatalf("failed to create membership: %v", err)
		}

		apiClient := testServer.NewClient()
		resp, loginErr := apiClient.Login(ctx, "nonexistent-org", "singleorg@example.com", "singleorgpass")
		if loginErr != nil {
			t.Fatalf("expected successful login, got error: %v", loginErr)
		}

		if resp.LoginAction == nil || string(*resp.LoginAction) != loginActionOrgRedirect {
			t.Errorf("expected loginAction 'orgRedirect', got %v", resp.LoginAction)
		}

		if resp.Organization == nil || resp.Organization.Slug == nil || *resp.Organization.Slug != TestOrgSlug {
			t.Errorf("expected auto-redirect to %q, got %v", TestOrgSlug, resp.Organization)
		}

		if resp.Organizations == nil || len(*resp.Organizations) != 1 {
			t.Errorf("expected 1 organization, got %v", resp.Organizations)
		}
	})

	t.Run("org choice when multiple orgs but not requested", func(t *testing.T) {
		t.Parallel()

		testServer := NewTestServer(t)
		ctx := t.Context()
		apiClient := testServer.NewClient()

		// Default test user has 2 orgs (test-org and test-org-2)
		resp, err := apiClient.Login(ctx, "nonexistent-org", TestUserEmail, TestUserPassword)
		if err != nil {
			t.Fatalf("expected successful login, got error: %v", err)
		}

		if resp.LoginAction == nil || string(*resp.LoginAction) != loginActionOrgChoice {
			t.Errorf("expected loginAction 'orgChoice', got %v", resp.LoginAction)
		}

		if resp.Organizations == nil || len(*resp.Organizations) != 2 {
			t.Errorf("expected 2 available organizations, got %v", resp.Organizations)
		}

		if resp.Organization == nil || resp.Organization.Slug == nil {
			t.Error("expected an organization to be selected as default")
		}

		if resp.AccessToken == nil || *resp.AccessToken == "" {
			t.Error("expected access token to be set")
		}
	})

	t.Run("no org when user has no memberships", func(t *testing.T) {
		t.Parallel()

		testServer := NewTestServer(t)
		ctx := t.Context()
		dbService := testServer.Server.DBService()
		now := time.Now()

		// Create a user with no org memberships
		passwordHash, err := passwords.Hash("noorgpass123")
		if err != nil {
			t.Fatalf("failed to hash password: %v", err)
		}

		noOrgUser := &models.User{
			UID:          "30000000-0000-0000-0000-000000000001",
			Email:        "noorg@example.com",
			Name:         "No Org User",
			PasswordHash: &passwordHash,
			CreatedAt:    now, UpdatedAt: now,
		}
		if err := dbService.CreateUser(ctx, noOrgUser); err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		apiClient := testServer.NewClient()
		resp, loginErr := apiClient.Login(ctx, "some-org", "noorg@example.com", "noorgpass123")
		if loginErr != nil {
			t.Fatalf("expected successful login, got error: %v", loginErr)
		}

		if resp.LoginAction == nil || string(*resp.LoginAction) != loginActionNoOrg {
			t.Errorf("expected loginAction 'noOrg', got %v", resp.LoginAction)
		}

		if resp.Organization != nil {
			t.Errorf("expected nil organization, got %v", resp.Organization)
		}

		if resp.Organizations != nil && len(*resp.Organizations) > 0 {
			t.Errorf("expected empty organizations, got %v", resp.Organizations)
		}

		if resp.AccessToken == nil || *resp.AccessToken == "" {
			t.Error("expected access token even for no-org user")
		}
	})

	t.Run("super admin with nonexistent org gets orgChoice", func(t *testing.T) {
		t.Parallel()

		testServer := NewTestServer(t)
		ctx := t.Context()
		dbService := testServer.Server.DBService()
		now := time.Now()

		// Create a super admin user with org memberships
		passwordHash, err := passwords.Hash("superadminpass")
		if err != nil {
			t.Fatalf("failed to hash password: %v", err)
		}

		superAdminUser := &models.User{
			UID:          "40000000-0000-0000-0000-000000000001",
			Email:        "superadmin@example.com",
			Name:         "Super Admin",
			PasswordHash: &passwordHash,
			SuperAdmin:   true,
			CreatedAt:    now, UpdatedAt: now,
		}
		if err := dbService.CreateUser(ctx, superAdminUser); err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		// Add memberships to both test orgs
		for i, orgUID := range []string{
			"10000000-0000-0000-0000-000000000001",
			"10000000-0000-0000-0000-000000000010",
		} {
			member := &models.OrganizationMember{
				UID:             fmt.Sprintf("40000000-0000-0000-0000-00000000000%d", i+2),
				UserUID:         superAdminUser.UID,
				OrganizationUID: orgUID,
				Role:            models.MemberRoleAdmin,
				JoinedAt:        &now, CreatedAt: now, UpdatedAt: now,
			}
			if err := dbService.CreateOrganizationMember(ctx, member); err != nil {
				t.Fatalf("failed to create membership: %v", err)
			}
		}

		apiClient := testServer.NewClient()
		resp, loginErr := apiClient.Login(ctx, "nonexistent-org", "superadmin@example.com", "superadminpass")
		if loginErr != nil {
			t.Fatalf("expected successful login, got error: %v", loginErr)
		}

		if resp.LoginAction == nil || string(*resp.LoginAction) != loginActionOrgChoice {
			t.Errorf("expected loginAction 'orgChoice', got %v", resp.LoginAction)
		}

		if resp.Organizations == nil || len(*resp.Organizations) < 2 {
			t.Errorf("expected at least 2 organizations, got %v", resp.Organizations)
		}

		if resp.Organization == nil || resp.Organization.Slug == nil {
			t.Error("expected an organization to be selected as default")
		}
	})
}

func TestSwitchOrg(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login to test-org first
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Switch to test-org-2
	switchResp, err := apiClient.SwitchOrg(ctx, TestOrg2Slug)
	if err != nil {
		t.Fatalf("failed to switch org: %v", err)
	}

	// Verify the response has the new org
	if switchResp.Organization == nil || switchResp.Organization.Slug == nil ||
		*switchResp.Organization.Slug != TestOrg2Slug {
		t.Errorf("expected org slug %q, got %v", TestOrg2Slug, switchResp.Organization)
	}

	if switchResp.Organization.Name == nil || *switchResp.Organization.Name != TestOrg2Name {
		t.Errorf("expected org name %q, got %v", TestOrg2Name, switchResp.Organization.Name)
	}

	// Verify user info is preserved
	if switchResp.User == nil || switchResp.User.Name == nil || *switchResp.User.Name != TestUserName {
		t.Errorf("expected user name %q after switch, got %v", TestUserName, switchResp.User)
	}

	// Verify Me endpoint reflects the new org
	meResp, err := apiClient.Me(ctx)
	if err != nil {
		t.Fatalf("failed to get user info after switch: %v", err)
	}

	if meResp.Organization == nil || meResp.Organization.Slug == nil || *meResp.Organization.Slug != TestOrg2Slug {
		t.Errorf("expected org slug %q in /me after switch, got %v", TestOrg2Slug, meResp.Organization)
	}
}

func TestMeWithoutAuth(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	_, err := apiClient.Me(ctx)
	if err == nil {
		t.Error("expected error for unauthenticated request")
	}
}

func TestMeWithPAT(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()

	// Use the pre-created PAT token
	apiClient := testServer.NewAuthenticatedClient("pat_test")

	meResp, err := apiClient.Me(ctx)
	if err != nil {
		t.Fatalf("failed to get user info with PAT: %v", err)
	}

	if meResp.User == nil || meResp.User.Email == nil || string(*meResp.User.Email) != TestUserEmail {
		t.Errorf("expected user email %q, got %v", TestUserEmail, meResp.User)
	}
}

func TestRefresh(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// First login to get tokens
	loginResp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Clear the current token to use refresh token
	apiClient.SetToken("")

	// Refresh the token
	if loginResp.RefreshToken == nil {
		t.Fatal("login response has no refresh token")
	}

	refreshResp, err := apiClient.Refresh(ctx, *loginResp.RefreshToken)
	if err != nil {
		t.Fatalf("failed to refresh token: %v", err)
	}

	if refreshResp.AccessToken == nil || *refreshResp.AccessToken == "" {
		t.Error("expected new access token, got empty")
	}

	if refreshResp.ExpiresIn == nil || *refreshResp.ExpiresIn <= 0 {
		t.Error("expected positive expires_in")
	}

	// Verify the new token works
	meResp, err := apiClient.Me(ctx)
	if err != nil {
		t.Fatalf("failed to get user info with refreshed token: %v", err)
	}

	if meResp.User == nil || meResp.User.Email == nil || string(*meResp.User.Email) != TestUserEmail {
		t.Errorf("expected user email %q, got %v", TestUserEmail, meResp.User)
	}
}

func TestRefreshInvalidToken(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	_, err := apiClient.Refresh(ctx, "invalid-refresh-token")
	if err == nil {
		t.Error("expected error for invalid refresh token")
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// First login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Logout
	_, err = apiClient.Logout(ctx, false)
	if err != nil {
		t.Fatalf("failed to logout: %v", err)
	}
}

func TestCreateAndRevokeToken(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// First login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Create and validate token
	createResp := createAndValidateToken(ctx, t, apiClient)

	// Find the created token UID
	createdTokenUID := findTokenUIDByName(ctx, t, apiClient, "My Test Token")

	// Revoke the token
	err = apiClient.RevokeToken(ctx, *createdTokenUID)
	if err != nil {
		t.Fatalf("failed to revoke token: %v", err)
	}

	// Verify token is revoked
	verifyTokenRevoked(ctx, t, apiClient, *createdTokenUID)

	_ = createResp // Keep createResp for potential future use
}

func createAndValidateToken(
	ctx context.Context,
	t *testing.T,
	apiClient *client.SolidPingClient,
) *client.CreateTokenResponse {
	t.Helper()

	createResp, err := apiClient.CreateToken(ctx, TestOrgSlug, "My Test Token", nil)
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	if createResp.Token == nil || *createResp.Token == "" {
		t.Error("expected token value, got empty")
	}

	if createResp.Token != nil && !strings.HasPrefix(*createResp.Token, "pat_") {
		t.Errorf("expected token to start with 'pat_', got %q", *createResp.Token)
	}

	if createResp.Name == nil || *createResp.Name != "My Test Token" {
		t.Errorf("expected name 'My Test Token', got %v", createResp.Name)
	}

	return createResp
}

func findTokenUIDByName(ctx context.Context, t *testing.T, apiClient *client.SolidPingClient, name string) *string {
	t.Helper()

	listResp, err := apiClient.GetTokens(ctx)
	if err != nil {
		t.Fatalf("failed to list tokens: %v", err)
	}

	if listResp.Data == nil {
		t.Fatal("list response data is nil")
	}

	for index := range *listResp.Data {
		tok := &(*listResp.Data)[index]
		if tok.Name != nil && *tok.Name == name {
			if tok.Uid != nil {
				uidStr := tok.Uid.String()

				return &uidStr
			}
		}
	}

	t.Errorf("token with name %q not found in list", name)

	return nil
}

func verifyTokenRevoked(ctx context.Context, t *testing.T, apiClient *client.SolidPingClient, tokenUID string) {
	t.Helper()

	listResp, err := apiClient.GetTokens(ctx)
	if err != nil {
		t.Fatalf("failed to list tokens after revoke: %v", err)
	}

	if listResp.Data != nil {
		for index := range *listResp.Data {
			tok := &(*listResp.Data)[index]
			if tok.Uid != nil && tok.Uid.String() == tokenUID {
				t.Error("revoked token still found in list")

				return
			}
		}
	}
}

func TestGetTokensFiltered(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Get all tokens (filtering not yet implemented in API)
	listResp, err := apiClient.GetTokens(ctx)
	if err != nil {
		t.Fatalf("failed to list tokens: %v", err)
	}

	// Just verify we got some tokens back
	if listResp.Data == nil || len(*listResp.Data) == 0 {
		t.Error("expected at least one token in the list")
	}
}

func TestInvalidOrgAccess(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login to test-org
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Try to access a different org via org-scoped endpoint (CreateToken)
	_, err = apiClient.CreateToken(ctx, "other-org", "test-token", nil)
	if err == nil {
		t.Error("expected error when accessing different org")
	}
}

func TestCreateTokenNoExpiry(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Create a token without expiration
	createResp, err := apiClient.CreateToken(ctx, TestOrgSlug, "No Expiry Token", nil)
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	if createResp.ExpiresAt != nil {
		t.Errorf("expected nil expiresAt for no-expiry token, got %v", *createResp.ExpiresAt)
	}

	// Verify it appears in the list without expiry
	listResp, err := apiClient.GetTokens(ctx)
	if err != nil {
		t.Fatalf("failed to list tokens: %v", err)
	}

	found := false

	if listResp.Data != nil {
		for index := range *listResp.Data {
			tok := &(*listResp.Data)[index]
			if tok.Name != nil && *tok.Name == "No Expiry Token" {
				found = true

				if tok.ExpiresAt != nil {
					t.Errorf("expected nil expiresAt in list, got %v", *tok.ExpiresAt)
				}
			}
		}
	}

	if !found {
		t.Error("created token not found in list")
	}
}

func TestCreateTokenWithExpiry(t *testing.T) {
	t.Parallel()

	testServer := NewTestServer(t)

	ctx := t.Context()
	apiClient := testServer.NewClient()

	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	if err != nil {
		t.Fatalf("failed to login: %v", err)
	}

	// Create a token with explicit expiry
	expiresAt := time.Now().Add(24 * time.Hour)

	createResp, err := apiClient.CreateToken(ctx, TestOrgSlug, "Expiring Token", &expiresAt)
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	if createResp.ExpiresAt == nil {
		t.Error("expected non-nil expiresAt for expiring token")
	}

	// Verify it appears in the list with expiry set
	listResp, err := apiClient.GetTokens(ctx)
	if err != nil {
		t.Fatalf("failed to list tokens: %v", err)
	}

	found := false

	if listResp.Data != nil {
		for index := range *listResp.Data {
			tok := &(*listResp.Data)[index]
			if tok.Name != nil && *tok.Name == "Expiring Token" {
				found = true

				if tok.ExpiresAt == nil {
					t.Error("expected non-nil expiresAt in list")
				}
			}
		}
	}

	if !found {
		t.Error("created token not found in list")
	}
}
