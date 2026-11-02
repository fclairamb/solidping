package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAutoPersonalOrgDefaultsByMode pins spec 2026-10-30-04: a SaaS instance
// creates a personal org for an org-less user, a self-hosted one does not.
func TestAutoPersonalOrgDefaultsByMode(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSaaS)

	cfg, err := Load()
	r.NoError(err)
	r.Nil(cfg.Auth.AutoPersonalOrg)
	r.True(cfg.AutoPersonalOrgEnabled(), "on by default under saas")

	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSelfHosted)

	cfg, err = Load()
	r.NoError(err)
	r.False(cfg.AutoPersonalOrgEnabled(), "off by default under self-hosted")
}

// TestAutoPersonalOrgEnvOverride: SP_AUTH_AUTO_PERSONAL_ORG wins over the mode
// default both ways, and a garbage value keeps the default. The key is
// snake_case, so koanf alone would never bind it.
func TestAutoPersonalOrgEnvOverride(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSelfHosted)
	t.Setenv(EnvAuthAutoPersonalOrg, "true")

	cfg, err := Load()
	r.NoError(err)
	r.True(cfg.AutoPersonalOrgEnabled(), "opt-in on self-hosted")

	t.Setenv("SP_DEPLOYMENT_MODE", DeploymentModeSaaS)
	t.Setenv(EnvAuthAutoPersonalOrg, "false")

	cfg, err = Load()
	r.NoError(err)
	r.False(cfg.AutoPersonalOrgEnabled(), "opt-out on saas")

	t.Setenv(EnvAuthAutoPersonalOrg, "maybe")

	cfg, err = Load()
	r.NoError(err)
	r.Nil(cfg.Auth.AutoPersonalOrg)
	r.True(cfg.AutoPersonalOrgEnabled(), "an unparseable value keeps the saas default")

	r.Contains(RecognizedEnvVars(), EnvAuthAutoPersonalOrg)
}
