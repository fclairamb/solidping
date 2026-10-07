package checkssh

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestSSHTypeValidateSamples(t *testing.T) {
	t.Parallel()

	c := &SSHChecker{}
	require.Equal(t, checkerdef.CheckTypeSSH, c.Type())
	require.NotEmpty(t, c.GetSampleConfigs(&checkerdef.ListSampleOptions{}))

	require.Error(t, c.Validate(&checkerdef.CheckSpec{
		Name: "s", Slug: "s", Period: time.Minute, Config: (&SSHConfig{}).GetConfig(),
	}))
	require.NoError(t, c.Validate(&checkerdef.CheckSpec{
		Name: "s", Slug: "s", Period: time.Minute, Config: (&SSHConfig{Host: "h"}).GetConfig(),
	}))
}

var errPlain = errors.New("plain")

func TestIsExitError(t *testing.T) {
	t.Parallel()

	var target *ssh.ExitError

	require.False(t, isExitError(errPlain, &target))
	require.True(t, isExitError(&ssh.ExitError{}, &target))
	require.NotNil(t, target)
}

func TestExecuteWithAuthInvalidPrivateKey(t *testing.T) {
	t.Parallel()

	output := map[string]any{}
	res := (&SSHChecker{}).executeWithAuth(context.Background(), "127.0.0.1:1",
		&SSHConfig{Host: "127.0.0.1", Username: "u", PrivateKey: "not a key"}, time.Second, output)

	require.Equal(t, checkerdef.StatusError, res.Status)
	require.Contains(t, res.Output[checkerdef.OutputKeyError], "invalid private key")
}

func TestExecuteWithAuthConnectFailures(t *testing.T) {
	t.Parallel()

	// Refused connection.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	res := (&SSHChecker{}).executeWithAuth(context.Background(), addr,
		&SSHConfig{Username: "u", Password: "p", ExpectedFingerprint: "SHA256:abc"}, time.Second, map[string]any{})
	require.Equal(t, checkerdef.StatusDown, res.Status)

	// Canceled context is reported as a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res = (&SSHChecker{}).executeWithAuth(ctx, addr,
		&SSHConfig{Username: "u", Password: "p"}, time.Second, map[string]any{})
	require.Equal(t, checkerdef.StatusTimeout, res.Status)
}
