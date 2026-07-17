package sshtunnel_test

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// An SSH channel natively rejects SetDeadline ("tcpChan: deadline not
// supported"), which would make every checker that bounds its reads fail
// instantly on a tunneled check. The dialer wraps the channel to emulate
// deadlines, so a read past the deadline must time out like a real socket.
func TestTunneledConnSupportsDeadlines(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A target that accepts and then says nothing — the read must hit the
	// deadline rather than block forever or error immediately.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		<-time.After(5 * time.Second)
		_ = conn.Close()
	}()

	srv := sshtunneltest.Start(t)
	srv.Forward("silent.invalid:80", listener.Addr().String())

	conn, err := srv.Dialer(t).DialContext(t.Context(), "tcp", "silent.invalid:80")
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	// Setting a deadline must succeed (the native channel would refuse).
	r.NoError(conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)))

	_, err = conn.Read(make([]byte, 8))
	r.ErrorIs(err, os.ErrDeadlineExceeded)
}
