package auth

import (
	"context"
	"testing"
	"time"

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

	svc := NewService(dbService, cfg, nil, nil, nil)

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

	svc := NewService(dbService, fullCfg.Auth, fullCfg, nil, nil)

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
