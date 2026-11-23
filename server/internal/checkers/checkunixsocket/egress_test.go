package checkunixsocket

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// countingListener counts the connections it accepts, so a test can prove a
// refused check never dialed.
func countingListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	accepted := &atomic.Int32{}
	path := listen(t, func(conn net.Conn) {
		accepted.Add(1)
		_ = conn.Close()
	})

	return path, accepted
}

func TestEnforcingGuardRefusesWithoutDialing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path, accepted := countingListener(t)

	// Positive control: the same config is up with no guard.
	r.Equal(checkerdef.StatusUp, run(t, t.Context(), map[string]any{"path": path}).Status)
	r.Eventually(func() bool { return accepted.Load() == 1 }, timeoutWait, tick)

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))
	result := run(t, ctx, map[string]any{"path": path})

	r.Equal(checkerdef.StatusError, result.Status)
	r.NotNil(rec.Denied(), "the refusal must be recorded")
	r.Contains(result.Output[checkerdef.OutputKeyError], "denied by egress policy")
	r.Never(func() bool { return accepted.Load() > 1 }, neverWait, tick, "a refused check must never dial")
}

func TestPermissiveGuardStillDials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path, _ := countingListener(t)

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(true)))
	r.Equal(checkerdef.StatusUp, run(t, ctx, map[string]any{"path": path}).Status)
	r.Nil(rec.Denied())
}

type refusingDialer struct{}

func (refusingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}

func TestTunnelIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path, accepted := countingListener(t)

	ctx := checkerdef.WithTunnelDialer(t.Context(), refusingDialer{})
	result := run(t, ctx, map[string]any{"path": path})

	r.Equal(checkerdef.StatusError, result.Status)
	r.Equal(ErrTunnelNotSupported.Error(), result.Output[checkerdef.OutputKeyError])
	r.Never(func() bool { return accepted.Load() > 0 }, neverWait, tick)
}
