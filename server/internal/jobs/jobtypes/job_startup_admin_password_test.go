package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestSeedAdminPassword: SP_ADMIN_PASSWORD wins; the test and demo run modes
// keep defaults.Password; every other mode, and no config, generates one
// (spec 2026-11-19-01).
func TestSeedAdminPassword(t *testing.T) {
	t.Parallel()

	configured := func(runMode string) *config.Config {
		return &config.Config{RunMode: runMode, Auth: config.AuthConfig{AdminPassword: "acme-pass"}}
	}

	tests := []struct {
		name       string
		cfg        *config.Config
		wantSource seedPasswordSource
		want       string
	}{
		{"configured in production", configured(""), seedPasswordConfigured, "acme-pass"},
		{"configured in test mode", configured(runModeTest), seedPasswordConfigured, "acme-pass"},
		{"test mode", &config.Config{RunMode: runModeTest}, seedPasswordDefault, defaults.Password},
		{"demo mode", &config.Config{RunMode: runModeDemo}, seedPasswordDefault, defaults.Password},
		{"production", &config.Config{}, seedPasswordGenerated, ""},
		{"unknown mode", &config.Config{RunMode: "staging"}, seedPasswordGenerated, ""},
		{"no config", nil, seedPasswordGenerated, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, source, err := seedAdminPassword(tt.cfg)
			r.NoError(err)
			r.Equal(tt.wantSource, source)

			if tt.wantSource != seedPasswordGenerated {
				r.Equal(tt.want, got)

				return
			}

			r.Len(got, passwords.GeneratedLength)
			r.NotEqual(defaults.Password, got)

			again, _, err := seedAdminPassword(tt.cfg)
			r.NoError(err)
			r.NotEqual(got, again, "each install draws its own password")
		})
	}
}
