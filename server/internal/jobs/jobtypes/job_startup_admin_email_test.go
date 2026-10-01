package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/defaults"
)

// TestSeedAdminEmail: SP_ADMIN_EMAIL (auth.admin_email) replaces the seeded
// super admin's address, normalized; unset keeps admin@solidping.io
// (spec 2026-09-30-08).
func TestSeedAdminEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"no config", nil, defaults.Email},
		{"unset", &config.Config{}, defaults.Email},
		{"blank", &config.Config{Auth: config.AuthConfig{AdminEmail: "  "}}, defaults.Email},
		{"set", &config.Config{Auth: config.AuthConfig{AdminEmail: " Admin@Acme.com "}}, "admin@acme.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, seedAdminEmail(tt.cfg))
		})
	}
}
