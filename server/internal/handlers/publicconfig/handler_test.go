package publicconfig_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/publicconfig"
)

// serve runs the handler and returns the raw response body. Assertions are made
// on the RAW JSON, not on a decoded struct: the whole point of the disabled
// case is that certain keys are ABSENT, which a struct decode would silently
// paper over by zero-valuing them.
func serve(t *testing.T, cfg *config.Config) map[string]any {
	t.Helper()

	handler := publicconfig.NewHandler(cfg)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/config", nil)

	require.NoError(t, handler.GetConfig(recorder, req))
	require.Equal(t, http.StatusOK, recorder.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	return body
}

func posthogBlock(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	block, ok := body["posthog"].(map[string]any)
	require.True(t, ok, "posthog block must be present")

	return block
}

// TestGetConfigOmitsEverythingWhenDisabled is the core negative test: on any
// deployment that has not configured PostHog, the endpoint must report
// disabled AND omit the key/host entirely — not return them as empty strings.
func TestGetConfigOmitsEverythingWhenDisabled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		config config.PostHogConfig
	}{
		{
			name:   "zero value",
			config: config.PostHogConfig{},
		},
		{
			name: "enabled but no key (the self-hosted default)",
			config: config.PostHogConfig{
				Enabled: true,
				Host:    config.DefaultPostHogHost,
			},
		},
		{
			name: "key present but kill switch off",
			config: config.PostHogConfig{
				Enabled:       false,
				Host:          "https://ph.example.com",
				ProjectAPIKey: "phc_secret_key",
			},
		},
		{
			name: "whitespace-only key",
			config: config.PostHogConfig{
				Enabled:       true,
				ProjectAPIKey: "   ",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			body := serve(t, &config.Config{PostHog: tc.config})
			block := posthogBlock(t, body)

			r.Equal(false, block["enabled"])

			// The core guarantee: ABSENT, not empty.
			_, hasKey := block["projectApiKey"]
			r.False(hasKey, "projectApiKey must be absent when analytics is off")
			_, hasHost := block["host"]
			r.False(hasHost, "host must be absent when analytics is off")

			// And exactly one key in the block.
			r.Len(block, 1)
		})
	}
}

// TestGetConfigNeverLeaksThePersonalAPIKey checks the secret never appears in
// the serialized response under ANY configuration, enabled or not.
func TestGetConfigNeverLeaksThePersonalAPIKey(t *testing.T) {
	t.Parallel()

	const personal = "phx_super_secret_personal_key"

	for _, enabled := range []bool{true, false} {
		cfg := &config.Config{PostHog: config.PostHogConfig{
			Enabled:        enabled,
			Host:           config.DefaultPostHogHost,
			ProjectAPIKey:  "phc_public_key",
			PersonalAPIKey: personal,
		}}

		handler := publicconfig.NewHandler(cfg)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/config", nil)
		require.NoError(t, handler.GetConfig(recorder, req))

		require.NotContains(t, recorder.Body.String(), personal)
		require.NotContains(t, recorder.Body.String(), "personal")
	}
}

// TestGetConfigPositiveControl is the positive control for the negative test
// above: once a key is configured, the very same fields DO appear.
func TestGetConfigPositiveControl(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := serve(t, &config.Config{PostHog: config.PostHogConfig{
		Enabled:       true,
		Host:          "https://ph.example.com",
		ProjectAPIKey: "phc_public_key",
	}})
	block := posthogBlock(t, body)

	r.Equal(true, block["enabled"])
	r.Equal("phc_public_key", block["projectApiKey"])
	r.Equal("https://ph.example.com", block["host"])
}

// TestGetConfigFallsBackToDefaultHost proves an operator who sets only a key
// gets the documented default endpoint rather than an empty host.
func TestGetConfigFallsBackToDefaultHost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := serve(t, &config.Config{PostHog: config.PostHogConfig{
		Enabled:       true,
		ProjectAPIKey: "phc_public_key",
	}})

	r.Equal(config.DefaultPostHogHost, posthogBlock(t, body)["host"])
}

// TestBuildIsNilSafe guards the handler against a nil config (defensive: the
// endpoint is public and must never panic).
func TestBuildIsNilSafe(t *testing.T) {
	t.Parallel()

	resp := publicconfig.Build(nil)
	require.False(t, resp.PostHog.Enabled)
	require.Empty(t, resp.PostHog.ProjectAPIKey)
}

// TestBuild_WhatsAppFlag proves the public document reports the *resolved*
// capability and never leaks a credential. The flag is what the dashboard uses
// to decide whether to offer the WhatsApp contact type at all.
func TestBuild_WhatsAppFlag(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Off by default.
	r.False(publicconfig.Build(&config.Config{}).WhatsApp.Enabled)

	// Kill switch on but no credentials → still off.
	cfg := &config.Config{}
	cfg.WhatsApp = config.WhatsAppConfig{Enabled: true}
	r.False(publicconfig.Build(cfg).WhatsApp.Enabled)

	cfg.WhatsApp = config.WhatsAppConfig{Enabled: true, AccessToken: "tok"}
	r.False(publicconfig.Build(cfg).WhatsApp.Enabled)

	// Fully configured → on.
	cfg.WhatsApp = config.WhatsAppConfig{
		Enabled:            true,
		AccessToken:        "super-secret-token",
		PhoneNumberID:      "555000111",
		WABAID:             "waba-1",
		AppSecret:          "super-secret-app-secret",
		WebhookVerifyToken: "verify-me",
		AlertTemplate:      "solidping_alert",
	}
	resp := publicconfig.Build(cfg)
	r.True(resp.WhatsApp.Enabled)

	// The serialized document must contain the boolean and nothing else about
	// WhatsApp — no token, secret, ids or template names.
	encoded, err := json.Marshal(resp)
	r.NoError(err)

	body := string(encoded)
	r.Contains(body, `"whatsapp":{"enabled":true}`)
	for _, secret := range []string{
		"super-secret-token", "super-secret-app-secret", "555000111",
		"waba-1", "verify-me", "solidping_alert",
	} {
		r.NotContains(body, secret)
	}
}

// TestBuild_TelegramFlag proves the public document reports the *resolved*
// Telegram enablement rule (kill switch AND token AND username), and that the
// bot username — which the browser genuinely needs to build a connect link — is
// the ONLY Telegram value that ever leaves the server.
func TestBuild_TelegramFlag(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Nothing configured at all.
	r.False(publicconfig.Build(&config.Config{}).Telegram.Enabled)

	cfg := &config.Config{}

	// Kill switch on, but no credentials: still off.
	cfg.Telegram = config.TelegramConfig{Enabled: true}
	r.False(publicconfig.Build(cfg).Telegram.Enabled)

	// Token without a username is half-configured — the dashboard could not
	// build a connect link, so the feature must report itself off.
	cfg.Telegram = config.TelegramConfig{Enabled: true, BotToken: "123:AAsuper-secret-bot-token"}
	r.False(publicconfig.Build(cfg).Telegram.Enabled)

	// Username without a token cannot send.
	cfg.Telegram = config.TelegramConfig{Enabled: true, BotUsername: "solidping_bot"}
	r.False(publicconfig.Build(cfg).Telegram.Enabled)

	cfg.Telegram = config.TelegramConfig{
		Enabled:       true,
		BotToken:      "123:AAsuper-secret-bot-token",
		BotUsername:   "@solidping_bot",
		WebhookSecret: "super-secret-webhook-secret",
	}

	resp := publicconfig.Build(cfg)
	r.True(resp.Telegram.Enabled)
	// Normalized for the t.me link: no leading '@'.
	r.Equal("solidping_bot", resp.Telegram.BotUsername)

	encoded, err := json.Marshal(resp)
	r.NoError(err)

	body := string(encoded)
	r.Contains(body, `"telegram":{"enabled":true,"botUsername":"solidping_bot"}`)

	for _, secret := range []string{"super-secret-bot-token", "super-secret-webhook-secret"} {
		r.NotContains(body, secret)
	}
}

// TestBuild_TelegramUsernameOmittedWhenDisabled proves an unconfigured instance
// emits nothing at all beyond the false flag — an operator must be able to see
// at a glance that the feature is wholly unwired.
func TestBuild_TelegramUsernameOmittedWhenDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.Config{}
	cfg.Telegram = config.TelegramConfig{Enabled: false, BotUsername: "solidping_bot"}

	encoded, err := json.Marshal(publicconfig.Build(cfg))
	r.NoError(err)

	r.Contains(string(encoded), `"telegram":{"enabled":false}`)
	r.NotContains(string(encoded), "solidping_bot")
}
