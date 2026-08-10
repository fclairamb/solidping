package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// telegramRouteServer boots a real server (real NewServer + real SetupRoutes,
// so the synchronous Telegram resolution actually runs) over an in-memory
// SQLite DB with the supplied Telegram config.
func telegramRouteServer(t *testing.T, tg config.TelegramConfig) (*Server, *httptest.Server) {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "telegram-route-secret"
	cfg.Telegram = tg

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return server, ts
}

// postTelegramWebhook fires one update at the real router and returns the
// status code (the body is irrelevant: the handler answers empty 200/403).
func postTelegramWebhook(t *testing.T, ts *httptest.Server, secret string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		ts.URL+TelegramWebhookPath, strings.NewReader(`{"update_id":1}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	if secret != "" {
		req.Header.Set(telegram.SecretTokenHeader, secret)
	}

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// TestTelegramWebhookRouteRegisteredWithTokenOnly is the route half of the
// Configured() vs Active() split. A first boot holding ONLY a bot token has no
// @username yet — and here getMe cannot supply one either — but a route that
// was never registered cannot be repaired without a restart, so it must exist:
// 403 (not 404) to an unauthenticated caller, 200 to an authenticated one.
func TestTelegramWebhookRouteRegisteredWithTokenOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	fake.failGetMe = true

	_, ts := telegramRouteServer(t, config.TelegramConfig{
		Enabled:       config.BoolPtr(true),
		BotToken:      "123456789:AAtest",
		WebhookSecret: "route-test-secret",
		BaseURL:       fake.server.URL,
		// BotUsername deliberately absent.
	})

	// Wrong secret: the route exists and rejects. A 404 here would mean it was
	// never registered.
	bad := postTelegramWebhook(t, ts, "not-the-secret")
	r.Equal(http.StatusForbidden, bad,
		"the webhook route must be registered on a token-only instance")

	// Right secret: the handler actually runs.
	good := postTelegramWebhook(t, ts, "route-test-secret")
	r.Equal(http.StatusOK, good)
}

// TestTelegramWebhookRouteAbsentWithoutToken keeps the other side honest: with
// no bot identity at all, no route is exposed.
func TestTelegramWebhookRouteAbsentWithoutToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, ts := telegramRouteServer(t, config.TelegramConfig{})

	code := postTelegramWebhook(t, ts, "whatever")
	// 404 or 405 depending on what the catch-all SPA route claims for the path;
	// what matters is that no Telegram POST handler answers.
	r.Contains([]int{http.StatusNotFound, http.StatusMethodNotAllowed}, code)
}

// TestPublicConfigTelegramOffWithTokenOnly is the connect-surface half: while
// the webhook route is live and escalations dispatch, /api/v1/config must still
// report Telegram off whenever the @username is unknown — here because getMe is
// unreachable — since the dashboard cannot build a t.me link without it.
func TestPublicConfigTelegramOffWithTokenOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	fake.failGetMe = true

	_, ts := telegramRouteServer(t, config.TelegramConfig{
		Enabled:       config.BoolPtr(true),
		BotToken:      "123456789:AAtest",
		WebhookSecret: "route-test-secret",
		BaseURL:       fake.server.URL,
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		ts.URL+"/api/v1/config", nil)
	r.NoError(err)

	resp, err := ts.Client().Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var payload struct {
		Telegram struct {
			Enabled     bool   `json:"enabled"`
			BotUsername string `json:"botUsername"`
		} `json:"telegram"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&payload))
	r.False(payload.Telegram.Enabled, "the connect surface stays off until the username is known")
	r.Empty(payload.Telegram.BotUsername)
}
