package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	testTelegramBotToken = "123456789:AAtelegram-test-secret"
	testTelegramUserID   = int64(424242)
)

// signTelegramFields adds the hash Telegram would compute with botToken.
func signTelegramFields(fields map[string]string, botToken string) map[string]string {
	signed := make(map[string]string, len(fields)+1)
	for key, value := range fields {
		signed[key] = value
	}

	signed[telegramFieldHash] = telegramLoginHash(fields, botToken)

	return signed
}

// telegramLoginFields is an unsigned login result for testTelegramUserID.
func telegramLoginFields(authDate time.Time) map[string]string {
	return map[string]string{
		"id":         strconv.FormatInt(testTelegramUserID, 10),
		"first_name": "Acme",
		"last_name":  "Member",
		"username":   "acme_member",
		"photo_url":  "https://t.me/i/userpic/320/acme.jpg",
		"auth_date":  strconv.FormatInt(authDate.Unix(), 10),
	}
}

func TestVerifyTelegramLogin(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	fresh := signTelegramFields(telegramLoginFields(now.Add(-30*time.Second)), testTelegramBotToken)

	t.Run("valid", func(t *testing.T) {
		t.Parallel()

		identity, err := VerifyTelegramLogin(fresh, testTelegramBotToken, now)
		require.NoError(t, err)
		require.Equal(t, testTelegramUserID, identity.ID)
		require.Equal(t, "acme_member", identity.Username)
	})

	t.Run("uppercase hash is accepted", func(t *testing.T) {
		t.Parallel()

		upper := map[string]string{}
		for k, v := range fresh {
			upper[k] = v
		}

		upper[telegramFieldHash] = strings.ToUpper(upper[telegramFieldHash])

		_, err := VerifyTelegramLogin(upper, testTelegramBotToken, now)
		require.NoError(t, err)
	})

	t.Run("tampered field", func(t *testing.T) {
		t.Parallel()

		tampered := map[string]string{}
		for k, v := range fresh {
			tampered[k] = v
		}

		tampered["id"] = "1"

		_, err := VerifyTelegramLogin(tampered, testTelegramBotToken, now)
		require.ErrorIs(t, err, ErrTelegramAuthInvalid)
	})

	t.Run("added field", func(t *testing.T) {
		t.Parallel()

		added := map[string]string{"extra": "x"}
		for k, v := range fresh {
			added[k] = v
		}

		_, err := VerifyTelegramLogin(added, testTelegramBotToken, now)
		require.ErrorIs(t, err, ErrTelegramAuthInvalid)
	})

	t.Run("wrong bot token", func(t *testing.T) {
		t.Parallel()

		_, err := VerifyTelegramLogin(fresh, "987654321:AAanother-bot", now)
		require.ErrorIs(t, err, ErrTelegramAuthInvalid)
	})

	t.Run("missing hash", func(t *testing.T) {
		t.Parallel()

		_, err := VerifyTelegramLogin(telegramLoginFields(now), testTelegramBotToken, now)
		require.ErrorIs(t, err, ErrTelegramAuthInvalid)
	})

	t.Run("stale auth_date", func(t *testing.T) {
		t.Parallel()

		stale := signTelegramFields(
			telegramLoginFields(now.Add(-6*time.Minute)), testTelegramBotToken)

		_, err := VerifyTelegramLogin(stale, testTelegramBotToken, now)
		require.ErrorIs(t, err, ErrTelegramAuthExpired)
	})

	t.Run("future auth_date", func(t *testing.T) {
		t.Parallel()

		future := signTelegramFields(
			telegramLoginFields(now.Add(10*time.Minute)), testTelegramBotToken)

		_, err := VerifyTelegramLogin(future, testTelegramBotToken, now)
		require.ErrorIs(t, err, ErrTelegramAuthExpired)
	})
}

// freshTelegramResult is a just-signed, valid login result.
func freshTelegramResult() map[string]string {
	return signTelegramFields(telegramLoginFields(time.Now()), testTelegramBotToken)
}

// encodeTelegramAuthResult builds a tgAuthResult the way oauth.telegram.org
// does: numbers as JSON numbers, the object base64-encoded.
func encodeTelegramAuthResult(t *testing.T, fields map[string]string) string {
	t.Helper()

	object := map[string]any{}

	for key, value := range fields {
		if key == "id" || key == "auth_date" {
			object[key] = json.Number(value)

			continue
		}

		object[key] = value
	}

	raw, err := json.Marshal(object)
	require.NoError(t, err)

	return base64.URLEncoding.EncodeToString(raw)
}

func TestDecodeTelegramAuthResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now()
	signed := signTelegramFields(telegramLoginFields(now), testTelegramBotToken)

	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		raw, err := base64.URLEncoding.DecodeString(encodeTelegramAuthResult(t, signed))
		r.NoError(err)

		fields, err := DecodeTelegramAuthResult(enc.EncodeToString(raw))
		r.NoError(err)
		r.Equal(signed, fields)

		_, err = VerifyTelegramLogin(fields, testTelegramBotToken, now)
		r.NoError(err)
	}

	_, err := DecodeTelegramAuthResult("not base64 !!")
	r.ErrorIs(err, ErrTelegramAuthInvalid)

	_, err = DecodeTelegramAuthResult(base64.StdEncoding.EncodeToString([]byte(`{"id":{"x":1}}`)))
	r.ErrorIs(err, ErrTelegramAuthInvalid)
}

// telegramFixture is an org with one member who linked a Telegram account.
type telegramFixture struct {
	svc     *TelegramLoginService
	handler *TelegramLoginHandler
	org     *models.Organization
	user    *models.User
}

func newTelegramFixture(t *testing.T, link bool) *telegramFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	dbService := newSQLiteDBService(t)

	cfg := &config.Config{
		Telegram: config.TelegramConfig{
			BotToken:     testTelegramBotToken,
			BotUsername:  "acme_bot",
			LoginEnabled: true,
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{BaseURL: "https://solidping.acme.example"},
	}

	fixture := nextFixture()
	org := models.NewOrganization("acme-tg-"+fixture, "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	user := models.NewUser("member" + fixture + "@acme.example")
	r.NoError(dbService.CreateUser(ctx, user))
	r.NoError(dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)))

	if link {
		contact := models.NewUserContact(user.UID, org.UID, models.UserContactTypeTelegram,
			strconv.FormatInt(testTelegramUserID, 10), "tg")
		now := time.Now()
		contact.VerifiedAt = &now
		r.NoError(dbService.UpsertUserContact(ctx, contact))
	}

	svc := NewTelegramLoginService(dbService, cfg, NewService(dbService, cfg.Auth, cfg, nil, nil))

	return &telegramFixture{svc: svc, handler: NewTelegramLoginHandler(svc, cfg), org: org, user: user}
}

// callback drives the real callback with a signed result in tgAuthResult.
func (f *telegramFixture) callback(t *testing.T, state string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	return serveCallback(t, f.handler.Callback, telegramLoginCallbackPath+"?state="+url.QueryEscape(state)+
		"&tgAuthResult="+url.QueryEscape(encodeTelegramAuthResult(t, fields)))
}

// runTelegramCallback plugs Telegram into the shared handoff contract.
func runTelegramCallback(t *testing.T, opts callbackOpts) callbackRun {
	t.Helper()

	f := newTelegramFixture(t, true)
	returnTo := opts.returnTo("/d/orgs/" + f.org.Slug + "/checks")
	loginPath := "/api/v1/auth/telegram/login?org=" + f.org.Slug

	var state string

	if opts.viaLogin {
		rec := serveCallback(t, f.handler.Login, loginPath+"&redirect_uri="+url.QueryEscape(returnTo))
		require.Equal(t, http.StatusFound, rec.Code)

		providerURL, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)

		back, err := url.Parse(providerURL.Query().Get("return_to"))
		require.NoError(t, err)

		state = back.Query().Get("state")
	} else {
		var err error

		state, err = f.svc.GenerateOAuthState(t.Context(), returnTo, f.org.Slug)
		require.NoError(t, err)
	}

	rec := f.callback(t, state, freshTelegramResult())

	return callbackRun{rec: rec, db: f.svc.db, returnTo: returnTo, orgSlug: f.org.Slug, defaultTo: "/d/orgs/" + f.org.Slug}
}

func TestTelegramCallbackLinkedUserGetsSession(t *testing.T) {
	t.Parallel()

	assertHandoffRedirect(t, runTelegramCallback(t, callbackOpts{viaLogin: true}))
}

func TestTelegramCallbackWithoutOrgUsesContactOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTelegramFixture(t, true)

	state, err := f.svc.GenerateOAuthState(t.Context(), "/", "")
	r.NoError(err)

	rec := f.callback(t, state, freshTelegramResult())
	r.Equal(http.StatusFound, rec.Code)

	location, err := url.Parse(rec.Header().Get("Location"))
	r.NoError(err)
	r.Equal(handoffCompletePath, location.Path, rec.Header().Get("Location"))

	session, err := authhandoff.Redeem(t.Context(), f.svc.db, location.Query().Get(handoffCodeParam))
	r.NoError(err)
	r.Equal(f.org.Slug, session.OrgSlug)
	r.Equal(f.user.UID, session.UserUID)

	// The login audit names the connector.
	events, err := f.svc.db.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID:   f.org.UID,
		EventTypePrefixes: []string{"auth"},
		Limit:             10,
	})
	r.NoError(err)
	r.Len(events, 1)
	r.Equal(models.EventTypeAuthLoginSucceeded, events[0].EventType)
	r.Equal("telegram", events[0].Payload["auth_method"])
}

// redirectError parses the error a refused callback redirects with.
func redirectError(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()

	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.NotEqual(t, handoffCompletePath, location.Path, "a refused login must not hand over a session")

	return location.Query().Get("error"), location.Query().Get("error_description")
}

func TestTelegramCallbackUnlinkedIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTelegramFixture(t, false)

	state, err := f.svc.GenerateOAuthState(t.Context(), "/d/orgs/"+f.org.Slug, f.org.Slug)
	r.NoError(err)

	rec := f.callback(t, state, freshTelegramResult())
	code, description := redirectError(t, rec)
	r.Equal(OAuthCodeTelegramNotLinked, code)
	r.Contains(description, "connect Telegram in your account")
}

func TestTelegramCallbackUnverifiedContactIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTelegramFixture(t, false)

	contact := models.NewUserContact(f.user.UID, f.org.UID, models.UserContactTypeTelegram,
		strconv.FormatInt(testTelegramUserID, 10), "tg")
	r.NoError(f.svc.db.UpsertUserContact(t.Context(), contact))

	state, err := f.svc.GenerateOAuthState(t.Context(), "/", "")
	r.NoError(err)

	code, _ := redirectError(t,
		f.callback(t, state, freshTelegramResult()))
	r.Equal(OAuthCodeTelegramNotLinked, code)
}

func TestTelegramCallbackRejectsBadResults(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fields map[string]string
		code   string
	}{
		"wrong bot token": {
			fields: signTelegramFields(telegramLoginFields(time.Now()), "987654321:AAother"),
			code:   OAuthCodeTelegramInvalid,
		},
		"stale": {
			fields: signTelegramFields(
				telegramLoginFields(time.Now().Add(-time.Hour)), testTelegramBotToken),
			code: OAuthCodeTelegramExpired,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newTelegramFixture(t, true)

			state, err := f.svc.GenerateOAuthState(t.Context(), "/", "")
			require.NoError(t, err)

			code, _ := redirectError(t, f.callback(t, state, tc.fields))
			require.Equal(t, tc.code, code)
		})
	}
}

func TestTelegramCallbackStateIsSingleUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTelegramFixture(t, true)

	state, err := f.svc.GenerateOAuthState(t.Context(), "/", "")
	r.NoError(err)

	fields := freshTelegramResult()
	r.Equal(handoffCompletePath, mustLocationPath(t, f.callback(t, state, fields)))

	code, _ := redirectError(t, f.callback(t, state, fields))
	r.Equal(OAuthCodeInvalidState, code)
}

func mustLocationPath(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	return location.Path
}

func TestTelegramCallbackCancelled(t *testing.T) {
	t.Parallel()

	f := newTelegramFixture(t, true)

	state, err := f.svc.GenerateOAuthState(t.Context(), "/", "")
	require.NoError(t, err)

	code, _ := redirectError(t, serveCallback(t, f.handler.Callback,
		telegramLoginCallbackPath+"?state="+url.QueryEscape(state)+"&error=access_denied"))
	require.Equal(t, "access_denied", code)
}

func TestTelegramCallbackWidgetQueryForm(t *testing.T) {
	t.Parallel()

	f := newTelegramFixture(t, true)

	state, err := f.svc.GenerateOAuthState(t.Context(), "/", "")
	require.NoError(t, err)

	query := url.Values{"state": {state}}
	for key, value := range freshTelegramResult() {
		query.Set(key, value)
	}

	rec := serveCallback(t, f.handler.Callback, telegramLoginCallbackPath+"?"+query.Encode())
	require.Equal(t, handoffCompletePath, mustLocationPath(t, rec))
}

func TestTelegramCallbackServesFragmentBridge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTelegramFixture(t, true)

	rec := serveCallback(t, f.handler.Callback, telegramLoginCallbackPath+"?state=abc")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Header().Get("Content-Type"), "text/html")
	r.Contains(rec.Body.String(), telegramBridgeScript)
	r.Equal(telegramBridgeCSP(), rec.Header().Get("Content-Security-Policy"))
	r.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'sha256-")
	r.NotContains(rec.Header().Get("Content-Security-Policy"), "unsafe-inline")
}

func TestTelegramLoginRedirectsToTelegram(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTelegramFixture(t, true)

	rec := serveCallback(t, f.handler.Login,
		"/api/v1/auth/telegram/login?org="+f.org.Slug+"&redirect_uri="+url.QueryEscape("/d/orgs/"+f.org.Slug))
	r.Equal(http.StatusFound, rec.Code)

	target, err := url.Parse(rec.Header().Get("Location"))
	r.NoError(err)
	r.Equal("oauth.telegram.org", target.Host)
	r.Equal("/auth", target.Path)
	r.Equal("123456789", target.Query().Get("bot_id"))
	r.Equal("https://solidping.acme.example", target.Query().Get("origin"))

	back, err := url.Parse(target.Query().Get("return_to"))
	r.NoError(err)
	r.Equal("https://solidping.acme.example"+telegramLoginCallbackPath, back.Scheme+"://"+back.Host+back.Path)
	r.NotEmpty(back.Query().Get("state"))
	r.NotContains(rec.Header().Get("Location"), "AAtelegram-test-secret", "the token secret never leaves the server")
}

func TestTelegramLoginRefusedWhenDisabled(t *testing.T) {
	t.Parallel()

	f := newTelegramFixture(t, true)
	f.handler.cfg.Telegram.LoginEnabled = false

	rec := serveCallback(t, f.handler.Login, "/api/v1/auth/telegram/login")
	require.Equal(t, http.StatusFound, rec.Code)
	require.NotContains(t, rec.Header().Get("Location"), "oauth.telegram.org")
}

func TestListProvidersTelegramGating(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		telegram config.TelegramConfig
		want     bool
	}{
		"bot and login switch":   {config.TelegramConfig{BotToken: testTelegramBotToken, LoginEnabled: true}, true},
		"bot without the switch": {config.TelegramConfig{BotToken: testTelegramBotToken}, false},
		"switch without a bot":   {config.TelegramConfig{LoginEnabled: true}, false},
		"bot killed": {
			config.TelegramConfig{Enabled: config.BoolPtr(false), BotToken: testTelegramBotToken, LoginEnabled: true},
			false,
		},
		"token without a bot id": {config.TelegramConfig{BotToken: "not-a-token", LoginEnabled: true}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := NewProvidersHandler(&config.Config{Telegram: tc.telegram}, nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers", nil)
			require.NoError(t, handler.ListProviders(rec, req))

			var resp ProvidersResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

			found := false

			for _, provider := range resp.Data {
				if provider.Type == "telegram" {
					found = true
				}
			}

			require.Equal(t, tc.want, found)
		})
	}
}
