package systemconfig

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// TestKnownEgressAllowPrivateTargetsKey pins the system parameter behind the
// egress policy: same key as the config path, same env var as the config
// reader, and an ApplyFunc that only ever sets a recognizable boolean — an
// unparseable value must never open a SaaS worker nor close an agent.
func TestKnownEgressAllowPrivateTargetsKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(egress.ParamAllowPrivate, string(KeyEgressAllowPrivateTargets))

	var def ParameterDefinition

	found := false

	for _, d := range getKnownParameters() {
		if d.Key == KeyEgressAllowPrivateTargets {
			def, found = d, true

			break
		}
	}

	r.True(found, "egress.allow_private_targets must be a known parameter")
	r.False(def.Secret)
	r.Equal(egress.EnvAllowPrivate, def.EnvVar)
	r.Equal(config.EnvEgressAllowPrivate, def.EnvVar)
	r.Contains(KnownEnvVars(), "SP_EGRESS_ALLOW_PRIVATE")

	for _, tc := range []struct {
		value any
		want  *bool
	}{
		{value: true, want: config.BoolPtr(true)},
		{value: false, want: config.BoolPtr(false)},
		{value: "true", want: config.BoolPtr(true)},
		{value: "0", want: config.BoolPtr(false)},
		{value: "no", want: config.BoolPtr(false)},
		{value: "", want: nil},
		{value: "perhaps", want: nil},
		{value: 1.0, want: nil},
		{value: nil, want: nil},
	} {
		cfg := &config.Config{}
		def.ApplyFunc(cfg, tc.value)
		r.Equal(tc.want, cfg.Egress.AllowPrivateTargets, "value %#v", tc.value)
	}

	// The parameter drives the effective policy of a SaaS worker.
	cfg := &config.Config{}
	cfg.Deployment.Mode = config.DeploymentModeSaaS
	r.False(cfg.EgressAllowsPrivateTargets())
	def.ApplyFunc(cfg, true)
	r.True(cfg.EgressAllowsPrivateTargets())
}
