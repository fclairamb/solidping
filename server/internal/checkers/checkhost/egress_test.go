package checkhost_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhost"
	"github.com/fclairamb/solidping/server/internal/egress"
)

func TestEnforcingGuardRefusesWithoutReading(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeSampler{}

	// Positive control: with no guard the sources are read.
	run(t, t.Context(), fake, map[string]any{})
	r.Equal(int32(3), fake.calls.Load())

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))
	result := run(t, ctx, fake, map[string]any{})

	r.Equal(checkerdef.StatusError, result.Status)
	r.NotNil(rec.Denied(), "the refusal must be recorded")
	r.Contains(result.Output[checkerdef.OutputKeyError], "denied by egress policy")
	r.Equal(int32(3), fake.calls.Load(), "a refused check must never read the host")
}

func TestPermissiveGuardStillRuns(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeSampler{}

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(true)))
	run(t, ctx, fake, map[string]any{})
	r.Nil(rec.Denied())
	r.Equal(int32(3), fake.calls.Load())
}

type refusingDialer struct{}

func (refusingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}

func TestTunnelIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeSampler{}

	ctx := checkerdef.WithTunnelDialer(t.Context(), refusingDialer{})
	result := run(t, ctx, fake, map[string]any{})

	r.Equal(checkerdef.StatusError, result.Status)
	r.Equal(checkhost.ErrTunnelNotSupported.Error(), result.Output[checkerdef.OutputKeyError])
	r.Zero(fake.calls.Load())
}
