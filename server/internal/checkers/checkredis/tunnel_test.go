package checkredis_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkredis"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606. Only
// the bastion's forwarding table can make sense of it, so the direct-tcpip
// request carrying it verbatim PROVES the checker routed its dial through the
// tunnel and skipped local resolution.
const (
	tunnelHost   = "redis.private.invalid"
	tunnelTarget = "redis.private.invalid:6379"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered: the bastion refuses to connect the target, so the
	// dial fails AT the bastion. The direct-tcpip request still carries the
	// verbatim host:port, which is the proof we want — end-to-end byte flow
	// through the tunnel is already covered by checktcp/checkhttp.
	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	checker := &checkredis.RedisChecker{}
	config := &checkredis.RedisConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(6379),
		"timeout": "2s",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)

	r.Contains(srv.Requested(), tunnelTarget)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}

// Untunneled, a `.invalid` host must still fail locally — proving the tunnel
// branch is what routes the dial remotely, not a blanket change.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkredis.RedisChecker{}
	config := &checkredis.RedisConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(6379),
		"timeout": "2s",
	}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}
