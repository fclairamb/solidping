package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// SP_TOOLS_ENABLED binds through koanf, SP_TOOLS_DENIED_HOSTS through the
// manual reader (denied_hosts is snake_case).
func TestToolsEnvVarsBind(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_TOOLS_ENABLED", "true")
	t.Setenv(EnvToolsDeniedHosts, "status.acme.com, API.acme.com")

	cfg, err := Load()
	r.NoError(err)
	r.True(cfg.Tools.Enabled)
	r.Contains(cfg.ToolsDeniedHosts(), "status.acme.com")
	r.Contains(cfg.ToolsDeniedHosts(), "api.acme.com")
	r.Contains(RecognizedEnvVars(), "SP_TOOLS_ENABLED")
	r.Contains(RecognizedEnvVars(), EnvToolsDeniedHosts)
}

func TestToolsDeniedHostsIncludeTheInstanceOwnHosts(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.Server.BaseURL = "https://SolidPing.acme.com:8443/"
	cfg.Server.DocsHost = "docs.acme.com"
	cfg.Tools.DeniedHosts = []string{" ", "Extra.acme.com"}

	require.Equal(t, []string{"solidping.acme.com", "docs.acme.com", "extra.acme.com"}, cfg.ToolsDeniedHosts())
}
