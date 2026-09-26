package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHeadersCSPExtraSourcesEnvVarBinds proves SP_HEADERS_CSP_EXTRA_SOURCES
// reaches the struct through the real Load(): csp_extra_sources is
// snake_case, so koanf's env loader alone would land it on
// headers.csp.extra.sources and bind nothing.
func TestHeadersCSPExtraSourcesEnvVarBinds(t *testing.T) {
	r := require.New(t)

	t.Setenv(EnvHeadersCSPExtraSources, "  img-src https://cdn.acme.com  ")

	cfg, err := Load()
	r.NoError(err)
	r.Equal("img-src https://cdn.acme.com", cfg.Headers.CSPExtraSources)
}

// TestHeadersCSPExtraSourcesIsRecognized keeps the startup unrecognized-env
// check from flagging the documented switch as a typo.
func TestHeadersCSPExtraSourcesIsRecognized(t *testing.T) {
	t.Parallel()

	require.Contains(t, RecognizedEnvVars(), EnvHeadersCSPExtraSources)
}
