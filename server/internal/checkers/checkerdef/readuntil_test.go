package checkerdef_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// readUntilPipe gives the test one end of an in-memory connection and a
// goroutine-free way to stuff bytes into the other.
func readUntilPipe(t *testing.T) (client, server net.Conn) {
	t.Helper()

	client, server = net.Pipe()

	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	return client, server
}

func TestReadUntilMatches(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client, server := readUntilPipe(t)

	go func() {
		_, _ = server.Write([]byte("+PO"))
		_, _ = server.Write([]byte("NG\r\nleftover"))
	}()

	res := checkerdef.ReadUntil(client, time.Now().Add(2*time.Second), func(buf []byte) bool {
		return bytes.Contains(buf, []byte("\r\n"))
	}, 4096)

	r.Equal(checkerdef.ReadUntilMatched, res.Outcome)
	r.NoError(res.Err)
	r.True(bytes.HasPrefix(res.Data, []byte("+PONG\r\n")))
	r.Equal(2, res.Reads)
}

// A match that never arrives must stop at the limit — with the FULL byte count
// reported even though the buffer itself is capped.
func TestReadUntilCaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client, server := readUntilPipe(t)

	go func() {
		for i := 0; i < 8; i++ {
			if _, err := server.Write(bytes.Repeat([]byte("x"), 64)); err != nil {
				return
			}
		}
	}()

	res := checkerdef.ReadUntil(client, time.Now().Add(2*time.Second), func(buf []byte) bool {
		return bytes.Contains(buf, []byte("never"))
	}, 128)

	r.Equal(checkerdef.ReadUntilCapped, res.Outcome)
	r.Len(res.Data, 128)
	r.GreaterOrEqual(res.Received, 128)
}

// With no match function the loop performs exactly ONE read — the
// `send_data`-with-no-expectation diagnostic read, and a bare JS `c.read()`.
func TestReadUntilChunkReadsOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client, server := readUntilPipe(t)

	go func() {
		_, _ = server.Write([]byte("first"))
		_, _ = server.Write([]byte("second"))
	}()

	res := checkerdef.ReadUntil(client, time.Now().Add(2*time.Second), nil, 4096)

	r.Equal(checkerdef.ReadUntilChunk, res.Outcome)
	r.Equal(1, res.Reads)
	r.Equal("first", string(res.Data))
}

// A peer that closes mid-conversation stops the loop with io.EOF and the
// partial buffer intact — that partial is what a caller reports.
func TestReadUntilStopsOnEOF(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client, server := readUntilPipe(t)

	go func() {
		_, _ = server.Write([]byte("partial"))
		_ = server.Close()
	}()

	res := checkerdef.ReadUntil(client, time.Now().Add(2*time.Second), func(buf []byte) bool {
		return bytes.Contains(buf, []byte("complete"))
	}, 4096)

	r.Equal(checkerdef.ReadUntilStopped, res.Outcome)
	r.ErrorIs(res.Err, io.EOF)
	r.Equal("partial", string(res.Data))
}

// A deadline that fires stops the loop with a net.Error timeout, again keeping
// what was accumulated before it.
func TestReadUntilStopsOnDeadline(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client, server := readUntilPipe(t)

	go func() {
		_, _ = server.Write([]byte("hello"))
	}()

	res := checkerdef.ReadUntil(client, time.Now().Add(150*time.Millisecond), func(buf []byte) bool {
		return bytes.Contains(buf, []byte("goodbye"))
	}, 4096)

	r.Equal(checkerdef.ReadUntilStopped, res.Outcome)
	r.Error(res.Err)

	var netErr net.Error

	r.ErrorAs(res.Err, &netErr)
	r.True(netErr.Timeout())
	r.Equal("hello", string(res.Data))
}
