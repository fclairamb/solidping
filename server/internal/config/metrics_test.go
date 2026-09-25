package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMetricsScrapeTokenEnvVarBinds proves SP_METRICS_SCRAPE_TOKEN reaches the
// struct through the real Load(). scrape_token is snake_case, so koanf's env
// loader can never reach it (SP_METRICS_SCRAPE_TOKEN would land on
// metrics.scrape.token, and there is no such struct path at all): without the
// manual reader the variable would parse and silently do nothing — the same
// koanf env quirk as SP_EGRESS_ALLOW_PRIVATE.
func TestMetricsScrapeTokenEnvVarBinds(t *testing.T) {
	r := require.New(t)

	t.Setenv(EnvMetricsScrapeToken, "  s3cr3t-token  ")

	cfg, err := Load()
	r.NoError(err)
	r.Equal("s3cr3t-token", cfg.Prometheus.ScrapeToken, "value must land trimmed")
}

// TestMetricsScrapeTokenEnvVarEmptyLeavesConfigured proves an absent env var
// leaves whatever config.yml / config.local.yml already set alone, rather than
// clobbering it with an empty value.
func TestMetricsScrapeTokenEnvVarEmptyLeavesConfigured(t *testing.T) {
	r := require.New(t)

	cfg := &Config{}
	cfg.Prometheus.ScrapeToken = "from-yaml"

	applyMetricsEnv(&cfg.Prometheus)
	r.Equal("from-yaml", cfg.Prometheus.ScrapeToken)
}

// TestMetricsScrapeTokenIsRecognized is the env-var table entry: the startup
// unrecognized-env check must not flag the documented switch as a typo.
func TestMetricsScrapeTokenIsRecognized(t *testing.T) {
	t.Parallel()

	require.Contains(t, RecognizedEnvVars(), EnvMetricsScrapeToken)
}
