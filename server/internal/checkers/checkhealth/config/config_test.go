package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth/config"
)

func validate(raw map[string]any) (*checkerdef.CheckSpec, error) {
	spec := &checkerdef.CheckSpec{Config: raw}

	return spec, config.ValidateSpec(spec)
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"url":     "https://app.acme.com/health",
		"method":  "GET",
		"headers": map[string]any{"Accept": "application/json"},
		"secretHeaders": map[string]any{
			"oh-dear-health-check-secret": "s3cret",
		},
		"verifySsl":  false,
		"format":     "spring",
		"maxAge":     "30m",
		"ignore":     []any{"Cache", "Queue"},
		"components": map[string]any{"Mail": map[string]any{"onFailed": "warning"}},
	}

	cfg := &config.HealthConfig{}
	require.NoError(t, cfg.FromMap(input))
	require.Equal(t, "https://app.acme.com/health", cfg.URL)
	require.Equal(t, "s3cret", cfg.SecretHeaders["oh-dear-health-check-secret"])
	require.Equal(t, "spring", cfg.Format)
	require.Equal(t, 30*time.Minute, cfg.EffectiveMaxAge())
	require.True(t, cfg.IsIgnored("Cache"))
	require.Equal(t, config.OnFailedWarning, cfg.OnFailed("Mail"))
	require.Equal(t, config.OnFailedDown, cfg.OnFailed("Database"))

	again := &config.HealthConfig{}
	require.NoError(t, again.FromMap(cfg.GetConfig()))
	require.Equal(t, cfg, again)
	require.Equal(t, cfg.GetConfig(), again.GetConfig())
}

func TestDefaults(t *testing.T) {
	t.Parallel()

	cfg := &config.HealthConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": "https://app.acme.com/health"}))
	require.Equal(t, "auto", cfg.EffectiveFormat())
	require.Equal(t, 10*time.Minute, cfg.EffectiveMaxAge())
	require.Equal(t, map[string]any{"url": "https://app.acme.com/health"}, cfg.GetConfig())

	cfg.MaxAge = "0"
	require.Zero(t, cfg.EffectiveMaxAge(), "0 disables the staleness rule")
}

func TestSnakeCaseAliases(t *testing.T) {
	t.Parallel()

	cfg := &config.HealthConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{
		"url":        "https://app.acme.com/health",
		"max_age":    "5m",
		"components": map[string]any{"A": map[string]any{"on_failed": "warning"}},
	}))
	require.Equal(t, 5*time.Minute, cfg.EffectiveMaxAge())
	require.Equal(t, config.OnFailedWarning, cfg.OnFailed("A"))
}

func TestValidateAcceptsAndFillsDefaults(t *testing.T) {
	t.Parallel()

	spec, err := validate(map[string]any{"url": "https://app.acme.com/health"})
	require.NoError(t, err)
	require.Equal(t, "Health: app.acme.com", spec.Name)
	require.Equal(t, "health-app-acme-com", spec.Slug)
}

func TestValidateRefusals(t *testing.T) {
	t.Parallel()

	base := func(extra map[string]any) map[string]any {
		out := map[string]any{"url": "https://app.acme.com/health"}
		for k, v := range extra {
			out[k] = v
		}

		return out
	}

	cases := map[string]map[string]any{
		"body_expect":           {"body_expect": "ok"},
		"bodyExpect":            {"bodyExpect": "ok"},
		"body_pattern":          {"body_pattern": "ok"},
		"json_path_assertions":  {"json_path_assertions": map[string]any{"type": "assertion", "path": "$.a", "operator": "exists"}},
		"bodyAssertions":        {"bodyAssertions": map[string]any{"type": "assertion", "operator": "eq", "value": "ok"}},
		"expected_status":       {"expected_status": 200},
		"expected_status_codes": {"expected_status_codes": []any{"2XX"}},
		"unknown format":        {"format": "xml"},
		"invalid on_failed":     {"components": map[string]any{"A": map[string]any{"onFailed": "maybe"}}},
		"bad max age":           {"maxAge": "soon"},
		"negative max age":      {"maxAge": "-5m"},
		"empty ignore name":     {"ignore": []any{""}},
		"no url":                {"url": ""},
		"bad url scheme":        {"url": "ftp://app.acme.com"},
	}

	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := validate(base(extra))
			require.Error(t, err)

			var configErr *checkerdef.ConfigError
			require.ErrorAs(t, err, &configErr)
		})
	}
}

func TestValidateTooManyIgnored(t *testing.T) {
	t.Parallel()

	names := make([]any, config.MaxIgnored+1)
	for i := range names {
		names[i] = "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}

	_, err := validate(map[string]any{"url": "https://app.acme.com/health", "ignore": names})
	require.Error(t, err)

	_, err = validate(map[string]any{"url": "https://app.acme.com/health", "ignore": names[:config.MaxIgnored]})
	require.NoError(t, err)
}

func TestHTTPRulesStillApply(t *testing.T) {
	t.Parallel()

	_, err := validate(map[string]any{"url": "https://app.acme.com/health", "method": "TELEPORT"})
	require.Error(t, err)

	_, err = validate(map[string]any{"url": "https://app.acme.com/health", "redirectHostPolicy": "sometimes"})
	require.Error(t, err)
}

func TestSecretFieldsAreTheHTTPOnes(t *testing.T) {
	t.Parallel()

	cfg := &config.HealthConfig{}
	require.ElementsMatch(t, []string{"basicAuth", "password", "secretHeaders"}, cfg.SecretFields())

	normalized, err := cfg.NormalizeConfig(map[string]any{"url": "u", "username": "bob", "password": "pw"})
	require.NoError(t, err)
	require.Equal(t, "bob:pw", normalized["basicAuth"])
	require.NotContains(t, normalized, "password")
}
