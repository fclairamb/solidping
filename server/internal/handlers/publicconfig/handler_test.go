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
