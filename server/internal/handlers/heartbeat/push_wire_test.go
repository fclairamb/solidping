package heartbeat_test

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

// wireSetup boots the real listeners over the real ingest and a real (in-memory)
// database, so these tests exercise the exact path a device hits.
type wireSetup struct {
	*heartbeatSetup

	server *heartbeatpush.Server
}

func newWireSetup(t *testing.T) *wireSetup {
	t.Helper()

	base := newHeartbeatSetup(t)

	cfg := config.DefaultHeartbeatConfig()
	cfg.UDPListen = "127.0.0.1:0"
	cfg.TCPListen = "127.0.0.1:0"

	server := heartbeatpush.NewServer(&cfg, base.svc)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Close() })

	return &wireSetup{heartbeatSetup: base, server: server}
}

// sendUDP sends one datagram and reports the reply bytes (nil when the server
// stayed silent).
func (w *wireSetup) sendUDP(t *testing.T, payload string) []byte {
	t.Helper()

	addr, ok := w.server.UDPAddr().(*net.UDPAddr)
	require.True(t, ok)

	conn, err := net.DialUDP("udp", nil, addr)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte(payload))
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(400*time.Millisecond)))

	buf := make([]byte, 64)

	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}

	return buf[:n]
}

// TestWireUDPAcceptsAndRecords is the end-to-end positive control: a real
// datagram over a real socket produces a real result row.
func TestWireUDPAcceptsAndRecords(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	w := newWireSetup(t)

	reply := w.sendUDP(t, "SP1 "+w.org.Slug+"/"+w.checkSlug()+" "+testToken)
	r.Equal("OK", string(reply))
	r.Eventually(func() bool { return w.beatCount(t) == 1 }, 2*time.Second, 20*time.Millisecond)
}

// TestWireUDPFailuresAreIndistinguishable is the no-oracle property measured on
// the wire, against a REAL database: a nonexistent organization, a nonexistent
// check, a wrong token, a bad MAC and a replay all produce the same zero bytes.
//
// This is the assertion a fake sink cannot make — it is the only place where
// "does this check exist?" is answered by an actual lookup.
func TestWireUDPFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	w := newWireSetup(t)
	slug := w.checkSlug()

	// Burn one signed counter so the replay case below is a genuine replay.
	replayed := heartbeatpush.SignSP2(w.org.Slug, slug, testToken, 0, 5, "")
	r.Equal("OK", string(w.sendUDP(t, replayed)), "positive control")
	r.Eventually(func() bool { return w.beatCount(t) == 1 }, 2*time.Second, 20*time.Millisecond)

	for name, payload := range map[string]string{
		"nonexistent org":   "SP1 no-such-org/" + slug + " " + testToken,
		"nonexistent check": "SP1 " + w.org.Slug + "/no-such-check " + testToken,
		"wrong token":       "SP1 " + w.org.Slug + "/" + slug + " wrong-token",
		"bad mac":           heartbeatpush.SignSP2(w.org.Slug, slug, "wrong-key", 0, 9, ""),
		"replay":            replayed,
		"stale counter":     heartbeatpush.SignSP2(w.org.Slug, slug, testToken, 0, 1, ""),
		"malformed":         "hello there",
		"signed nonexistent org": heartbeatpush.SignSP2(
			"no-such-org", slug, testToken, 0, 9, ""),
	} {
		r.Nil(w.sendUDP(t, payload), "%s must produce no bytes at all", name)
	}

	// None of them recorded anything either.
	r.Equal(1, w.beatCount(t))

	// Positive control again, so the silences above are not a dead listener.
	r.Equal("OK", string(w.sendUDP(t, heartbeatpush.SignSP2(w.org.Slug, slug, testToken, 0, 6, ""))))
	r.Eventually(func() bool { return w.beatCount(t) == 2 }, 2*time.Second, 20*time.Millisecond)
}

// TestWireTCPOpenConnectionRecordsNothing measures the liveness rule against a
// real database: holding a socket open, and even sending a partial line, marks
// nothing alive.
func TestWireTCPOpenConnectionRecordsNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	w := newWireSetup(t)

	dialer := net.Dialer{Timeout: time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", w.server.TCPAddr().String())
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	line := "SP1 " + w.org.Slug + "/" + w.checkSlug() + " " + testToken

	_, err = conn.Write([]byte(line))
	r.NoError(err)

	time.Sleep(250 * time.Millisecond)
	r.Zero(w.beatCount(t), "an open connection with an unterminated line is not a heartbeat")

	// Positive control: terminating the line records exactly one beat.
	_, err = conn.Write([]byte("\n"))
	r.NoError(err)

	r.NoError(conn.SetReadDeadline(time.Now().Add(2 * time.Second)))

	buf := make([]byte, 8)

	n, err := conn.Read(buf)
	r.NoError(err)
	r.Equal("OK\n", string(buf[:n]))
	r.Equal(1, w.beatCount(t))
}

// TestWireSP1RejectedOnRequireHMACCheck is the end-to-end version of the
// option: over a real socket, on a real check, an unsigned beat marks nothing.
func TestWireSP1RejectedOnRequireHMACCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	w := newWireSetup(t)
	slug := w.checkSlug()

	w.check.Config["require_hmac"] = true
	cfg := w.check.Config
	r.NoError(w.dbSvc.UpdateCheck(t.Context(), w.check.UID, &models.CheckUpdate{Config: &cfg}))

	r.Nil(w.sendUDP(t, "SP1 "+w.org.Slug+"/"+slug+" "+testToken))
	time.Sleep(200 * time.Millisecond)
	r.Zero(w.beatCount(t))

	// Positive control: the signed form on the same check is accepted.
	r.Equal("OK", string(w.sendUDP(t, heartbeatpush.SignSP2(w.org.Slug, slug, testToken, 0, 1, ""))))
	r.Eventually(func() bool { return w.beatCount(t) == 1 }, 2*time.Second, 20*time.Millisecond)
}
