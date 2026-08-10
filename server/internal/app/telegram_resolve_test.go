package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/usernotifications"
)

// resolveTestDB is a throwaway in-memory SQLite service. Each test gets its own
// so a persisted parameter from one can never leak into another.
func resolveTestDB(t *testing.T) db.Service {
	t.Helper()

	ctx := context.Background()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

// resolveTestConfig is a token-only instance pointed at the fake Bot API — the
// shape this whole spec exists to make sufficient.
func resolveTestConfig(fake *fakeTelegramAPI) *config.Config {
	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.test"
	cfg.Telegram = config.TelegramConfig{
		Enabled:  config.BoolPtr(true),
		BotToken: "123456789:AAtest-token",
		BaseURL:  fake.server.URL,
	}

	return cfg
}

// storedParam reads a system parameter's string value.
func storedParam(t *testing.T, dbSvc db.Service, key string) (string, bool) {
	t.Helper()

	param, err := dbSvc.GetSystemParameter(context.Background(), key)
	require.NoError(t, err)

	if param == nil {
		return "", false
	}

	value, ok := param.Value[models.ParameterValueKey].(string)

	return value, ok
}

// --- First boot -------------------------------------------------------------

// TestResolveTelegram_FirstBootDerivesEverything is the headline case: a
// deployment holding ONLY SP_TELEGRAM_BOT_TOKEN ends up with a username fetched
// from getMe, a generated webhook secret, both persisted, and a correct connect
// link — with no other operator input.
func TestResolveTelegram_FirstBootDerivesEverything(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "solidping_derived_bot"

	dbSvc := resolveTestDB(t)
	cfg := resolveTestConfig(fake)

	resolveTelegramSettings(context.Background(), dbSvc, cfg)

	r.Equal("solidping_derived_bot", cfg.Telegram.BotUsername)
	r.NotEmpty(cfg.Telegram.WebhookSecret)
	r.True(cfg.Telegram.Active(), "the connect surface comes up on the very same boot")

	// Both values are on disk, so no later boot has to re-derive them.
	username, ok := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.True(ok)
	r.Equal("solidping_derived_bot", username)

	secret, ok := storedParam(t, dbSvc, TelegramWebhookSecretParam)
	r.True(ok)
	r.Equal(cfg.Telegram.WebhookSecret, secret)

	// The connect link points at the derived bot.
	r.Equal(
		"https://t.me/solidping_derived_bot?start=tok",
		usernotifications.TelegramLinkURL(cfg.Telegram.ResolvedBotUsername(), "tok"),
	)

	// The secret parameter is flagged secret so the parameters API masks it.
	param, err := dbSvc.GetSystemParameter(context.Background(), TelegramWebhookSecretParam)
	r.NoError(err)
	r.NotNil(param.Secret)
	r.True(*param.Secret)

	// The username parameter is NOT a secret — the browser needs it.
	param, err = dbSvc.GetSystemParameter(context.Background(), TelegramBotUsernameParam)
	r.NoError(err)
	r.NotNil(param.Secret)
	r.False(*param.Secret)
}

// TestResolveTelegram_SecondBootMakesNoGetMeCall proves the derived username is
// genuinely cached: after the first boot, startup makes NO network call at all.
func TestResolveTelegram_SecondBootMakesNoGetMeCall(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "cached_bot"

	dbSvc := resolveTestDB(t)

	first := resolveTestConfig(fake)
	resolveTelegramSettings(context.Background(), dbSvc, first)
	r.Equal("cached_bot", first.Telegram.BotUsername)
	r.Equal(1, fake.callCount("getMe"), "the first boot has to ask")

	// Second boot: fresh process config, SAME database.
	second := resolveTestConfig(fake)
	resolveTelegramSettings(context.Background(), dbSvc, second)

	r.Equal("cached_bot", second.Telegram.BotUsername)
	r.Equal(1, fake.callCount("getMe"), "a later boot must not call getMe again")
	r.Equal(first.Telegram.WebhookSecret, second.Telegram.WebhookSecret,
		"and it must reuse the SAME secret, not mint a new one")
}

// TestResolveTelegram_GetMeUnreachableDegradesGracefully covers the first boot
// where Telegram cannot be reached: startup finishes inside the bounded
// timeout, the connect surface stays off, nothing crashes, no bogus username is
// persisted — and the NEXT boot recovers on its own.
func TestResolveTelegram_GetMeUnreachableDegradesGracefully(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A server that accepts the request and then simply never answers, which is
	// what an unreachable Telegram looks like from the inside.
	blocked := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-blocked:
		}
	}))

	t.Cleanup(func() {
		close(blocked)
		hang.Close()
	})

	dbSvc := resolveTestDB(t)

	cfg := &config.Config{}
	cfg.Telegram = config.TelegramConfig{
		Enabled:  config.BoolPtr(true),
		BotToken: "123456789:AAtest-token",
		BaseURL:  hang.URL,
	}

	start := time.Now()
	resolveTelegramSettings(context.Background(), dbSvc, cfg)
	elapsed := time.Since(start)

	r.Less(elapsed, telegramGetMeTimeout+5*time.Second,
		"an unreachable Telegram may delay startup by the bounded timeout, no more")

	r.Empty(cfg.Telegram.BotUsername)
	r.False(cfg.Telegram.Active(), "the connect surface stays off")
	r.True(cfg.Telegram.Configured(), "but the instance is still configured: route + dispatch stay on")

	// Nothing bogus was written.
	_, ok := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.False(ok, "a failed getMe must not persist a username")

	// The secret is local, so it was still generated and persisted.
	r.NotEmpty(cfg.Telegram.WebhookSecret)
	secret, ok := storedParam(t, dbSvc, TelegramWebhookSecretParam)
	r.True(ok)
	r.Equal(cfg.Telegram.WebhookSecret, secret)

	// Next boot, Telegram is back: it recovers with no operator action.
	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "recovered_bot"

	next := resolveTestConfig(fake)
	resolveTelegramSettings(context.Background(), dbSvc, next)

	r.Equal("recovered_bot", next.Telegram.BotUsername)
	r.True(next.Telegram.Active())
	r.Equal(secret, next.Telegram.WebhookSecret, "and the secret is still the original one")
}

// --- Precedence -------------------------------------------------------------

// TestResolveTelegram_EnvWinsOverStoredParameters pins the precedence rule for
// BOTH keys in its hardest form: env set AND a *different* value already stored.
// Env must win, and the stored parameter must be left untouched — an operator
// fronting the webhook with a proxy, or running GitOps config, must never be
// fought by the server, and reverting to a derived value must not be a
// data-loss event.
func TestResolveTelegram_EnvWinsOverStoredParameters(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "derived_bot"

	dbSvc := resolveTestDB(t)

	// Pre-existing (different) persisted values.
	r.NoError(dbSvc.SetSystemParameter(ctx, TelegramBotUsernameParam, "stored_bot", false))
	r.NoError(dbSvc.SetSystemParameter(ctx, TelegramWebhookSecretParam, "stored-secret", true))

	cfg := resolveTestConfig(fake)
	cfg.Telegram.BotUsername = "env_bot"
	cfg.Telegram.WebhookSecret = "env-secret"

	resolveTelegramSettings(ctx, dbSvc, cfg)

	r.Equal("env_bot", cfg.Telegram.BotUsername)
	r.Equal("env-secret", cfg.Telegram.WebhookSecret)
	r.Equal(0, fake.callCount("getMe"), "an env username must not trigger a network call")

	username, _ := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.Equal("stored_bot", username, "the stored parameter must be left untouched")

	secret, _ := storedParam(t, dbSvc, TelegramWebhookSecretParam)
	r.Equal("stored-secret", secret, "the stored parameter must be left untouched")
}

// TestResolveTelegram_StoredWinsOverDerived is the middle rung: with no env
// value but a parameter on disk, the parameter is used and nothing is derived.
func TestResolveTelegram_StoredWinsOverDerived(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "derived_bot"

	dbSvc := resolveTestDB(t)
	r.NoError(dbSvc.SetSystemParameter(ctx, TelegramBotUsernameParam, "stored_bot", false))
	r.NoError(dbSvc.SetSystemParameter(ctx, TelegramWebhookSecretParam, "stored-secret", true))

	cfg := resolveTestConfig(fake)
	resolveTelegramSettings(ctx, dbSvc, cfg)

	r.Equal("stored_bot", cfg.Telegram.BotUsername)
	r.Equal("stored-secret", cfg.Telegram.WebhookSecret)
	r.Equal(0, fake.callCount("getMe"), "a stored username must not trigger a network call")
}

// TestResolveTelegram_LoserAdoptsTheWinnersSecret is the fleet case: a second
// pod booting against a database that already holds a secret must adopt THAT
// one, never the candidate it just generated — otherwise it would validate
// inbound updates against a secret Telegram never received.
func TestResolveTelegram_LoserAdoptsTheWinnersSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	dbSvc := resolveTestDB(t)

	podA := resolveTestConfig(fake)
	resolveTelegramSettings(context.Background(), dbSvc, podA)

	podB := resolveTestConfig(fake)
	resolveTelegramSettings(context.Background(), dbSvc, podB)

	r.NotEmpty(podA.Telegram.WebhookSecret)
	r.Equal(podA.Telegram.WebhookSecret, podB.Telegram.WebhookSecret)
}

// --- The generated secret ---------------------------------------------------

// TestGenerateTelegramWebhookSecret_EntropyAndShape pins the properties that
// make this value safe as the SOLE authenticity gate on a public endpoint.
func TestGenerateTelegramWebhookSecret_EntropyAndShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const samples = 64

	seen := make(map[string]struct{}, samples)

	for range samples {
		secret, err := generateTelegramWebhookSecret()
		r.NoError(err)

		// ≥32 bytes of actual entropy, not 32 characters.
		raw, err := base64.RawURLEncoding.DecodeString(secret)
		r.NoError(err, "must be valid unpadded base64url")
		r.GreaterOrEqual(len(raw), 32)

		// URL-safe: no '+', '/' or '=' to break a header, URL or shell paste.
		for _, c := range secret {
			r.True(
				(c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
					(c >= '0' && c <= '9') || c == '-' || c == '_',
				"secret must be URL-safe, got %q", string(c),
			)
		}

		// Telegram caps secret_token at 256 characters.
		r.LessOrEqual(len(secret), 256)

		_, dup := seen[secret]
		r.False(dup, "generated secrets must be distinct across instances")
		seen[secret] = struct{}{}
	}

	r.Len(seen, samples)
}

// TestResolveTelegram_SecretIsNeverLogged guards the obvious way a secret
// leaks: an over-helpful log line. Resolution may say THAT it resolved one,
// never WHICH.
func TestResolveTelegram_SecretIsNeverLogged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	dbSvc := resolveTestDB(t)
	cfg := resolveTestConfig(fake)

	var logs bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	defer slog.SetDefault(previous)

	resolveTelegramSettings(context.Background(), dbSvc, cfg)

	r.NotEmpty(cfg.Telegram.WebhookSecret)
	r.NotContains(logs.String(), cfg.Telegram.WebhookSecret,
		"the generated webhook secret must never reach the logs")
	r.NotContains(logs.String(), cfg.Telegram.BotToken,
		"nor must the bot token")
}

// TestResolveTelegram_SecretNeverReachesPublicConfig is the end-to-end control
// for the same property over the wire: /api/v1/config is unauthenticated, so a
// derived secret appearing there would hand the webhook to anyone.
func TestResolveTelegram_SecretNeverReachesPublicConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "public_config_bot"

	server, ts := telegramRouteServer(t, config.TelegramConfig{
		Enabled:  config.BoolPtr(true),
		BotToken: "123456789:AAtest",
		BaseURL:  fake.server.URL,
		// No webhook secret and no username: BOTH are derived at boot.
	})

	secret := server.config.Telegram.WebhookSecret
	r.NotEmpty(secret, "SetupRoutes must have derived a secret synchronously")

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		ts.URL+"/api/v1/config", nil)
	r.NoError(err)

	resp, err := ts.Client().Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var raw bytes.Buffer
	_, err = raw.ReadFrom(resp.Body)
	r.NoError(err)

	r.NotContains(raw.String(), secret, "the webhook secret must never leave the server")
	r.NotContains(raw.String(), server.config.Telegram.BotToken)

	// The derived username, on the other hand, is exactly what the browser needs.
	var payload struct {
		Telegram struct {
			Enabled     bool   `json:"enabled"`
			BotUsername string `json:"botUsername"`
		} `json:"telegram"`
	}
	r.NoError(json.Unmarshal(raw.Bytes(), &payload))
	r.True(payload.Telegram.Enabled, "a token-only instance self-completes and turns the connect surface on")
	r.Equal("public_config_bot", payload.Telegram.BotUsername)
}

// TestResolveTelegram_NoOpWhenNotConfigured keeps an unconfigured deployment
// completely inert: no parameter is created, no call is made.
func TestResolveTelegram_NoOpWhenNotConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	dbSvc := resolveTestDB(t)

	cfg := resolveTestConfig(fake)
	cfg.Telegram.BotToken = ""

	resolveTelegramSettings(context.Background(), dbSvc, cfg)

	r.Empty(cfg.Telegram.WebhookSecret)
	r.Empty(cfg.Telegram.BotUsername)
	r.Equal(0, fake.callCount("getMe"))

	_, ok := storedParam(t, dbSvc, TelegramWebhookSecretParam)
	r.False(ok, "an unconfigured instance must not create parameters")
}
