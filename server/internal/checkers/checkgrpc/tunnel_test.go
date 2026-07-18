package checkgrpc_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606. gRPC
// would normally resolve it with its `dns` resolver before dialing, so the
// bastion recording it verbatim PROVES the checker switched to `passthrough` and
// routed the dial through the tunnel with no local resolution.
const (
	tunnelHost   = "grpc.private.invalid"
	tunnelTarget = "grpc.private.invalid:50051"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered: the bastion refuses the target; the verbatim
	// host:port still crossed the tunnel, which is what we assert.
	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	checker := &checkgrpc.GRPCChecker{}
	config := &checkgrpc.GRPCConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(50051),
		"timeout": "2s",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)

	r.Contains(srv.Requested(), tunnelTarget)
	r.NoError(dialer.TunnelFailure())
	r.NotEqual(checkerdef.StatusUp, result.Status)
}

// Untunneled, a `.invalid` host must still fail locally.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkgrpc.GRPCChecker{}
	config := &checkgrpc.GRPCConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(50051),
		"timeout": "2s",
	}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}
