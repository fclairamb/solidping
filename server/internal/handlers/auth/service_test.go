package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

func setupAuthTestService(t *testing.T) (*Service, db.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}

	svc := NewService(dbService, cfg, nil, nil)

	return svc, dbService, ctx
}

func setupAuthTestServiceWithConfig(t *testing.T, baseURL string) (*Service, db.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	fullCfg := &config.Config{
		Server: config.ServerConfig{
			BaseURL: baseURL,
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	svc := NewService(dbService, fullCfg.Auth, fullCfg, nil)

	return svc, dbService, ctx
}

func TestCreateInvitation(t *testing.T) {
	t.Parallel()

	t.Run("uses configured base URL in invite URL", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

		org := models.NewOrganization("invite-org", "Invite Org")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("inviter@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.CreateInvitation(ctx, "invite-org", user.UID, InviteRequest{
			Email:     "invitee@example.com",
			Role:      "user",
			ExpiresIn: "24h",
			App:       "dash0",
		})
		r.NoError(err)
		r.Contains(resp.InviteURL, "http://127.0.0.1:4000/dash0/invite/")
		r.NotContains(resp.InviteURL, "localhost")
	})

	t.Run("uses custom base URL", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://solidping.example.com")

		org := models.NewOrganization("invite-org2", "Invite Org 2")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("inviter2@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.CreateInvitation(ctx, "invite-org2", user.UID, InviteRequest{
			Email:     "invitee2@example.com",
			Role:      "admin",
			ExpiresIn: "1h",
			App:       "dash0",
		})
		r.NoError(err)
		r.Contains(resp.InviteURL, "https://solidping.example.com/dash0/invite/")
	})

	t.Run("uses dash app in URL", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

		org := models.NewOrganization("invite-org3", "Invite Org 3")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("inviter3@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.CreateInvitation(ctx, "invite-org3", user.UID, InviteRequest{
			Email:     "invitee3@example.com",
			Role:      "user",
			ExpiresIn: "24h",
			App:       "dash",
		})
		r.NoError(err)
		r.Contains(resp.InviteURL, "http://127.0.0.1:4000/dash/invite/")
	})

	t.Run("rejects invalid app", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

		org := models.NewOrganization("invite-org4", "Invite Org 4")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("inviter4@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		_, err := svc.CreateInvitation(ctx, "invite-org4", user.UID, InviteRequest{
			Email:     "invitee4@example.com",
			Role:      "user",
			ExpiresIn: "24h",
			App:       "invalid",
		})
		r.ErrorIs(err, ErrInvalidApp)
	})
}

func TestGetUserInfo(t *testing.T) {
	t.Parallel()

	t.Run("returns user name and avatarUrl", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		org := models.NewOrganization("info-org", "Info Organization")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("info@example.com")
		user.Name = "Alice Smith"
		user.AvatarURL = "https://example.com/alice.jpg"
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.GetUserInfo(ctx, &Claims{UserUID: user.UID, OrgSlug: org.Slug})
		r.NoError(err)
		r.Equal("Alice Smith", resp.User.Name)
		r.Equal("https://example.com/alice.jpg", resp.User.AvatarURL)
		r.Equal("admin", resp.User.Role)
		r.Equal(user.Email, resp.User.Email)
	})

	t.Run("returns organization name", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		org := models.NewOrganization("named-org", "My Organization")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("orgname@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.GetUserInfo(ctx, &Claims{UserUID: user.UID, OrgSlug: org.Slug})
		r.NoError(err)
		r.Equal("My Organization", resp.Organization.Name)
		r.Equal("named-org", resp.Organization.Slug)
	})

	t.Run("returns organizations list for multi-org user", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		orgA := models.NewOrganization("org-a", "Org A")
		orgB := models.NewOrganization("org-b", "Org B")
		orgC := models.NewOrganization("org-c", "Org C")
		r.NoError(dbSvc.CreateOrganization(ctx, orgA))
		r.NoError(dbSvc.CreateOrganization(ctx, orgB))
		r.NoError(dbSvc.CreateOrganization(ctx, orgC))

		user := models.NewUser("multi@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgA.UID, user.UID, models.MemberRoleAdmin)))
		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgB.UID, user.UID, models.MemberRoleUser)))
		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgC.UID, user.UID, models.MemberRoleViewer)))

		resp, err := svc.GetUserInfo(ctx, &Claims{UserUID: user.UID, OrgSlug: orgA.Slug})
		r.NoError(err)
		r.Len(resp.Organizations, 3)

		bySlug := make(map[string]OrganizationSummary)
		for _, o := range resp.Organizations {
			bySlug[o.Slug] = o
		}

		r.Equal("Org A", bySlug["org-a"].Name)
		r.Equal("admin", bySlug["org-a"].Role)
		r.Equal("Org B", bySlug["org-b"].Name)
		r.Equal("user", bySlug["org-b"].Role)
		r.Equal("Org C", bySlug["org-c"].Name)
		r.Equal("viewer", bySlug["org-c"].Role)
	})

	t.Run("returns empty fields when not set", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		org := models.NewOrganization("empty-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		user := models.NewUser("empty@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.GetUserInfo(ctx, &Claims{UserUID: user.UID, OrgSlug: org.Slug})
		r.NoError(err)
		r.Empty(resp.User.Name)
		r.Empty(resp.User.AvatarURL)
		r.Empty(resp.Organization.Name)
	})
}

func TestLoginUserInfo(t *testing.T) {
	t.Parallel()

	t.Run("includes user name and avatarUrl", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		org := models.NewOrganization("login-org", "Login Org")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		passwordHash, err := passwords.Hash("testpass")
		r.NoError(err)

		user := models.NewUser("login@example.com")
		user.Name = "Login User"
		user.AvatarURL = "https://example.com/login.jpg"
		user.PasswordHash = &passwordHash
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.Login(ctx, "login-org", "login@example.com", "testpass", Context{})
		r.NoError(err)
		r.Equal("Login User", resp.User.Name)
		r.Equal("https://example.com/login.jpg", resp.User.AvatarURL)
		r.NotEmpty(resp.AccessToken)
	})

	t.Run("includes organization name", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		org := models.NewOrganization("login-org-name", "My Company")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		passwordHash, err := passwords.Hash("testpass")
		r.NoError(err)

		user := models.NewUser("login2@example.com")
		user.PasswordHash = &passwordHash
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		resp, err := svc.Login(ctx, "login-org-name", "login2@example.com", "testpass", Context{})
		r.NoError(err)
		r.Equal("My Company", resp.Organization.Name)
		r.Equal("login-org-name", resp.Organization.Slug)
	})
}

func TestSwitchOrgUserInfo(t *testing.T) {
	t.Parallel()

	t.Run("returns new org info", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		orgA := models.NewOrganization("switch-a", "Org Alpha")
		orgB := models.NewOrganization("switch-b", "Org Beta")
		r.NoError(dbSvc.CreateOrganization(ctx, orgA))
		r.NoError(dbSvc.CreateOrganization(ctx, orgB))

		user := models.NewUser("switch@example.com")
		user.Name = "Switch User"
		user.AvatarURL = "https://example.com/switch.jpg"
		r.NoError(dbSvc.CreateUser(ctx, user))

		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgA.UID, user.UID, models.MemberRoleAdmin)))
		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgB.UID, user.UID, models.MemberRoleUser)))

		resp, err := svc.SwitchOrg(ctx, user.UID, "switch-b", Context{})
		r.NoError(err)
		r.Equal("Org Beta", resp.Organization.Name)
		r.Equal("switch-b", resp.Organization.Slug)
		r.Equal("Switch User", resp.User.Name)
		r.Equal("https://example.com/switch.jpg", resp.User.AvatarURL)
	})

	t.Run("returns correct role for target org", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestService(t)

		orgA := models.NewOrganization("role-a", "Role A")
		orgB := models.NewOrganization("role-b", "Role B")
		r.NoError(dbSvc.CreateOrganization(ctx, orgA))
		r.NoError(dbSvc.CreateOrganization(ctx, orgB))

		user := models.NewUser("role@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgA.UID, user.UID, models.MemberRoleAdmin)))
		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(orgB.UID, user.UID, models.MemberRoleViewer)))

		resp, err := svc.SwitchOrg(ctx, user.UID, "role-b", Context{})
		r.NoError(err)
		r.Equal("viewer", resp.User.Role)
	})
}

func TestRefreshUserInfo(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("refresh-org", "Refresh Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	passwordHash, err := passwords.Hash("testpass")
	r.NoError(err)

	user := models.NewUser("refresh@example.com")
	user.Name = "Refresh User"
	user.AvatarURL = "https://example.com/refresh.jpg"
	user.PasswordHash = &passwordHash
	r.NoError(dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

	// Login to get a refresh token
	loginResp, err := svc.Login(ctx, "refresh-org", "refresh@example.com", "testpass", Context{})
	r.NoError(err)
	r.NotEmpty(loginResp.RefreshToken)

	// Refresh
	refreshResp, err := svc.Refresh(ctx, loginResp.RefreshToken)
	r.NoError(err)
	r.Equal("Refresh User", refreshResp.User.Name)
	r.Equal("https://example.com/refresh.jpg", refreshResp.User.AvatarURL)
	r.Equal("Refresh Org", refreshResp.Organization.Name)
}

// passwordResetUser is a small helper that wires up an org + a user with a
// real password hash, so reset tests can exercise the happy path without
// duplicating a dozen lines per case.
func passwordResetUser(t *testing.T, ctx context.Context, dbSvc db.Service, email string) *models.User {
	t.Helper()
	r := require.New(t)

	hash, err := passwords.Hash("oldpassword")
	r.NoError(err)

	user := models.NewUser(email)
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))

	return user
}

func extractResetTokenFromState(t *testing.T, ctx context.Context, dbSvc db.Service) string {
	t.Helper()
	r := require.New(t)

	entries, err := dbSvc.ListStateEntries(ctx, nil, "password_reset:")
	r.NoError(err)

	for _, e := range entries {
		if !strings.HasPrefix(e.Key, "password_reset:") {
			continue
		}
		// Skip the per-user counter; it has its own prefix.
		if strings.HasPrefix(e.Key, passwordResetCountKeyPrefix) {
			continue
		}

		return strings.TrimPrefix(e.Key, "password_reset:")
	}

	return ""
}

func TestRequestPasswordReset(t *testing.T) {
	t.Parallel()

	t.Run("creates hashed entry and counts toward per-user cap", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		passwordResetUser(t, ctx, dbSvc, "reset1@example.com")

		_, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: "reset1@example.com"}, "127.0.0.1")
		r.NoError(err)

		entries, err := dbSvc.ListStateEntries(ctx, nil, "password_reset:")
		r.NoError(err)
		// Exactly one reset entry; key must be a 64-char hex hash, not the email.
		var resetEntries []*models.StateEntry
		for _, e := range entries {
			if !strings.HasPrefix(e.Key, passwordResetCountKeyPrefix) {
				resetEntries = append(resetEntries, e)
			}
		}
		r.Len(resetEntries, 1)

		hashSuffix := strings.TrimPrefix(resetEntries[0].Key, "password_reset:")
		r.Len(hashSuffix, 64, "key suffix should be sha256 hex")
		// Regression guard: no plaintext token, no email anywhere in the value.
		val := *resetEntries[0].Value
		r.NotContains(val, "token")
		r.NotContains(val, "email")
		userUID, ok := val["userUid"].(string)
		r.True(ok)
		r.NotEmpty(userUID)
	})

	t.Run("OAuth-only user produces no entry and no email", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		// Create a user without a password hash.
		user := models.NewUser("oauth@example.com")
		r.NoError(dbSvc.CreateUser(ctx, user))

		resp, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: "oauth@example.com"}, "127.0.0.1")
		r.NoError(err)
		r.NotEmpty(resp.Message)

		entries, err := dbSvc.ListStateEntries(ctx, nil, "password_reset:")
		r.NoError(err)
		r.Empty(entries, "no entry should exist for OAuth-only user")
	})

	t.Run("unknown email returns success but creates nothing", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")

		resp, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: "ghost@example.com"}, "127.0.0.1")
		r.NoError(err)
		r.NotEmpty(resp.Message)

		entries, err := dbSvc.ListStateEntries(ctx, nil, "password_reset:")
		r.NoError(err)
		r.Empty(entries)
	})

	t.Run("per-user cap drops the 4th request silently", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		passwordResetUser(t, ctx, dbSvc, "cap@example.com")

		// Distinct IP per call so the per-IP limit doesn't kick in first.
		for i := 0; i < passwordResetMaxPerUser; i++ {
			ip := fmt.Sprintf("192.0.2.%d", i+1)
			_, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: "cap@example.com"}, ip)
			r.NoError(err)
		}

		// Sanity: cap entries created (we may have one per request).
		var beforeReset []*models.StateEntry
		entries, err := dbSvc.ListStateEntries(ctx, nil, "password_reset:")
		r.NoError(err)
		for _, e := range entries {
			if !strings.HasPrefix(e.Key, passwordResetCountKeyPrefix) {
				beforeReset = append(beforeReset, e)
			}
		}
		r.Len(beforeReset, passwordResetMaxPerUser)

		// 4th request is silently dropped.
		_, err = svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: "cap@example.com"}, "192.0.2.99")
		r.NoError(err)

		var afterReset []*models.StateEntry
		entries, err = dbSvc.ListStateEntries(ctx, nil, "password_reset:")
		r.NoError(err)
		for _, e := range entries {
			if !strings.HasPrefix(e.Key, passwordResetCountKeyPrefix) {
				afterReset = append(afterReset, e)
			}
		}
		r.Len(afterReset, passwordResetMaxPerUser, "no new entry beyond the cap")
	})

	t.Run("per-IP rate limit returns ErrRateLimited beyond the cap", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		passwordResetUser(t, ctx, dbSvc, "iprl@example.com")

		// Up to the cap, all calls succeed.
		for i := 0; i < passwordResetMaxPerIP; i++ {
			_, err := svc.RequestPasswordReset(ctx,
				RequestPasswordResetRequest{Email: "iprl@example.com"}, "203.0.113.1")
			r.NoError(err)
		}

		// One past the cap → ErrRateLimited.
		_, err := svc.RequestPasswordReset(ctx,
			RequestPasswordResetRequest{Email: "iprl@example.com"}, "203.0.113.1")
		r.ErrorIs(err, ErrRateLimited)
	})
}

func TestResetPassword(t *testing.T) {
	t.Parallel()

	t.Run("happy path: updates password, deletes entry, revokes refresh tokens, preserves PATs", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "happy@example.com")

		// Seed a refresh token + a PAT for this user.
		refresh := &models.UserToken{
			UID:     uuidV7(t),
			UserUID: user.UID,
			Type:    models.TokenTypeRefresh,
			Token:   "r-" + user.UID,
		}
		r.NoError(dbSvc.CreateUserToken(ctx, refresh))
		pat := &models.UserToken{
			UID:     uuidV7(t),
			UserUID: user.UID,
			Type:    models.TokenTypePAT,
			Token:   "p-" + user.UID,
		}
		r.NoError(dbSvc.CreateUserToken(ctx, pat))

		_, err := svc.RequestPasswordReset(ctx,
			RequestPasswordResetRequest{Email: "happy@example.com"}, "127.0.0.1")
		r.NoError(err)
		token := extractResetTokenFromState(t, ctx, dbSvc)
		r.NotEmpty(token, "RequestPasswordReset must produce a state entry")

		// We don't get the plaintext token back through the API, but we can
		// reconstruct it: the entry's key suffix is sha256(token). For tests
		// we can't reverse the hash, so we exercise the happy path by also
		// directly writing a new entry under a known token and using that.
		knownToken := "test-known-token"
		stateValue := &models.JSONMap{"userUid": user.UID}
		ttl := passwordResetTTL
		r.NoError(dbSvc.SetStateEntry(ctx, nil,
			passwordResetKeyPrefix+hashResetToken(knownToken), stateValue, &ttl))

		resp, err := svc.ResetPassword(ctx, ResetPasswordRequest{
			Token:    knownToken,
			Password: "newpassword",
		})
		r.NoError(err)
		r.NotEmpty(resp.Message)

		// Password updated.
		updated, err := dbSvc.GetUser(ctx, user.UID)
		r.NoError(err)
		r.NotNil(updated.PasswordHash)
		r.NotEqual("", *updated.PasswordHash)
		// Old password no longer matches.
		r.False(passwords.Verify("oldpassword", *updated.PasswordHash))
		r.True(passwords.Verify("newpassword", *updated.PasswordHash))

		// State entry for the used token is gone.
		entry, err := dbSvc.GetStateEntry(ctx, nil,
			passwordResetKeyPrefix+hashResetToken(knownToken))
		r.NoError(err)
		r.Nil(entry)

		// Refresh tokens revoked.
		refreshes, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
		r.NoError(err)
		r.Empty(refreshes)

		// PAT preserved.
		pats, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypePAT)
		r.NoError(err)
		r.Len(pats, 1)
	})

	t.Run("malformed or unknown token returns ErrPasswordResetExpired", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, _, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")

		_, err := svc.ResetPassword(ctx, ResetPasswordRequest{Token: "nope", Password: "newpassword"})
		r.ErrorIs(err, ErrPasswordResetExpired)
	})

	t.Run("already-used token returns ErrPasswordResetExpired", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "used@example.com")

		token := "token-used-once"
		stateValue := &models.JSONMap{"userUid": user.UID}
		ttl := passwordResetTTL
		r.NoError(dbSvc.SetStateEntry(ctx, nil,
			passwordResetKeyPrefix+hashResetToken(token), stateValue, &ttl))

		_, err := svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "newpassword"})
		r.NoError(err)

		// Second use must fail.
		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "anothernew"})
		r.ErrorIs(err, ErrPasswordResetExpired)
	})

	t.Run("short password is rejected before any DB mutation", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "short@example.com")
		originalHash := *user.PasswordHash

		token := "token-short-pw"
		stateValue := &models.JSONMap{"userUid": user.UID}
		ttl := passwordResetTTL
		r.NoError(dbSvc.SetStateEntry(ctx, nil,
			passwordResetKeyPrefix+hashResetToken(token), stateValue, &ttl))

		_, err := svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "short"})
		r.ErrorIs(err, ErrInvalidCredentials)

		// Password unchanged.
		refreshed, err := dbSvc.GetUser(ctx, user.UID)
		r.NoError(err)
		r.Equal(originalHash, *refreshed.PasswordHash)

		// State entry still present (the user hasn't burned their reset).
		entry, err := dbSvc.GetStateEntry(ctx, nil,
			passwordResetKeyPrefix+hashResetToken(token))
		r.NoError(err)
		r.NotNil(entry)
	})
}

// uuidV7 returns a UUIDv7 string for tests that need to seed unique IDs.
func uuidV7(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)

	return id.String()
}

// TestStateEntryUpsertWithNullOrg pins the upsert semantics SetStateEntry
// owes its callers: writing the same key twice with a NULL orgUID must
// leave exactly one row. Both SQLite and Postgres treat NULL as distinct
// in UNIQUE constraints by default, so the implementation runs an
// UPDATE-first / INSERT-fallback pattern; this test would catch a
// regression that lets duplicate rows accumulate and rendered counters
// or one-shot tokens silently wrong.
func TestStateEntryUpsertWithNullOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	_, dbSvc, ctx := setupAuthTestService(t)

	value1 := &models.JSONMap{"count": 1}
	value2 := &models.JSONMap{"count": 2}
	ttl := time.Hour

	r.NoError(dbSvc.SetStateEntry(ctx, nil, "test:upsert", value1, &ttl))
	r.NoError(dbSvc.SetStateEntry(ctx, nil, "test:upsert", value2, &ttl))

	entries, err := dbSvc.ListStateEntries(ctx, nil, "test:upsert")
	r.NoError(err)
	r.Len(entries, 1, "second SetStateEntry must replace the first, not duplicate it")

	got, err := dbSvc.GetStateEntry(ctx, nil, "test:upsert")
	r.NoError(err)
	r.NotNil(got)
	r.NotNil(got.Value)
	r.InEpsilon(2.0, (*got.Value)["count"], 0.0001)
}
