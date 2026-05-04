package checkssl

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSamples_RoundTrip(t *testing.T) {
	t.Parallel()

	checker := &SSLChecker{}

	for _, spec := range checker.GetSampleConfigs(nil) {
		t.Run(spec.Slug, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &SSLConfig{}
			r.NoError(cfg.FromMap(spec.Config))
			r.NoError(cfg.Validate())

			again := &SSLConfig{}
			r.NoError(again.FromMap(cfg.GetConfig()))
			r.Equal(cfg.Host, again.Host)
			r.Equal(cfg.Port, again.Port)
			r.Equal(cfg.ThresholdDays, again.ThresholdDays)
			r.Equal(cfg.Timeout, again.Timeout)
			r.Equal(cfg.ServerName, again.ServerName)
		})
	}
}

func TestFromMap_AcceptsCamelCaseAndSnakeCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	camel := map[string]any{
		"host":          "example.com",
		"port":          8443,
		"thresholdDays": 14,
		"serverName":    "alt.example.com",
		"timeout":       "5s",
	}

	cfg := &SSLConfig{}
	r.NoError(cfg.FromMap(camel))
	r.Equal("example.com", cfg.Host)
	r.Equal(8443, cfg.Port)
	r.Equal(14, cfg.ThresholdDays)
	r.Equal("alt.example.com", cfg.ServerName)

	snake := map[string]any{
		"host":           "legacy.example.com",
		"threshold_days": 7,
		"server_name":    "snake.example.com",
	}

	legacyCfg := &SSLConfig{}
	r.NoError(legacyCfg.FromMap(snake))
	r.Equal(7, legacyCfg.ThresholdDays)
	r.Equal("snake.example.com", legacyCfg.ServerName)
}
