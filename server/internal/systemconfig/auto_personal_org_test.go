package systemconfig

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestKnownAutoPersonalOrgKey pins the auth.auto_personal_org instance setting
// (spec 2026-10-30-04): same env var as the config reader, and an ApplyFunc
// that only sets a recognizable boolean, so a typo keeps the mode default.
func TestKnownAutoPersonalOrgKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var def ParameterDefinition

	found := false

	for _, d := range getKnownParameters() {
		if d.Key == KeyAutoPersonalOrg {
			def, found = d, true

			break
		}
	}

	r.True(found, "auth.auto_personal_org must be a known parameter")
	r.False(def.Secret)
	r.Equal(config.EnvAuthAutoPersonalOrg, def.EnvVar)

	for _, tc := range []struct {
		value any
		want  *bool
	}{
		{value: true, want: config.BoolPtr(true)},
		{value: "false", want: config.BoolPtr(false)},
		{value: "", want: nil},
		{value: "perhaps", want: nil},
		{value: nil, want: nil},
	} {
		cfg := &config.Config{}
		def.ApplyFunc(cfg, tc.value)
		r.Equal(tc.want, cfg.Auth.AutoPersonalOrg, "value %#v", tc.value)
	}

	cfg := &config.Config{}
	cfg.Deployment.Mode = config.DeploymentModeSelfHosted
	r.False(cfg.AutoPersonalOrgEnabled())
	def.ApplyFunc(cfg, true)
	r.True(cfg.AutoPersonalOrgEnabled(), "the parameter opts a self-hosted instance in")
}
