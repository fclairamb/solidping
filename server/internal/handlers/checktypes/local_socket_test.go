package checktypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
)

// socketFlags returns the two Unix-socket capability flags of checkType as
// served by the authenticated listing (spec 2026-11-24-01).
func socketFlags(t *testing.T, cfg *config.CheckersConfig, mode, checkType string) (bool, bool) {
	t.Helper()

	svc := NewService(checkerdef.NewActivationResolver(cfg, mode), "https://example.com")

	for _, entry := range svc.ListServerCheckTypes().Data {
		if entry.Type == checkType {
			return entry.SupportsLocalSocket, entry.LocalSocketOnServer
		}
	}

	require.Failf(t, "type not listed", "%s", checkType)

	return false, false
}

func TestCheckTypesLocalSocketFlags(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	allow := &config.CheckersConfig{UnixSocketPaths: []string{"/run/app/"}}

	supports, onServer := socketFlags(t, allow, config.DeploymentModeSelfHosted, "tcp")
	r.True(supports)
	r.True(onServer, "an allowlist on self-hosted")

	supports, onServer = socketFlags(t, allow, config.DeploymentModeSelfHosted, "icmp")
	r.False(supports)
	r.False(onServer)

	_, onServer = socketFlags(t, &config.CheckersConfig{}, config.DeploymentModeSelfHosted, "tcp")
	r.False(onServer, "an empty allowlist")

	_, onServer = socketFlags(t, allow, config.DeploymentModeSaaS, "tcp")
	r.False(onServer, "SaaS shared regions never dial a socket")
}
