package jobtypes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
)

const testDiscordSnowflake = "111222333444555666"

// fakeDiscordBot stands in for Discord's REST API on the two DM routes, wired in
// through the package's client seam per test.
type fakeDiscordBot struct {
	mu sync.Mutex
	// calls records "createDM" / "createMessage" in order.
	calls []string
	// bodies keeps the last body per call name.
	bodies map[string]map[string]any
	// postStatus / postBody answer POST /channels/{id}/messages.
	postStatus int
	postBody   string
	// factory builds a bot client pointed at this fake.
	factory func(token string) *discord.BotClient
}

func newFakeDiscordBot(t *testing.T) *fakeDiscordBot {
	t.Helper()

	fake := &fakeDiscordBot{
		bodies:     map[string]map[string]any{},
		postStatus: http.StatusOK,
		postBody:   `{"id":"M-DM","channel_id":"DM-1"}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/users/@me/channels", func(w http.ResponseWriter, req *http.Request) {
		fake.record("createDM", req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"DM-1","type":1}`))
	})
	mux.HandleFunc("/channels/", func(w http.ResponseWriter, req *http.Request) {
		fake.record("createMessage", req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fake.postStatus)
		_, _ = w.Write([]byte(fake.postBody))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	fake.factory = func(token string) *discord.BotClient {
		return discord.NewBotClient(token).WithBaseURL(server.URL)
	}

	return fake
}

// run returns a fresh escalation run wired to this fake.
func (f *fakeDiscordBot) run() *EscalationStepJobRun {
	r := newRun()
	r.discordClientFactory = f.factory

	return r
}

func (f *fakeDiscordBot) record(name string, req *http.Request) {
	body := map[string]any{}

	if raw, _ := io.ReadAll(req.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, name)
	f.bodies[name] = body
}

func (f *fakeDiscordBot) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0

	for _, call := range f.calls {
		if call == name {
			n++
		}
	}

	return n
}

func (f *fakeDiscordBot) body(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.bodies[name]
}

// enableDiscordBot points the instance config at a fully configured bot.
// BotConfigured() needs the public key too: the DM carries the action row, and an
// instance that cannot verify interactions would DM a dead Acknowledge button.
func enableDiscordBot(env *phoneTestEnv) {
	env.jctx.AppConfig.Discord = config.DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		BotToken:     "bot-token",
		PublicKey:    "00112233",
	}
}

func discordEscalationRoute(orgUID string, verified bool) *models.UserNotificationRoute {
	contact := &models.UserContact{
		UID:             "contact-discord",
		OrganizationUID: orgUID,
		UserUID:         "user-1",
		Type:            models.UserContactTypeDiscord,
		Value:           testDiscordSnowflake,
	}

	if verified {
		now := time.Now()
		contact.VerifiedAt = &now
	}

	return &models.UserNotificationRoute{UID: "route-discord", UserUID: "user-1", Contact: contact}
}

// TestDispatch_DiscordPagesWithTheActionRow is the happy path, and it pins the one
// thing that makes a DM page as good as a channel alert: the SAME
// discord.IncidentActionRow the channel message carries.
//
// Acknowledge pressed inside a DM therefore reaches the existing,
// signature-verified interactions endpoint unchanged — there is no DM-specific
// acknowledge path that could drift from the channel one.
func TestDispatch_DiscordPagesWithTheActionRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake := newFakeDiscordBot(t)
	enableDiscordBot(env)

	run := fake.run()

	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		discordEscalationRoute(env.org.UID, true), map[string]bool{"discord": true})
	r.Equal(1, sent)

	r.Equal(1, fake.count("createDM"))
	r.Equal(testDiscordSnowflake, fake.body("createDM")["recipient_id"])
	r.Equal(1, fake.count("createMessage"))

	body := fake.body("createMessage")

	embeds, ok := body["embeds"].([]any)
	r.True(ok, "the DM must carry the incident embed")
	r.Len(embeds, 1)

	embed, ok := embeds[0].(map[string]any)
	r.True(ok)
	r.Contains(embed["title"], "Incident #1")

	components, ok := body["components"].([]any)
	r.True(ok, "the DM must carry the incident action row")
	r.Len(components, 1)

	row, ok := components[0].(map[string]any)
	r.True(ok)
	r.EqualValues(discord.ComponentTypeActionRow, row["type"])

	buttons, ok := row["components"].([]any)
	r.True(ok)
	r.NotEmpty(buttons)

	first, ok := buttons[0].(map[string]any)
	r.True(ok)
	r.Equal("Acknowledge", first["label"])
	r.Equal(discord.BuildCustomID(discord.ActionAcknowledge, env.incident.UID), first["custom_id"],
		"the custom id must be the one the existing interactions endpoint parses")

	// No mention line in a DM: it has exactly one reader already.
	r.Empty(body["content"])
}

// TestDispatch_DiscordSeverityGate: the filter follows the email/SMS/Telegram
// "on unless excluded" rule, not the voice/WhatsApp "off unless named" one — a
// Discord DM is free and lands where the member already reads alerts.
func TestDispatch_DiscordSeverityGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		filter map[string]bool
		want   int
	}{
		{name: "nil filter delivers", filter: nil, want: 1},
		{name: "discord named delivers", filter: map[string]bool{"discord": true}, want: 1},
		{name: "another channel named excludes it", filter: map[string]bool{"voice": true}, want: 0},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, false, "")
			fake := newFakeDiscordBot(t)
			enableDiscordBot(env)

			sent := fake.run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
				discordEscalationRoute(env.org.UID, true), testCase.filter)

			r.Equal(testCase.want, sent)
			r.Equal(testCase.want, fake.count("createMessage"))
		})
	}

	// severityAllowsPersonTargets must also know the token, or a severity whose
	// ONLY channel is discord would never open the person-target gate at all and
	// this whole path would be unreachable.
	require.True(t, severityAllowsPersonTargets(map[string]bool{"discord": true}))
}

// TestDispatch_DiscordUnverifiedContactIsSkipped: the binding IS the opt-in, so an
// unverified discord contact means it was revoked. Paging it would be paging a
// route the member has withdrawn.
func TestDispatch_DiscordUnverifiedContactIsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake := newFakeDiscordBot(t)
	enableDiscordBot(env)

	sent := fake.run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		discordEscalationRoute(env.org.UID, false), nil)

	r.Equal(0, sent)
	r.Equal(0, fake.count("createDM"), "nothing may reach Discord for a revoked binding")
}

// TestDispatch_DiscordUnconfiguredBotIsSkipped: an instance with no bot degrades to
// a skip, so the escalation step falls through to the next route instead of
// failing the whole step.
func TestDispatch_DiscordUnconfiguredBotIsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake := newFakeDiscordBot(t)
	// Deliberately NOT enableDiscordBot: BotConfigured() is false.

	sent := fake.run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		discordEscalationRoute(env.org.UID, true), nil)

	r.Equal(0, sent)
	r.Equal(0, fake.count("createDM"))
}

// TestDispatch_Discord50007DoesNotUnverifyTheContact is the deliberate divergence
// from the Telegram path, and the reason it is deliberate.
//
// A Telegram block is an explicit act by the user against the bot, so that path
// clears VerifiedAt. Discord's 50007 is ALSO what a member who has simply not
// joined the server yet gets — un-verifying their contact would demolish a working
// binding they are one invite away from using, and they would never be told why
// they stopped being paged.
func TestDispatch_Discord50007DoesNotUnverifyTheContact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake := newFakeDiscordBot(t)
	fake.postStatus = http.StatusForbidden
	fake.postBody = `{"code":50007,"message":"Cannot send messages to this user"}`
	enableDiscordBot(env)

	user := models.NewUser("oncall-discord@example.com")
	r.NoError(env.db.CreateUser(ctx, user))

	now := time.Now()
	contact := models.NewUserContact(
		user.UID, env.org.UID, models.UserContactTypeDiscord, testDiscordSnowflake, "Discord")
	contact.VerifiedAt = &now
	r.NoError(env.db.UpsertUserContact(ctx, contact))

	route := &models.UserNotificationRoute{UID: "route-discord", UserUID: user.UID, Contact: contact}

	sent := fake.run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, route, nil)
	r.Equal(0, sent, "a refused DM is not a delivery")

	stored, err := env.db.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.VerifiedAt, "a 50007 must NOT un-verify the contact")
}

// TestDispatch_DiscordDedupsOneRecipientPerRun: the same human can be reached
// through two routes in one step (on call by schedule AND named on the policy),
// and two identical DMs from the same bot in the same second read as a bug.
func TestDispatch_DiscordDedupsOneRecipientPerRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake := newFakeDiscordBot(t)
	enableDiscordBot(env)

	run := fake.run()
	route := discordEscalationRoute(env.org.UID, true)

	r.Equal(1, run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, route, nil))
	r.Equal(0, run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, route, nil))
	r.Equal(1, fake.count("createMessage"))
}
