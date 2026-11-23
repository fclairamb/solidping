package checksystemd_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksystemd"
	"github.com/fclairamb/solidping/server/internal/egress"
)

func TestEnforcingGuardRefusesWithoutRunning(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeRunner{out: showInactive}

	// Positive control: with no guard the runner is called.
	run(t, t.Context(), fake.run, map[string]any{"unit": "nginx"})
	r.Equal(1, fake.callCount())

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))
	result := run(t, ctx, fake.run, map[string]any{"unit": "nginx"})

	r.Equal(checkerdef.StatusError, result.Status)
	r.NotNil(rec.Denied(), "the refusal must be recorded")
	r.Contains(result.Output[checkerdef.OutputKeyError], "denied by egress policy")
	r.Equal(1, fake.callCount(), "a refused check must never run systemctl")
}

func TestPermissiveGuardStillRuns(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeRunner{out: showInactive}

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(true)))
	run(t, ctx, fake.run, map[string]any{"unit": "nginx"})
	r.Nil(rec.Denied())
	r.Equal(1, fake.callCount())
}

type refusingDialer struct{}

func (refusingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}

func TestTunnelIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeRunner{out: showInactive}

	ctx := checkerdef.WithTunnelDialer(t.Context(), refusingDialer{})
	result := run(t, ctx, fake.run, map[string]any{"unit": "nginx"})

	r.Equal(checkerdef.StatusError, result.Status)
	r.Equal(checksystemd.ErrTunnelNotSupported.Error(), result.Output[checkerdef.OutputKeyError])
	r.Zero(fake.callCount())
}
