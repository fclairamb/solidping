package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistrationMaxPendingPerIPDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.Equal(3, cfg.Auth.RegistrationMaxPendingPerIP)
}

func TestApplyRegistrationLimitEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		runMode string
		want    int
	}{
		{name: "unset keeps the default", env: "", want: DefaultRegistrationMaxPendingPerIP},
		{name: "explicit value", env: "10", want: 10},
		{name: "0 turns it off", env: "0", want: 0},
		{name: "garbage is ignored", env: "many", want: DefaultRegistrationMaxPendingPerIP},
		{name: "negative is ignored", env: "-1", want: DefaultRegistrationMaxPendingPerIP},
		{name: "off in test run mode", env: "", runMode: runModeTest, want: 0},
		{name: "explicit value wins in test run mode", env: "2", runMode: runModeTest, want: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvRegistrationMaxPendingPerIP, tc.env)

			cfg := AuthConfig{RegistrationMaxPendingPerIP: DefaultRegistrationMaxPendingPerIP}
			applyRegistrationLimitEnv(&cfg, tc.runMode)
			require.Equal(t, tc.want, cfg.RegistrationMaxPendingPerIP)
		})
	}
}
