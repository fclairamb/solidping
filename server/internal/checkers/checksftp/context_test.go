package checksftp_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksftp"
)

// TestExecute_HonorsContextOnAStalledServer: a server that accepts the TCP
// connection and then never speaks used to hold the checker in the SSH
// handshake until the OS gave up, past the worker's deadline, so the worker
// abandoned it ("did not honor context"). Canceling the context must now cut
// the connection and return a timeout promptly.
func TestExecute_HonorsContextOnAStalledServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			t.Cleanup(func() { _ = conn.Close() }) // held open, silent
		}
	}()

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	r.NoError(err)

	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	cfg := &checksftp.SFTPConfig{
		Host:     host,
		Port:     port,
		Timeout:  time.Minute, // the context, not the config, must stop it
		Username: "tester",
		Password: "s3cret",
	}

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	done := make(chan *checkerdef.Result, 1)

	go func() {
		result, _ := (&checksftp.SFTPChecker{}).Execute(ctx, cfg)
		done <- result
	}()

	select {
	case result := <-done:
		r.NotNil(result)
		r.Equal(checkerdef.StatusTimeout, result.Status, "output: %#v", result.Output)
	case <-time.After(5 * time.Second):
		r.Fail("Execute ignored its context on a stalled SSH handshake")
	}
}
