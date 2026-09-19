package usernotifications

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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// configuredDiscord is a fully configured instance bot. BotConfigured() needs all
// four values including the public key, because a DM carries the incident action
// row and an instance that cannot verify interactions would DM a dead button.
func configuredDiscord() *config.DiscordOAuthConfig {
	return &config.DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		BotToken:     "bot-token",
		PublicKey:    "00112233",
	}
}

// fakeDiscordAPI records the bot REST calls a test provokes.
type fakeDiscordAPI struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []string
	payloads map[string]map[string]any
	// postStatus / postBody answer POST /channels/{id}/messages.
	postStatus int
	postBody   string
}

func newFakeDiscordAPI(t *testing.T) *fakeDiscordAPI {
	t.Helper()

	fake := &fakeDiscordAPI{
		payloads:   map[string]map[string]any{},
		postStatus: http.StatusOK,
		postBody:   `{"id":"M-1","channel_id":"DM-1"}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/users/@me/channels", func(w http.ResponseWriter, r *http.Request) {
		fake.record("createDM", r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"DM-1","type":1}`))
	})
	mux.HandleFunc("/channels/", func(w http.ResponseWriter, r *http.Request) {
		fake.record("createMessage", r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fake.postStatus)
		_, _ = w.Write([]byte(fake.postBody))
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeDiscordAPI) record(name string, req *http.Request) {
	payload := map[string]any{}

	if raw, _ := io.ReadAll(req.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, name)
	f.payloads[name] = payload
}

func (f *fakeDiscordAPI) callCount(name string) int {
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

func (f *fakeDiscordAPI) payload(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.payloads[name]
}

// discordRoute builds a connected, verified Discord route.
func discordRoute(discordUserID string) *models.UserNotificationRoute {
	return &models.UserNotificationRoute{
		UID:     "route-uid",
		Enabled: true,
		Contact: &models.UserContact{
			UID:   "contact-uid",
			Type:  models.UserContactTypeDiscord,
			Value: discordUserID,
			Label: "Discord",
		},
	}
}

// contactEnv is a real database with one org and one member, for the creation
// paths (which write rows and therefore cannot use a nil db).
type contactEnv struct {
	db   db.Service
	svc  *Service
	org  *models.Organization
	user *models.User
}

func setupContactEnv(t *testing.T, opts ...Option) (context.Context, *contactEnv) {
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

	opts = append([]Option{WithDiscordConfig(configuredDiscord())}, opts...)

	return ctx, &contactEnv{
		db:   dbSvc,
		svc:  NewService(dbSvc, nil, opts...),
		org:  org,
		user: user,
	}
}

// TestCreateContactRejectsTypedDiscordSnowflake is the security invariant this
// whole section exists for, with its positive control right beside it.
//
// A Discord user id is PUBLIC — anyone can copy a stranger's snowflake out of a
// Discord client in two clicks — and there is no verification round trip that
// could catch a wrong one. If the generic create endpoint accepted one, any user
// could point our incident DMs at any Discord account on earth, indefinitely,
// with nothing telling that person where the messages came from.
//
// The positive control matters as much as the negative: a contact created from a
// real Discord SIGN-IN must work, and be born verified, or the rejection would
// just be a broken feature rather than a closed hole.
func TestCreateContactRejectsTypedDiscordSnowflake(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	// NEGATIVE: a typed snowflake, through the generic endpoint.
	_, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type:  models.UserContactTypeDiscord,
		Value: "999888777666555444", // somebody else's id
		Label: "Discord",
	})
	r.ErrorIs(err, ErrDiscordContactNotDirect)

	routes, err := env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
	r.NoError(err)

	for _, route := range routes {
		r.NotEqual(models.UserContactTypeDiscord, route.Contact.Type,
			"a rejected create must leave NO discord contact behind")
	}

	// POSITIVE CONTROL: the same member, with a Discord sign-in on file, gets a
	// working contact through the connect path.
	r.NoError(env.db.CreateUserProvider(ctx,
		models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, "111222333444555666")))

	created, err := env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.Equal(models.UserContactTypeDiscord, created.Contact.Type)
	r.Equal("111222333444555666", created.Contact.Value)
	r.NotNil(created.Contact.VerifiedAt,
		"the OAuth binding IS the proof, so the contact is born verified")
}

// TestConnectDiscordIsIdempotent: pressing Connect twice must not create a second
// contact for the same account.
func TestConnectDiscordIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	r.NoError(env.db.CreateUserProvider(ctx,
		models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, "111222333444555666")))

	first, err := env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
	r.NoError(err)

	second, err := env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.Equal(first.Contact.UID, second.Contact.UID)
}

// TestConnectDiscordRequiresASignIn: with nothing on file there is no attested
// snowflake, so the member is sent to the link flow instead of being asked to
// type one.
func TestConnectDiscordRequiresASignIn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	_, err := env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
	r.ErrorIs(err, ErrDiscordNotSignedIn)
}

// TestConnectDiscordRefusedWhenBotUnconfigured: an instance with no Discord bot
// must not hand out a contact it can never deliver to.
func TestConnectDiscordRefusedWhenBotUnconfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t, WithDiscordConfig(&config.DiscordOAuthConfig{}))

	r.NoError(env.db.CreateUserProvider(ctx,
		models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, "111222333444555666")))

	_, err := env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
	r.ErrorIs(err, ErrDiscordNotEnabled)
}

// TestCreateDiscordLinkAsksForNoGuildsScope: link mode must not hold a guild
// list, because that is what makes it structurally incapable of resolving (or
// creating) an organization.
func TestCreateDiscordLinkAsksForNoGuildsScope(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t, WithServerBaseURL("https://solidping.example"))

	resp, err := env.svc.CreateDiscordLink(ctx, env.org.Slug, env.user, "/d/orgs/acme/account/notifications")
	r.NoError(err)

	parsed, err := url.Parse(resp.URL)
	r.NoError(err)
	r.Equal("identify email", parsed.Query().Get("scope"))
	r.NotContains(parsed.Query().Get("scope"), "guilds")
	r.Equal("https://solidping.example/api/v1/auth/discord/callback",
		parsed.Query().Get("redirect_uri"))
	r.True(strings.HasPrefix(parsed.Query().Get("state"), "link:"),
		"the state must carry the link marker the callback branches on")
	r.WithinDuration(time.Now().Add(15*time.Minute), resp.ExpiresAt, time.Minute)
}

// TestDispatchTestRoute_DiscordSendsDM goes through dispatchTestRoute rather than
// calling the helper directly: the Telegram version of this gap WAS a missing
// case in that switch, so a test invoking the helper would pass against a switch
// that never reaches it.
func TestDispatchTestRoute_DiscordSendsDM(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeDiscordAPI(t)
	svc := NewService(nil, nil, WithDiscordConfig(configuredDiscord()))
	svc.discordAPIBaseURL = fake.server.URL

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", "org-slug", discordRoute("111222333444555666"),
		nil, nil, webpush.Options{},
	)
	r.NoError(err)

	r.Equal(1, fake.callCount("createDM"), "the DM channel must be opened")
	r.Equal("111222333444555666", fake.payload("createDM")["recipient_id"])
	r.Equal(1, fake.callCount("createMessage"), "the test button must actually send a message")
	r.Contains(fake.payload("createMessage")["content"], "Test alert from SolidPing")
}

// TestDispatchTestRoute_Discord50007SurfacesTheRemedy: the member pressing Test is
// checking a setup they just completed, and 50007 is the one outcome they can
// personally fix. A generic "send failed" leaves them nothing to act on.
func TestDispatchTestRoute_Discord50007SurfacesTheRemedy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeDiscordAPI(t)
	fake.postStatus = http.StatusForbidden
	fake.postBody = `{"code":50007,"message":"Cannot send messages to this user"}`

	svc := NewService(nil, nil, WithDiscordConfig(configuredDiscord()))
	svc.discordAPIBaseURL = fake.server.URL

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", "org-slug", discordRoute("111222333444555666"),
		nil, nil, webpush.Options{},
	)

	r.ErrorIs(err, ErrDiscordDMRefused)
	r.Contains(err.Error(), "open your DMs for server members")
	r.NotContains(err.Error(), "provider not configured",
		"a refusal must be named as such, not reported as an unknown contact type")
}

// TestDispatchTestRoute_DiscordUnconfiguredIsNamed proves an unconfigured
// instance gets the specific error, not the generic default that hid the Telegram
// version of this bug.
func TestDispatchTestRoute_DiscordUnconfiguredIsNamed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc := NewService(nil, nil, WithDiscordConfig(&config.DiscordOAuthConfig{}))

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", "org-slug", discordRoute("111222333444555666"),
		nil, nil, webpush.Options{},
	)

	r.ErrorIs(err, ErrDiscordNotEnabled)
	r.NotContains(err.Error(), "provider not configured")
}

// TestContactRequiresSetupCoversDiscord: a discord contact is connected or it is
// nothing, exactly like a Telegram one. An unverified row means the binding was
// revoked, and testing or paging it would be pointless.
func TestContactRequiresSetupCoversDiscord(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(contactRequiresSetup(models.UserContactTypeDiscord))
	r.True(contactRequiresSetup(models.UserContactTypeTelegram))
	r.False(contactRequiresSetup(models.UserContactTypeEmail))
	r.False(models.ContactRequiresVerification(models.UserContactTypeDiscord),
		"there is no verification CODE for discord — the binding is the proof")
}

// TestListRoutesOffersTheDiscordSuggestion: the one-click connect row appears when
// a sign-in is on file and the bot is configured, and disappears once connected.
func TestListRoutesOffersTheDiscordSuggestion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	r.NoError(env.db.CreateUserProvider(ctx,
		models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, "111222333444555666")))

	resp, err := env.svc.ListRoutes(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.NotNil(resp.DiscordSuggestion)
	r.Equal("111222333444555666", resp.DiscordSuggestion.DiscordUserID)

	_, err = env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
	r.NoError(err)

	resp, err = env.svc.ListRoutes(ctx, env.org.Slug, env.user)
	r.NoError(err)
	r.Nil(resp.DiscordSuggestion, "an already-connected account must not be suggested again")
}
