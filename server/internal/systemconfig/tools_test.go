package systemconfig

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// The public tools endpoint is off unless tools.enabled (or SP_TOOLS_ENABLED)
// turns it on.
func TestKnownToolsEnabledKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var def ParameterDefinition

	found := false

	for _, d := range getKnownParameters() {
		if d.Key == KeyToolsEnabled {
			def, found = d, true

			break
		}
	}

	r.True(found, "tools.enabled must be a known parameter")
	r.Equal("tools.enabled", string(def.Key))
	r.Equal("SP_TOOLS_ENABLED", def.EnvVar)
	r.False(def.Secret)

	cfg := &config.Config{}
	r.False(cfg.Tools.Enabled, "off by default")

	def.ApplyFunc(cfg, "true")
	r.True(cfg.Tools.Enabled)

	def.ApplyFunc(cfg, false)
	r.False(cfg.Tools.Enabled)
}
