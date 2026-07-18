package sshtunnel

import (
	"net"
	"os"
	"sync"
	"time"
)

// deadlineConn adds working deadlines to a forwarded SSH channel.
//
// x/crypto/ssh's channel connection rejects every SetDeadline call outright
// ("ssh: tcpChan: deadline not supported") — the SSH protocol has no per-channel
// deadline concept. That is a problem here rather than a curiosity: bounding a
// read with a deadline is standard practice for a probe (checktcp does it before
// every read and write), so an un-emulated deadline turns into an immediate
// error and every tunneled TCP check would report a bogus failure instead of
// measuring the target.
//
// The emulation arms a timer that closes the channel when the deadline expires
// and reports os.ErrDeadlineExceeded from the interrupted Read/Write, so callers
// see the same timeout semantics a real socket gives them. Closing (rather than
// merely unblocking) is the right call for a probe: the connection is single-use
// per execution and gets torn down with the tunnel moments later anyway.
type deadlineConn struct {
	net.Conn

	mu           sync.Mutex
	readTimer    *time.Timer
	writeTimer   *time.Timer
	readExpired  bool
	writeExpired bool
}

func newDeadlineConn(conn net.Conn) net.Conn {
	return &deadlineConn{Conn: conn}
}

func (c *deadlineConn) Read(buf []byte) (int, error) {
	n, err := c.Conn.Read(buf)
	if err != nil && c.expired(true) {
		return n, os.ErrDeadlineExceeded
	}

	return n, err
}

func (c *deadlineConn) Write(buf []byte) (int, error) {
	n, err := c.Conn.Write(buf)
	if err != nil && c.expired(false) {
		return n, os.ErrDeadlineExceeded
	}

	return n, err
}

func (c *deadlineConn) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}

	return c.SetWriteDeadline(deadline)
}

func (c *deadlineConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readExpired = false
	c.readTimer = c.arm(c.readTimer, deadline, func() {
		c.mu.Lock()
		c.readExpired = true
		c.mu.Unlock()
	})

	return nil
}

func (c *deadlineConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.writeExpired = false
	c.writeTimer = c.arm(c.writeTimer, deadline, func() {
		c.mu.Lock()
		c.writeExpired = true
		c.mu.Unlock()
	})

	return nil
}

// arm replaces a pending timer with one that marks the direction expired and
// closes the channel. A zero deadline disarms, matching net.Conn semantics.
// Caller holds c.mu.
func (c *deadlineConn) arm(timer *time.Timer, deadline time.Time, mark func()) *time.Timer {
	if timer != nil {
		timer.Stop()
	}

	if deadline.IsZero() {
		return nil
	}

	return time.AfterFunc(time.Until(deadline), func() {
		mark()
		_ = c.Conn.Close()
	})
}

func (c *deadlineConn) expired(read bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if read {
		return c.readExpired
	}

	return c.writeExpired
}

// Close stops any pending deadline timers before closing the channel, so a
// finished check leaves nothing armed behind it.
func (c *deadlineConn) Close() error {
	c.mu.Lock()

	if c.readTimer != nil {
		c.readTimer.Stop()
	}

	if c.writeTimer != nil {
		c.writeTimer.Stop()
	}

	c.mu.Unlock()

	return c.Conn.Close()
}
