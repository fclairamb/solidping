package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupSlackTestService(t *testing.T) (*SlackOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		Slack: config.SlackConfig{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}

	authService := NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewSlackOAuthService(dbService, cfg, authService)

	return svc, ctx
}

// TestFindOrCreateOrganizationSlugFromWorkspace verifies that the org slug is
// derived from the Slack workspace identity in priority order: team_domain,
// then team_name, then the OAuth Team.Name, then "org".
func TestFindOrCreateOrganizationSlugFromWorkspace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		teamID      string
		orgName     string
		candidates  []string
		wantSlug    string
		wantOrgName string
	}{
		{
			name:        "team_domain wins",
			teamID:      "T1",
			orgName:     "Acme Corp",
			candidates:  []string{"acme", "Acme Corp", ""},
			wantSlug:    "acme",
			wantOrgName: "Acme Corp",
		},
		{
			name:        "empty domain falls back to team_name",
			teamID:      "T2",
			orgName:     "Acme Corp",
			candidates:  []string{"", "Acme Corp", ""},
			wantSlug:    "acme-corp",
			wantOrgName: "Acme Corp",
		},
		{
			name:        "all empty falls back to org",
			teamID:      "T3",
			orgName:     "",
			candidates:  []string{"", "", ""},
			wantSlug:    "org",
			wantOrgName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, ctx := setupSlackTestService(t)

			org, err := svc.findOrCreateOrganization(ctx, tc.teamID, tc.orgName, tc.candidates...)
			r.NoError(err)
			r.NotNil(org)
			r.Equal(tc.wantSlug, org.Slug)
			r.Equal(tc.wantOrgName, org.Name)

			// Calling again with the same team ID returns the same org (idempotent).
			again, err := svc.findOrCreateOrganization(ctx, tc.teamID, tc.orgName, tc.candidates...)
			r.NoError(err)
			r.Equal(org.UID, again.UID)
		})
	}
}

// TestFindOrCreateOrganizationSlugCollision verifies the shared collision loop
// is wired in: a second distinct workspace reusing a taken domain gets a
// numeric suffix rather than reusing the slug.
func TestFindOrCreateOrganizationSlugCollision(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, ctx := setupSlackTestService(t)

	first, err := svc.findOrCreateOrganization(ctx, "TEAM-A", "Acme", "acme")
	r.NoError(err)
	r.Equal("acme", first.Slug)

	second, err := svc.findOrCreateOrganization(ctx, "TEAM-B", "Acme", "acme")
	r.NoError(err)
	r.Equal("acme2", second.Slug)
}
