package opsnotifywire_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
)

// discordBotConfig is a fully configured instance Discord bot. BotConfigured()
// needs all four values, including the public key: a DM carries the incident
// action row, so an instance whose interactions cannot be verified is not a
// working setup.
func discordBotConfig() *config.Config {
	return &config.Config{Discord: config.DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		BotToken:     "bot-token",
		PublicKey:    "abcd",
	}}
}

// fakeDiscordDM stands in for Discord's REST API on the two calls a DM needs.
type fakeDiscordDM struct {
	server *httptest.Server
	// postStatus / postBody are what POST /channels/{id}/messages answers.
	postStatus int
	postBody   string
	// dmOpened counts POST /users/@me/channels calls, so the caching claim can
	// be checked rather than assumed.
	dmOpened int
	posted   int
}

func newFakeDiscordDM(t *testing.T) *fakeDiscordDM {
	t.Helper()

	fake := &fakeDiscordDM{postStatus: http.StatusOK, postBody: `{"id":"M-1","channel_id":"DM-1"}`}

	mux := http.NewServeMux()
	mux.HandleFunc("/users/@me/channels", func(w http.ResponseWriter, _ *http.Request) {
		fake.dmOpened++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"DM-1","type":1}`))
	})
	mux.HandleFunc("/channels/", func(w http.ResponseWriter, _ *http.Request) {
		fake.posted++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fake.postStatus)
		_, _ = w.Write([]byte(fake.postBody))
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeDiscordDM) install(t *testing.T) {
	t.Helper()

	t.Cleanup(opsnotifywire.SetDiscordBotClientFactory(func(token string) *discord.BotClient {
		return discord.NewBotClient(token).WithBaseURL(f.server.URL)
	}))
}

// discordEnv is a real sqlite database with one org, one member, and a verified
// discord contact with no cached DM channel.
type discordEnv struct {
	db      db.Service
	contact *models.UserContact
}

func newDiscordEnv(t *testing.T) *discordEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "ACME")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	contact := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeDiscord, "SNOWFLAKE", "Discord")
	r.NoError(dbSvc.UpsertUserContact(ctx, contact))
	r.NoError(dbSvc.MarkUserContactVerified(ctx, contact.UID, time.Now()))

	return &discordEnv{db: dbSvc, contact: contact}
}

// TestSendDiscordDMOpensCachesAndPosts: the happy path also proves the cache —
// two sends must open the DM exactly ONCE, which is the entire reason the
// dm_channel_id column exists.
func TestSendDiscordDMOpensCachesAndPosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	env := newDiscordEnv(t)

	fake := newFakeDiscordDM(t)
	fake.install(t)

	deps := opsnotifywire.Build(env.db, nil, discordBotConfig())
	r.NotNil(deps.SendDiscordDM, "a configured bot must wire the Discord medium")

	contact := env.contact

	r.NoError(deps.SendDiscordDM(ctx, contact, "first"))
	r.NoError(deps.SendDiscordDM(ctx, contact, "second"))

	r.Equal(1, fake.dmOpened, "the DM channel must be opened once and then reused")
	r.Equal(2, fake.posted)

	// The cache is persisted, not just held in memory: a fresh read of the row
	// must carry it, or the next process pays for the open again.
	reloaded, err := env.db.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(reloaded.DMChannelID)
	r.Equal("DM-1", *reloaded.DMChannelID)
}

// TestSendDiscordDM50007IsUnavailableAnd5xxIsNot is the security/correctness pair
// the paging-coverage fallthrough depends on, asserted over a real HTTP response
// body rather than a stubbed error.
//
//   - Discord error code 50007 ("Cannot send messages to this user") is NOT a
//     fault — DMs are off, the bot is blocked, or the member shares no server —
//     so it must wrap ErrMediumUnavailable and let paging fall through to the
//     member's next route;
//   - the positive control: a plain 500 with no error envelope must NOT be
//     unavailable, or a genuine Discord outage would be silently reclassified as
//     "this instance cannot do Discord" and nobody would be told.
func TestSendDiscordDM50007IsUnavailableAnd5xxIsNot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		status      int
		body        string
		unavailable bool
	}{
		{
			name:        "50007 recipient refusal is unavailable",
			status:      http.StatusForbidden,
			body:        `{"code":50007,"message":"Cannot send messages to this user"}`,
			unavailable: true,
		},
		{
			name:        "a plain 5xx is a failure, not an unavailability",
			status:      http.StatusInternalServerError,
			body:        `upstream exploded`,
			unavailable: false,
		},
		{
			name:   "a 403 that is NOT 50007 is a failure too",
			status: http.StatusForbidden,
			body:   `{"code":50013,"message":"Missing Permissions"}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()
			env := newDiscordEnv(t)

			fake := newFakeDiscordDM(t)
			fake.postStatus = testCase.status
			fake.postBody = testCase.body
			fake.install(t)

			deps := opsnotifywire.Build(env.db, nil, discordBotConfig())

			sendErr := deps.SendDiscordDM(ctx, env.contact, "page")
			r.Error(sendErr)
			r.Equal(testCase.unavailable, errorsIsMediumUnavailable(sendErr),
				"ErrMediumUnavailable classification for %q", testCase.name)
		})
	}
}

// TestSendDiscordDMUnconfiguredBotIsUnavailable: an instance with no Discord bot
// reports "unavailable", which is a skip, not a failure — the same rule every
// other unconfigured medium follows.
func TestSendDiscordDMUnconfiguredBotIsUnavailable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	env := newDiscordEnv(t)

	deps := opsnotifywire.Build(env.db, nil, &config.Config{})

	err := deps.SendDiscordDM(ctx, env.contact, "page")
	r.Error(err)
	r.True(errorsIsMediumUnavailable(err))
}

func errorsIsMediumUnavailable(err error) bool {
	return errors.Is(err, opsnotify.ErrMediumUnavailable)
}
