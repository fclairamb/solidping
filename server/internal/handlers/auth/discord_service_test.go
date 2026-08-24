package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// newDiscordTestService wires a Discord OAuth service on top of an already
// initialized database service, so the same test bodies can run against SQLite
// and Postgres (see provider_links_postgres_test.go).
func newDiscordTestService(t *testing.T, dbService db.Service) *DiscordOAuthService {
	t.Helper()

	cfg := &config.Config{
		Discord: config.DiscordOAuthConfig{
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

	return NewDiscordOAuthService(dbService, cfg, NewService(dbService, cfg.Auth, cfg, nil, nil))
}

// newSQLiteDBService is the default backend for the auth package's
// engine-agnostic contracts.
func newSQLiteDBService(t *testing.T) db.Service {
	t.Helper()

	dbService, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbService.Close() })

	return dbService
}

// fakeDiscordEndpoints points the service at an httptest stand-in for Discord's
// token, /users/@me and /users/@me/guilds endpoints, so the tests drive the REAL
// HandleCallback rather than a test-local re-implementation of it.
func fakeDiscordEndpoints(
	t *testing.T, svc *DiscordOAuthService, user DiscordUserInfo, guilds []DiscordGuild,
) {
	t.Helper()

	if guilds == nil {
		guilds = []DiscordGuild{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/token"):
			_ = json.NewEncoder(w).Encode(DiscordTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
		case strings.HasSuffix(r.URL.Path, "/users/@me/guilds"):
			_ = json.NewEncoder(w).Encode(guilds)
		case strings.HasSuffix(r.URL.Path, "/users/@me"):
			_ = json.NewEncoder(w).Encode(user)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	svc.tokenURL = server.URL + "/oauth2/token"
	svc.apiBaseURL = server.URL
	svc.httpClient = server.Client()
}

// fixtureSeq hands every contract body its own identifiers. The Postgres twin
// runs all of them against ONE embedded server (booting one per case would blow
// past its connection budget), so fixtures that reused a guild id, a Discord
// user id or an email would collide on the real unique indexes — including
// `user_providers_provider_idx`, which is exactly what these tests are about.
var fixtureSeq atomic.Int64 //nolint:gochecknoglobals // one counter shared by every contract body, across engines

// nextFixture returns a short token unique to one contract-body invocation.
func nextFixture() string {
	return strconv.FormatInt(fixtureSeq.Add(1), 10)
}

// discordTestUser is the identity a Discord contract signs in with, keyed by the
// caller's fixture token.
func discordTestUser(fixture string) DiscordUserInfo {
	return DiscordUserInfo{
		ID:         "du-" + fixture,
		Username:   "member" + fixture,
		GlobalName: "Acme Member",
		Email:      "member" + fixture + "@acme.example",
		Verified:   true,
	}
}

// TestDiscordStaleProviderLinks_SQLite is the reproduction of the production
// failure this spec fixes ("OAuth failed: failed to find/create organization:
// sql: no rows in result set", 2026-08-24): a Discord guild whose linked
// organization has been soft-deleted bricked every later login for that guild,
// because the still-live organization_providers row won the lookup and the org
// behind it no longer resolved.
func TestDiscordStaleProviderLinks_SQLite(t *testing.T) {
	t.Parallel()

	t.Run("stale guild link", func(t *testing.T) {
		t.Parallel()
		testDiscordStaleGuildLinkHeals(t, newSQLiteDBService(t))
	})

	t.Run("stale personal-org link", func(t *testing.T) {
		t.Parallel()
		testDiscordStalePersonalOrgLinkHeals(t, newSQLiteDBService(t))
	})

	t.Run("stale user link", func(t *testing.T) {
		t.Parallel()
		testDiscordStaleUserLinkHeals(t, newSQLiteDBService(t))
	})

	t.Run("live link is left alone", func(t *testing.T) {
		t.Parallel()
		testDiscordLiveLinkIsReused(t, newSQLiteDBService(t))
	})
}

// testDiscordStaleGuildLinkHeals drives the guild path: an org linked to guild
// G-1 is soft-deleted, then a member of G-1 signs in.
func testDiscordStaleGuildLinkHeals(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	svc := newDiscordTestService(t, dbService)

	fixture := nextFixture()
	guildID := "G-" + fixture

	dead := models.NewOrganization("acme-guild-"+fixture, "Acme Guild")
	r.NoError(dbService.CreateOrganization(ctx, dead))

	staleLink := models.NewOrganizationProvider(dead.UID, models.ProviderTypeDiscord, guildID)
	staleLink.ProviderName = "Acme Guild"
	r.NoError(dbService.CreateOrganizationProvider(ctx, staleLink))

	// The org goes away, the link does not: exactly the production state.
	r.NoError(dbService.DeleteOrganization(ctx, dead.UID))

	fakeDiscordEndpoints(t, svc, discordTestUser(fixture), []DiscordGuild{{ID: guildID, Name: "Acme Guild"}})

	result, err := svc.HandleCallback(ctx, "mock-code")
	r.NoError(err, "a soft-deleted linked org must not brick the login")
	r.NotEmpty(result.AccessToken)
	r.NotEmpty(result.OrgSlug)

	// A FRESH org, never the resurrected one.
	fresh, err := dbService.GetOrganizationBySlug(ctx, result.OrgSlug)
	r.NoError(err)
	r.NotEqual(dead.UID, fresh.UID, "the soft-deleted org must not be resurrected")

	// The stale link is cleared (soft-deleted) and re-pointed at the fresh org.
	cleared, err := dbService.GetOrganizationProvider(ctx, staleLink.UID)
	r.Error(err, "the stale organization_providers row must be soft-deleted")
	r.Nil(cleared)

	relinked, err := dbService.GetOrganizationProviderByProviderID(
		ctx, models.ProviderTypeDiscord, guildID)
	r.NoError(err)
	r.Equal(fresh.UID, relinked.OrganizationUID)
	r.NotEqual(staleLink.UID, relinked.UID)

	// And the heal is durable: signing in again reuses the fresh org rather
	// than creating a third one.
	again, err := svc.HandleCallback(ctx, "mock-code")
	r.NoError(err)
	r.Equal(result.OrgSlug, again.OrgSlug)
}

// testDiscordStalePersonalOrgLinkHeals drives the OTHER org path: a user with no
// guilds gets a personal org keyed "discord-user-<id>". That link goes stale the
// same way and must heal the same way.
func testDiscordStalePersonalOrgLinkHeals(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	svc := newDiscordTestService(t, dbService)

	fixture := nextFixture()
	user := discordTestUser(fixture)
	personalID := "discord-user-" + user.ID

	dead := models.NewOrganization("acme-member-"+fixture, user.DisplayName())
	r.NoError(dbService.CreateOrganization(ctx, dead))

	staleLink := models.NewOrganizationProvider(dead.UID, models.ProviderTypeDiscord, personalID)
	r.NoError(dbService.CreateOrganizationProvider(ctx, staleLink))
	r.NoError(dbService.DeleteOrganization(ctx, dead.UID))

	// No guilds at all → HandleCallback takes the personal-org branch.
	fakeDiscordEndpoints(t, svc, user, nil)

	result, err := svc.HandleCallback(ctx, "mock-code")
	r.NoError(err, "a stale personal-org link must not brick the login either")

	fresh, err := dbService.GetOrganizationBySlug(ctx, result.OrgSlug)
	r.NoError(err)
	r.NotEqual(dead.UID, fresh.UID)

	relinked, err := dbService.GetOrganizationProviderByProviderID(
		ctx, models.ProviderTypeDiscord, personalID)
	r.NoError(err)
	r.Equal(fresh.UID, relinked.OrganizationUID)
	r.NotEqual(staleLink.UID, relinked.UID)
}

// testDiscordStaleUserLinkHeals is the user_providers twin (spec requirement 2):
// the guild org is alive, but the user_providers row points at a soft-deleted
// user.
func testDiscordStaleUserLinkHeals(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	svc := newDiscordTestService(t, dbService)

	fixture := nextFixture()
	info := discordTestUser(fixture)

	dead := models.NewUser("stale" + fixture + "@acme.example")
	r.NoError(dbService.CreateUser(ctx, dead))

	staleLink := models.NewUserProvider(dead.UID, models.ProviderTypeDiscord, info.ID)
	r.NoError(dbService.CreateUserProvider(ctx, staleLink))
	r.NoError(dbService.DeleteUser(ctx, dead.UID))

	fakeDiscordEndpoints(t, svc, info, []DiscordGuild{{ID: "G-USER-" + fixture, Name: "Acme Guild"}})

	result, err := svc.HandleCallback(ctx, "mock-code")
	r.NoError(err, "a soft-deleted linked user must not brick the login")
	r.NotEqual(dead.UID, result.UserUID, "the soft-deleted user must not be resurrected")

	// The stale row is gone (user_providers is hard-deleted — no deleted_at
	// column, and its unique index is not partial) and re-created for the new
	// user.
	relinked, err := dbService.GetUserProviderByProviderID(ctx, models.ProviderTypeDiscord, info.ID)
	r.NoError(err)
	r.Equal(result.UserUID, relinked.UserUID)
	r.NotEqual(staleLink.UID, relinked.UID)

	_, err = dbService.GetUserProvider(ctx, staleLink.UID)
	r.Error(err, "the stale user_providers row must be gone")
}

// testDiscordLiveLinkIsReused is the positive control for all three: a link
// whose target is alive must be reused untouched. Without it, "the login
// succeeds" would also pass for an implementation that blindly clears every
// link and creates a new org on every sign-in.
func testDiscordLiveLinkIsReused(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	svc := newDiscordTestService(t, dbService)

	fixture := nextFixture()
	guildID := "G-LIVE-" + fixture

	org := models.NewOrganization("live-guild-"+fixture, "Live Guild")
	r.NoError(dbService.CreateOrganization(ctx, org))

	link := models.NewOrganizationProvider(org.UID, models.ProviderTypeDiscord, guildID)
	r.NoError(dbService.CreateOrganizationProvider(ctx, link))

	fakeDiscordEndpoints(t, svc, discordTestUser(fixture), []DiscordGuild{{ID: guildID, Name: "Live Guild"}})

	result, err := svc.HandleCallback(ctx, "mock-code")
	r.NoError(err)
	r.Equal(org.Slug, result.OrgSlug, "a live link must resolve to its own org")

	same, err := dbService.GetOrganizationProviderByProviderID(
		ctx, models.ProviderTypeDiscord, guildID)
	r.NoError(err)
	r.Equal(link.UID, same.UID, "a live link must not be cleared and re-created")

	stillThere, err := dbService.GetOrganizationProvider(ctx, link.UID)
	r.NoError(err)
	r.Nil(stillThere.DeletedAt)
}
