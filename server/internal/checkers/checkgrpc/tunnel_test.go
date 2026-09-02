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

// The eager, instrumented connect must not disturb the tunneled path: local
// resolution stays skipped (so NO dns_time_ms is recorded, unlike the direct
// path), the failure is attributed to the connect phase, and no reachability
// marker is minted — a trace run from this worker would describe a route the
// probe never took, since the dial happened on the far side of the bastion.
func TestExecuteThroughTunnelRecordsNoDNSPhaseAndNoNetworkFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)

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
	r.Equal(true, result.Output["tunneled"])
	r.Equal("connect", result.Output["phase"])
	r.NotContains(result.Metrics, "dns_time_ms")

	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.NetworkFailure)
	}
}
