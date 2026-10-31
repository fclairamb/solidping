package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegistrationMaxPendingPerIPLoad runs the whole Load: the limit is on by
// default only in SaaS mode, off in test run mode, and the variable wins.
func TestRegistrationMaxPendingPerIPLoad(t *testing.T) {
	tests := []struct {
		name           string
		deploymentMode string
		runMode        string
		env            string
		want           int
	}{
		{name: "self-hosted default is off", want: 0},
		{name: "SaaS default", deploymentMode: DeploymentModeSaaS, want: 10},
		{name: "off in test run mode, even in SaaS", deploymentMode: DeploymentModeSaaS, runMode: runModeTest, want: 0},
		{name: "self-hosted explicit value", env: "5", want: 5},
		{name: "SaaS explicit 0 turns it off", deploymentMode: DeploymentModeSaaS, env: "0", want: 0},
		{name: "explicit value wins in test run mode", runMode: runModeTest, env: "2", want: 2},
		{name: "garbage keeps the mode default", deploymentMode: DeploymentModeSaaS, env: "many", want: 10},
		{name: "negative keeps the mode default", env: "-1", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SP_DEPLOYMENT_MODE", tc.deploymentMode)
			t.Setenv("SP_RUNMODE", tc.runMode)
			t.Setenv(EnvRegistrationMaxPendingPerIP, tc.env)

			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.Auth.RegistrationMaxPendingPerIP)
		})
	}
}

// A config-file value is kept over the mode default, and loses to the variable.
func TestResolveRegistrationLimitFileValue(t *testing.T) {
	saas := func(limit int) *Config {
		return &Config{
			Deployment: DeploymentConfig{Mode: DeploymentModeSaaS},
			Auth:       AuthConfig{RegistrationMaxPendingPerIP: limit},
		}
	}

	t.Setenv(EnvRegistrationMaxPendingPerIP, "")

	cfg := saas(0)
	resolveRegistrationLimit(cfg, true)
	require.Equal(t, 0, cfg.Auth.RegistrationMaxPendingPerIP, "a file value of 0 turns the SaaS default off")

	cfg = saas(0)
	resolveRegistrationLimit(cfg, false)
	require.Equal(t, DefaultRegistrationMaxPendingPerIPSaaS, cfg.Auth.RegistrationMaxPendingPerIP,
		"positive control: without the file value, the SaaS default applies")

	t.Setenv(EnvRegistrationMaxPendingPerIP, "4")

	cfg = saas(7)
	resolveRegistrationLimit(cfg, true)
	require.Equal(t, 4, cfg.Auth.RegistrationMaxPendingPerIP)
}
