package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// setupDiscordService builds a Service over an in-memory SQLite database, with
// a fake Discord REST API behind it so no test ever touches the network.
func setupDiscordService(t *testing.T) (context.Context, *Service, *fakeDiscord) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	fake := newFakeDiscord(t)

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		Discord: config.DiscordOAuthConfig{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			// Obviously-fake token: never a real credential in a fixture.
			BotToken: "test-bot-token",
		},
	}

	svc := NewService(dbService, cfg, nil, nil)
	svc.oauthTokenURL = fake.server.URL + "/oauth2/token"
	svc.apiBaseURL = fake.server.URL
	svc.newBotClient = func(token string) *BotClient {
		return NewBotClient(token).WithBaseURL(fake.server.URL)
	}

	return ctx, svc, fake
}

// postedMessage is one message the fake Discord received.
type postedMessage struct {
	ChannelID string
	Body      map[string]any
}

// fakeDiscord is a stand-in for Discord's REST + OAuth endpoints.
type fakeDiscord struct {
	server *httptest.Server

	mu     sync.Mutex
	posted []postedMessage

	// guild is what the token exchange reports the bot was added to.
	guild Guild
	// channels is what ListGuildChannels returns.
	channels []ChannelInfo
	// installerID is what /users/@me returns for a user token.
	installerID string
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	t.Helper()

	fake := &fakeDiscord{
		guild:       Guild{ID: "G-ACME", Name: "acme"},
		installerID: "U-INSTALLER",
		channels: []ChannelInfo{
			{ID: "C-ALERTS", Name: "alerts", Type: ChannelTypeGuildText},
			{ID: "C-VOICE", Name: "lounge", Type: ChannelTypeGuildVoice},
			{ID: "C-GENERAL", Name: "General", Type: ChannelTypeGuildText},
		},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "fake-user-access-token",
			TokenType:   "Bearer",
			Scope:       botInstallScopes,
			Guild:       &fake.guild,
		})
	})

	mux.HandleFunc("/users/@me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(User{ID: fake.installerID, Username: "alice", Bot: false})
	})

	mux.HandleFunc("/channels/", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}

		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}

		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		channelID := ""

		if len(parts) > 1 {
			channelID = parts[1]
		}

		fake.mu.Lock()
		fake.posted = append(fake.posted, postedMessage{ChannelID: channelID, Body: body})
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MessageResult{ID: "M-POSTED", ChannelID: channelID})
	})

	mux.HandleFunc("/guilds/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/channels") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fake.channels)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fake.guild)
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

// messages returns the messages posted to the fake so far.
func (f *fakeDiscord) messages() []postedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]postedMessage, len(f.posted))
	copy(out, f.posted)

	return out
}

// installState mints a valid install state the way BuildInstallURL does, and
// returns the nonce so a test can drive HandleOAuthCallback end to end.
func installState(t *testing.T, ctx context.Context, svc *Service, orgSlug string) string {
	t.Helper()

	authorizeURL, err := svc.BuildInstallURL(ctx, "", orgSlug)
	require.NoError(t, err)

	parsed, err := url.Parse(authorizeURL)
	require.NoError(t, err)

	state := parsed.Query().Get("state")
	require.NotEmpty(t, state)

	return state
}

func TestBuildInstallURL_RequestsBotScopesAndPermissions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupDiscordService(t)

	authorizeURL, err := svc.BuildInstallURL(ctx, "", "acme")
	r.NoError(err)
	r.True(strings.HasPrefix(authorizeURL, OAuthAuthorizeURL+"?"))

	parsed, err := url.Parse(authorizeURL)
	r.NoError(err)

	q := parsed.Query()
	r.Equal("test-client-id", q.Get("client_id"))
	r.Equal("code", q.Get("response_type"))
	r.Contains(q.Get("scope"), "bot")
	r.Contains(q.Get("scope"), "applications.commands")
	r.NotEmpty(q.Get("permissions"))
	r.NotEmpty(q.Get("state"))
	r.Equal("http://localhost:4000/api/v1/integrations/discord/oauth", q.Get("redirect_uri"))
}

// TestHandleOAuthCallback_ReusesAuthGuildMapping is resolved question 2's
// acceptance test: installing the bot into a guild that "Sign in with Discord"
// already mapped must reuse that ONE organization_providers row — never add a
// second guild→org mapping, which would let auth and reporting disagree about
// which org a guild is.
func TestHandleOAuthCallback_ReusesAuthGuildMapping(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	// Exactly what auth.DiscordOAuthService.findOrCreateOrganization writes.
	org := models.NewOrganization("acme", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	authMapping := models.NewOrganizationProvider(org.UID, models.ProviderTypeDiscord, fake.guild.ID)
	authMapping.ProviderName = fake.guild.Name
	r.NoError(svc.db.CreateOrganizationProvider(ctx, authMapping))

	state := installState(t, ctx, svc, org.Slug)

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", state)
	r.NoError(err)
	r.Equal(org.Slug, result.OrgSlug)
	r.Equal(fake.guild.ID, result.GuildID)

	// The mapping is still exactly one row, and still the original one.
	providers, err := svc.db.ListOrganizationProviders(ctx, org.UID)
	r.NoError(err)

	discordProviders := make([]*models.OrganizationProvider, 0, len(providers))

	for _, p := range providers {
		if p.ProviderType == models.ProviderTypeDiscord {
			discordProviders = append(discordProviders, p)
		}
	}

	r.Len(discordProviders, 1, "bot install must not create a second guild→org mapping")
	r.Equal(authMapping.UID, discordProviders[0].UID)

	// And the lookup both auth and reporting use still resolves to this org.
	found, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(err)
	r.Equal(org.UID, found.OrganizationUID)
}

// TestHandleOAuthCallback_CreatesMappingWhenNoneExists covers the other half:
// a guild nobody has signed in from yet gets its mapping written by the install.
func TestHandleOAuthCallback_CreatesMappingWhenNoneExists(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	org := models.NewOrganization("acme-two", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	state := installState(t, ctx, svc, org.Slug)

	_, err := svc.HandleOAuthCallback(ctx, "fake-code", state)
	r.NoError(err)

	found, err := svc.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, fake.guild.ID)
	r.NoError(err)
	r.Equal(org.UID, found.OrganizationUID)
	r.Equal(fake.guild.Name, found.ProviderName)
}

// TestHandleOAuthCallback_WritesBotSettings pins the settings blob the install
// produces: bot fields set, mentions on, explicit comment ingestion.
func TestHandleOAuthCallback_WritesBotSettings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	org := models.NewOrganization("acme-three", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(t, ctx, svc, org.Slug))
	r.NoError(err)

	conn, err := svc.db.GetChannel(ctx, result.ConnectionUID)
	r.NoError(err)
	r.Equal(models.ConnectionTypeDiscord, conn.Type)

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal(fake.guild.ID, settings.GuildID)
	r.Equal(fake.guild.Name, settings.GuildName)
	r.Equal(fake.installerID, settings.InstalledByUserID)
	r.True(settings.MentionOnCall)
	r.Equal(models.DiscordCommentIngestionExplicit, settings.CommentIngestion)
	r.Empty(settings.WebhookURL)
	// No destination is chosen yet — the operator picks one in the dashboard.
	r.False(settings.UsesBot())
}

// TestHandleOAuthCallback_ReinstallKeepsOperatorChoices proves a reconnect does
// not silently flip a setting the operator decided, and — critically for the
// legacy path — does not throw away a webhook URL that is still in use.
func TestHandleOAuthCallback_ReinstallKeepsOperatorChoices(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	org := models.NewOrganization("acme-four", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	first, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(t, ctx, svc, org.Slug))
	r.NoError(err)

	// Operator makes their choices.
	stored := &models.DiscordSettings{
		GuildID:          fake.guild.ID,
		GuildName:        fake.guild.Name,
		ChannelID:        "C-ALERTS",
		ChannelName:      "alerts",
		MentionOnCall:    false,
		CommentIngestion: models.DiscordCommentIngestionAll,
		WebhookURL:       "https://discord.com/api/webhooks/1/acme-legacy",
	}
	settingsMap, err := stored.ToJSONMap()
	r.NoError(err)
	r.NoError(svc.db.UpdateChannel(ctx, first.ConnectionUID, &models.IntegrationUpdate{Settings: &settingsMap}))

	// Re-install.
	second, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(t, ctx, svc, org.Slug))
	r.NoError(err)
	r.Equal(first.ConnectionUID, second.ConnectionUID, "re-install must update, not duplicate")

	conn, err := svc.db.GetChannel(ctx, second.ConnectionUID)
	r.NoError(err)

	after, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.False(after.MentionOnCall)
	r.Equal(models.DiscordCommentIngestionAll, after.CommentIngestion)
	r.Equal("C-ALERTS", after.ChannelID)
	r.Equal("https://discord.com/api/webhooks/1/acme-legacy", after.WebhookURL)
}

// TestHandleOAuthCallback_RejectsReusedState pins the CSRF property.
func TestHandleOAuthCallback_RejectsReusedState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupDiscordService(t)

	org := models.NewOrganization("acme-five", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	state := installState(t, ctx, svc, org.Slug)

	_, err := svc.HandleOAuthCallback(ctx, "fake-code", state)
	r.NoError(err)

	_, err = svc.HandleOAuthCallback(ctx, "fake-code", state)
	r.ErrorIs(err, ErrInvalidState)
}

// TestGetDestinations_FiltersAndSorts pins the picker contract: only postable
// text channels, sorted case-insensitively by name.
func TestGetDestinations_FiltersAndSorts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupDiscordService(t)

	org := models.NewOrganization("acme-six", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(t, ctx, svc, org.Slug))
	r.NoError(err)

	resp, err := svc.GetDestinations(ctx, org.Slug, result.ConnectionUID)
	r.NoError(err)
	r.True(resp.Connected)
	r.Len(resp.Channels, 2, "the voice channel must not be offered as a destination")
	r.Equal("alerts", resp.Channels[0].Name)
	r.Equal("General", resp.Channels[1].Name)
}

// TestGetDestinations_LegacyWebhookIsNotConnected: a webhook-only integration
// has no guild, so the picker must say "not connected" rather than 502 out of
// a Discord API call it cannot make.
func TestGetDestinations_LegacyWebhookIsNotConnected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupDiscordService(t)

	org := models.NewOrganization("acme-legacy", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	legacy := &models.DiscordSettings{WebhookURL: "https://discord.com/api/webhooks/1/acme-legacy"}
	settingsMap, err := legacy.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeDiscord, "acme legacy")
	conn.Settings = settingsMap
	r.NoError(svc.db.CreateChannel(ctx, conn))

	_, err = svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.ErrorIs(err, ErrDiscordNotConnected)
}

// TestGetDestinations_RejectsForeignOrg proves the org scoping: an integration
// belonging to another org is a 404-shaped "not found", not somebody else's
// channel list.
func TestGetDestinations_RejectsForeignOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupDiscordService(t)

	owner := models.NewOrganization("acme-owner", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, owner))

	other := models.NewOrganization("acme-other", "acme other")
	r.NoError(svc.db.CreateOrganization(ctx, other))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(t, ctx, svc, owner.Slug))
	r.NoError(err)

	_, err = svc.GetDestinations(ctx, other.Slug, result.ConnectionUID)
	r.ErrorIs(err, ErrConnectionNotFound)
}

// TestSetDefaultChannel_RejectsForeignChannelID is the asserted-vs-proven
// identifier gate: a channel id that is not one of the guild's own channels
// must be refused, even though it is a perfectly well-formed snowflake.
func TestSetDefaultChannel_RejectsForeignChannelID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, fake := setupDiscordService(t)

	org := models.NewOrganization("acme-seven", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	_, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(t, ctx, svc, org.Slug))
	r.NoError(err)

	r.ErrorIs(svc.SetDefaultChannel(ctx, fake.guild.ID, "C-SOMEONE-ELSES", false), ErrConnectionNotFound)

	// A real one is accepted and recorded with its name.
	r.NoError(svc.SetDefaultChannel(ctx, fake.guild.ID, "C-ALERTS", false))

	conn, err := svc.GetConnectionByGuildID(ctx, fake.guild.ID)
	r.NoError(err)

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("C-ALERTS", settings.ChannelID)
	r.Equal("alerts", settings.ChannelName)
	r.True(settings.UsesBot())
}
