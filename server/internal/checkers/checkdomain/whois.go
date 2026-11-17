package checkdomain

import (
	"context"
	"net"
	"time"

	"github.com/likexian/whois"
)

// defaultWHOISTimeout bounds a WHOIS query when the context carries no
// deadline (the library's own default).
const defaultWHOISTimeout = 30 * time.Second

// queryWHOIS runs a WHOIS lookup that honors ctx: every port-43 connection
// is dialed with ctx, its read/write deadlines are clamped to ctx's deadline,
// and it is closed as soon as ctx is done. The likexian client only knows its
// own fixed timeout, so without this a stalled WHOIS server held the checker
// past the check timeout. server is "host:port" or empty for the default
// IANA referral.
func queryWHOIS(ctx context.Context, domain, server string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	timeout := defaultWHOISTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}

	client := whois.NewClient().
		SetDialer(&contextDialer{ctx: ctx}).
		SetTimeout(timeout)

	raw, err := client.Whois(domain, server)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}

		// The clamped read deadline can fire a hair before ctx's own timer.
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return "", context.DeadlineExceeded
		}
	}

	return raw, err
}

// contextDialer is a proxy.Dialer whose connections are bound to ctx.
type contextDialer struct {
	ctx context.Context //nolint:containedctx // the whois library's Dial has no ctx argument
}

// Dial opens a connection that ctx can cut.
func (d *contextDialer) Dial(network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(d.ctx, network, addr)
	if err != nil {
		return nil, err
	}

	return newContextConn(d.ctx, conn), nil
}

// contextConn closes the underlying connection when ctx is done and never
// lets a deadline extend past ctx's own.
type contextConn struct {
	net.Conn

	ctx  context.Context //nolint:containedctx // bound to the connection's lifetime
	stop func() bool
}

func newContextConn(ctx context.Context, conn net.Conn) *contextConn {
	return &contextConn{
		Conn: conn,
		ctx:  ctx,
		stop: context.AfterFunc(ctx, func() { _ = conn.Close() }),
	}
}

func (c *contextConn) clamp(t time.Time) time.Time {
	if deadline, ok := c.ctx.Deadline(); ok && (t.IsZero() || deadline.Before(t)) {
		return deadline
	}

	return t
}

// SetDeadline clamps t to ctx's deadline.
func (c *contextConn) SetDeadline(t time.Time) error { return c.Conn.SetDeadline(c.clamp(t)) }

// SetReadDeadline clamps t to ctx's deadline.
func (c *contextConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(c.clamp(t))
}

// SetWriteDeadline clamps t to ctx's deadline.
func (c *contextConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(c.clamp(t))
}

// Close releases the ctx watcher, then the connection.
func (c *contextConn) Close() error {
	c.stop()

	return c.Conn.Close()
}
