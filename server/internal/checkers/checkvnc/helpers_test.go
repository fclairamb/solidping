package checkvnc

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// errFakeServer tags a fake server's own assertion failures.
var errFakeServer = errors.New("fake server")

// fakeServer runs script on the first connection accepted on a loopback
// listener. Failures inside the script are reported through errs so the
// test goroutine asserts them (require must not be called off it).
func fakeServer(t *testing.T, script func(conn net.Conn) error) (string, int, <-chan error) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	errs := make(chan error, 1)

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			errs <- acceptErr

			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		errs <- script(conn)
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	return host, port, errs
}

// runCheck executes the checker against host:port with cfg overrides.
func runCheck(t *testing.T, cfg *VNCConfig) *checkerdef.Result {
	t.Helper()

	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Second
	}

	result, err := (&VNCChecker{}).Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// serverErr waits for the fake server script to finish and returns its error.
func serverErr(t *testing.T, errs <-chan error) error {
	t.Helper()

	select {
	case err := <-errs:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("fake server did not finish")

		return nil
	}
}

// readN reads exactly n bytes.
func readN(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)

	return buf, err
}

// writeAll writes each chunk in order.
func writeAll(w io.Writer, chunks ...[]byte) error {
	for _, c := range chunks {
		if _, err := w.Write(c); err != nil {
			return err
		}
	}

	return nil
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// reasonBytes is a uint32-length-prefixed string.
func reasonBytes(s string) []byte { return append(u32(uint32(len(s))), s...) }

// serverInitBytes builds a ServerInit for a w x h desktop named name.
func serverInitBytes(w, h uint16, name string) []byte {
	out := append(u16(w), u16(h)...)
	out = append(out, make([]byte, 16)...) // pixel format (ignored by the client)

	return append(out, reasonBytes(name)...)
}

// fakeServerSeq runs scripts[i] on the i-th connection accepted, in order.
// It reports the first script error (or nil once all ran) through errs.
func fakeServerSeq(t *testing.T, scripts ...func(conn net.Conn) error) (string, int, <-chan error) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	errs := make(chan error, 1)

	go func() {
		for _, script := range scripts {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				errs <- acceptErr

				return
			}

			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			scriptErr := script(conn)
			_ = conn.Close()

			if scriptErr != nil {
				errs <- scriptErr

				return
			}
		}

		errs <- nil
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	return host, port, errs
}

// rfbGreeting writes the 3.8 banner, reads the client's and offers types.
func rfbGreeting(conn net.Conn, types ...byte) error {
	if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
		return err
	}

	if _, err := readN(conn, bannerLen); err != nil {
		return err
	}

	return writeAll(conn, []byte{byte(len(types))}, types)
}
