package checkdocker

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// createClient routes the docker client's dial through the egress guard on the
// context (spec 2026-10-16-09): a top-level docker check pointed at a private
// tcp:// target is refused with a recorded denial, and a local socket is
// refused outright. The permissive guard is the positive control.
func TestCreateClientHonorsTheEgressGuard(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	tcpCfg := &DockerConfig{Host: "tcp://" + ln.Addr().String(), ContainerName: "web"}
	unixCfg := &DockerConfig{Host: "unix:///should-never-be-dialed-2026-10-16-09.sock", ContainerName: "web"}

	t.Run("enforcing guard refuses a loopback tcp host", func(t *testing.T) {
		t.Parallel()

		ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))

		result, err := (&DockerChecker{}).Execute(ctx, tcpCfg)
		require.NoError(t, err)
		require.NotNil(t, rec.Denied(), "the dial must be refused by the guard, got %v", result.Output)
	})

	t.Run("enforcing guard refuses a unix socket", func(t *testing.T) {
		t.Parallel()

		ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))

		cli, err := createClient(ctx, unixCfg)
		require.Nil(t, cli)
		require.ErrorIs(t, err, egress.ErrDenied)
		require.NotNil(t, rec.Denied())
	})

	t.Run("permissive guard keeps the client's own transport", func(t *testing.T) {
		t.Parallel()

		ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(true)))

		for _, cfg := range []*DockerConfig{tcpCfg, unixCfg} {
			cli, err := createClient(ctx, cfg)
			require.NoError(t, err)
			require.NotNil(t, cli)
			_ = cli.Close()
		}

		result, err := (&DockerChecker{}).Execute(ctx, tcpCfg)
		require.NoError(t, err)
		require.Nil(t, rec.Denied(), "a permissive guard must not refuse, got %v", result.Output)
	})
}
