package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEgressAllowPrivateEnvVarBinds proves SP_EGRESS_ALLOW_PRIVATE reaches the
// struct. egress.allow_private_targets is snake_case, so koanf's env loader
// can never reach it (SP_EGRESS_ALLOW_PRIVATE would land on
// egress.allow.private): without the manual reader the variable would parse
// and silently do nothing — the koanf env quirk.
func TestEgressAllowPrivateEnvVarBinds(t *testing.T) {
	r := require.New(t)

	t.Setenv(EnvEgressAllowPrivate, "true")
	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSaaS)

	cfg, err := Load()
	r.NoError(err)
	r.NotNil(cfg.Egress.AllowPrivateTargets)
	r.True(*cfg.Egress.AllowPrivateTargets)
	r.True(cfg.EgressAllowsPrivateTargets(), "an explicit true opens even a SaaS worker")

	t.Setenv(EnvEgressAllowPrivate, "false")
	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSelfHosted)

	cfg, err = Load()
	r.NoError(err)
	r.False(cfg.EgressAllowsPrivateTargets(), "an explicit false closes even a self-hosted worker")
}

// An empty or garbage value is "I did not choose": the derived default stays.
func TestEgressAllowPrivateEmptyOrInvalidKeepsTheDefault(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSaaS)

	for _, raw := range []string{"", "  ", "maybe"} {
		t.Setenv(EnvEgressAllowPrivate, raw)

		cfg, err := Load()
		r.NoError(err)
		r.Nil(cfg.Egress.AllowPrivateTargets, "%q must leave the switch unset", raw)
		r.False(cfg.EgressAllowsPrivateTargets(), "%q must not open a SaaS worker", raw)
	}
}

// TestEgressAllowPrivateIsRecognized is the env-var table entry: the startup
// unrecognized-env check must not flag the documented switch as a typo.
func TestEgressAllowPrivateIsRecognized(t *testing.T) {
	t.Parallel()

	require.Contains(t, RecognizedEnvVars(), "SP_EGRESS_ALLOW_PRIVATE")
}

// TestEgressDefaultsByPlacement pins the placement rule: the process that
// dials decides. A SaaS shared worker denies, a deported agent (private
// location) allows, self-hosted allows, and an explicit value always wins.
func TestEgressDefaultsByPlacement(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mode     string
		role     string
		explicit *bool
		allow    bool
	}{
		{name: "saas shared worker", mode: DeploymentModeSaaS, role: NodeRoleChecks, allow: false},
		{name: "saas all-in-one", mode: DeploymentModeSaaS, role: NodeRoleAll, allow: false},
		{name: "saas api+checks", mode: DeploymentModeSaaS, role: "api,checks", allow: false},
		{name: "saas private agent", mode: DeploymentModeSaaS, role: NodeRoleAgent, allow: true},
		{name: "self-hosted worker", mode: DeploymentModeSelfHosted, role: NodeRoleChecks, allow: true},
		{name: "self-hosted all", mode: DeploymentModeSelfHosted, role: NodeRoleAll, allow: true},
		{name: "unset mode", mode: "", role: NodeRoleAll, allow: true},
		{name: "self-hosted agent", mode: DeploymentModeSelfHosted, role: NodeRoleAgent, allow: true},
		{
			name: "agent explicitly closed", mode: DeploymentModeSaaS, role: NodeRoleAgent,
			explicit: BoolPtr(false), allow: false,
		},
		{
			name: "saas worker explicitly opened", mode: DeploymentModeSaaS, role: NodeRoleChecks,
			explicit: BoolPtr(true), allow: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{}
			cfg.Deployment.Mode = tc.mode
			cfg.Node.Role = tc.role
			cfg.Egress.AllowPrivateTargets = tc.explicit

			require.Equal(t, tc.allow, cfg.EgressAllowsPrivateTargets())
			require.NotEmpty(t, cfg.EgressPolicySource())
		})
	}
}
