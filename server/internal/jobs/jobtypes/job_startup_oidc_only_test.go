package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func oidcOnlyConfig(localLogin bool, superAdminGroups string) *config.Config {
	return &config.Config{
		Auth: config.AuthConfig{LocalLoginEnabled: localLogin},
		OIDC: config.OIDCOAuthConfig{
			Enabled: true, IssuerURL: "https://idp.acme.com", ClientID: "id", ClientSecret: "secret",
			SuperAdminGroups: superAdminGroups,
		},
	}
}

// OIDC-only first boot (spec 2026-11-21-02): the default org is created
// empty and no password super admin is seeded. Any other configuration keeps
// today's seeding.
func TestStartup_OIDCOnlyFirstBoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       *config.Config
		wantAdmin bool
	}{
		{"oidc-only", oidcOnlyConfig(false, "sp-admins"), false},
		{"local login on", oidcOnlyConfig(true, "sp-admins"), true},
		{"no super-admin group", oidcOnlyConfig(false, ""), true},
		{"oidc not configured fails open", &config.Config{
			OIDC: config.OIDCOAuthConfig{SuperAdminGroups: "sp-admins"},
		}, true},
		{"no config", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbSvc, jctx := startupWorld(t)
			jctx.AppConfig = tt.cfg
			require.NoError(t, (&StartupJobRun{}).Run(t.Context(), jctx))

			require.Equal(t, 1, countRows(t, dbSvc, "organizations"), "the default org always exists")

			users, err := dbSvc.ListUsers(t.Context())
			require.NoError(t, err)

			if !tt.wantAdmin {
				require.Empty(t, users, "no password admin in OIDC-only mode")
				require.Equal(t, 0, countRows(t, dbSvc, "organization_members"))

				return
			}

			require.Len(t, users, 1)
			require.True(t, users[0].SuperAdmin)
			require.NotNil(t, users[0].PasswordHash)

			members, err := dbSvc.ListMembersByUser(t.Context(), users[0].UID)
			require.NoError(t, err)
			require.Len(t, members, 1)
			require.Equal(t, models.MemberRoleOwner, members[0].Role)
		})
	}
}

// Break-glass (spec 2026-11-21-02): an OIDC-only instance restarted with local
// login on and SP_ADMIN_PASSWORD set gets a super admin to rotate. Not with
// local login still off, and never on an instance that seeded its admin.
func TestStartup_OIDCOnlyBreakGlass(t *testing.T) {
	t.Parallel()

	dbSvc, jctx := startupWorld(t)
	jctx.AppConfig = oidcOnlyConfig(false, "sp-admins")
	require.NoError(t, (&StartupJobRun{}).Run(t.Context(), jctx))

	// Still OIDC-only: SP_ADMIN_PASSWORD alone does nothing.
	jctx.AppConfig.Auth.AdminPassword = "break-glass-pass-1"
	require.NoError(t, (&StartupJobRun{}).Run(t.Context(), jctx))

	users, err := dbSvc.ListUsers(t.Context())
	require.NoError(t, err)
	require.Empty(t, users)

	// Local login back on: the break-glass admin is created.
	jctx.AppConfig.Auth.LocalLoginEnabled = true
	require.NoError(t, (&StartupJobRun{}).Run(t.Context(), jctx))

	users, err = dbSvc.ListUsers(t.Context())
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.True(t, users[0].SuperAdmin)
	require.True(t, users[0].MustChangePassword)

	// A normal instance whose admin changed its email never gets one.
	normalDB, normalCtx := startupWorld(t)
	normalCtx.AppConfig = oidcOnlyConfig(true, "")
	require.NoError(t, (&StartupJobRun{}).Run(t.Context(), normalCtx))

	seeded, err := normalDB.ListUsers(t.Context())
	require.NoError(t, err)
	require.Len(t, seeded, 1)

	renamed := "renamed@acme.com"
	require.NoError(t, normalDB.UpdateUser(t.Context(), seeded[0].UID, &models.UserUpdate{Email: &renamed}))

	normalCtx.AppConfig.Auth.AdminPassword = "break-glass-pass-1"
	require.NoError(t, (&StartupJobRun{}).Run(t.Context(), normalCtx))

	seeded, err = normalDB.ListUsers(t.Context())
	require.NoError(t, err)
	require.Len(t, seeded, 1)
}
