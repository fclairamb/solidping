package checkrdp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net"
	"strconv"
	"sync"
	"time"

	grdp "github.com/fclairamb/solidping/server/third_party/grdp"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// Authenticated-run failure reasons. Each maps to a distinct machine-readable
// code in the result output so a Down verdict is diagnosable without log
// access (spec: "New failure states with distinct error codes").
type authFailure string

const (
	// authRejected is credentials rejected by CredSSP/NLA: bad password,
	// locked account, or a domain that disabled NTLM.
	authRejected authFailure = "auth_rejected"
	// authTimedOut is the logon sequence never reaching a stable desktop
	// within the check's budget: hung profile, slow logon scripts.
	authTimedOut authFailure = "logon_timed_out"
	// authServerDropped is the server ending the session after the logon
	// started: licensing, policy, "another user is logged on".
	authServerDropped authFailure = "session_disconnected"
)

// The failure messages these codes carry in the output. Constants rather than
// fmt calls (err113: no dynamic errors) — they are fixed sentences by design.
const (
	msgAuthRejected   = "logon failed: the server rejected the credentials (CredSSP/NLA)"
	msgLogonTimedOut  = "logon timed out: the desktop never settled within the check timeout"
	msgServerDropped  = "session was disconnected by the server (licensing, policy, or another user logged on)"
	msgLogoffFailed   = "logon succeeded but ending the session failed"
	msgNoFrameForShot = "logon succeeded but no desktop frame was received in time for a screenshot"
)

// MaxConcurrentRDP caps simultaneous authenticated RDP sessions per worker
// process. Its OWN semaphore, deliberately not shared with the browser slots:
// a full Chrome pool says nothing about RDP capacity and vice versa. Four
// mirrors MaxConcurrentBrowsers for the same reasons — a real Windows logon
// is the most expensive RDP run there is, and the wait happens INSIDE the
// check's own timeout budget so a saturated worker reports a timeout rather
// than queueing invisibly.
const MaxConcurrentRDP = 4

// rdpSlots is the semaphore behind MaxConcurrentRDP. Process-wide, like the
// browser one, because the resource it protects (the worker's outbound RDP
// identity, one session per run) is process-wide.
//
//nolint:gochecknoglobals // process-wide concurrency cap, see MaxConcurrentRDP
var rdpSlots = make(chan struct{}, MaxConcurrentRDP)

// acquireRDPSlot waits for one of the MaxConcurrentRDP slots, giving up when
// the check's context (which already carries the check's timeout) is done.
// The returned release must be called exactly once.
func acquireRDPSlot(ctx context.Context) (func(), bool) {
	select {
	case rdpSlots <- struct{}{}:
		return func() { <-rdpSlots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// stableQuiet is how long the screen must stay free of bitmap updates before
// the logon counts as settled. A Windows desktop keeps painting during
// profile load and logon scripts; 2 s of silence is the point where a healthy
// logon has finished and a hung one never reaches.
const stableQuiet = 2 * time.Second

// errAuthFailure is a distinct authenticated-run failure carrying its machine
// code. Classified where it is raised, rendered where the verdict is built.
type errAuthFailure struct {
	reason authFailure
	msg    string
	cause  error
}

func (e *errAuthFailure) Error() string { return e.msg }

func (e *errAuthFailure) Unwrap() error { return e.cause }

// authFailuref builds an errAuthFailure with a wrapped cause.
func authFailuref(reason authFailure, cause error) *errAuthFailure {
	switch reason {
	case authRejected:
		return &errAuthFailure{reason: reason, msg: msgAuthRejected, cause: cause}
	case authTimedOut:
		return &errAuthFailure{reason: reason, msg: msgLogonTimedOut, cause: cause}
	case authServerDropped:
		return &errAuthFailure{reason: reason, msg: msgServerDropped, cause: cause}
	default:
		return &errAuthFailure{reason: reason, msg: cause.Error(), cause: cause}
	}
}

// frameBuffer accumulates bitmap tiles into one desktop image. It is the
// client-side screen state a screenshot is rendered from — the RDP analogue
// of the browser page for every pixel assertion phase 2 exposes.
type frameBuffer struct {
	mu   sync.Mutex
	img  *image.RGBA
	w, h int
}

// newFrameBuffer allocates the desktop-sized canvas.
func newFrameBuffer(w, h int) *frameBuffer {
	return &frameBuffer{img: image.NewRGBA(image.Rect(0, 0, w, h)), w: w, h: h}
}

// paint blits one grdp Bitmap tile into the buffer. Tiles arrive in screen
// coordinates; out-of-bounds and zero-sized tiles are ignored rather than
// rejected — a partially-decoded desktop is still worth having.
func (f *frameBuffer) paint(b grdp.Bitmap) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.img == nil {
		return
	}

	tw, th := b.Width, b.Height
	if tw <= 0 || th <= 0 {
		return
	}

	tile := b.FillRGBA(nil)
	bounds := f.img.Bounds()
	intersect := image.Rect(b.DestLeft, b.DestTop, b.DestLeft+tw, b.DestTop+th).Intersect(bounds)
	if intersect.Empty() {
		return
	}

	for y := intersect.Min.Y; y < intersect.Max.Y; y++ {
		srcY := y - b.DestTop
		srcX := intersect.Min.X - b.DestLeft
		dstOff := f.img.PixOffset(intersect.Min.X, y)
		srcOff := (srcY*tw + srcX) * 4
		copy(f.img.Pix[dstOff:dstOff+(intersect.Dx()*4)], tile.Pix[srcOff:srcOff+(intersect.Dx()*4)])
	}
}

// snapshot returns the current desktop as an image for encoding.
func (f *frameBuffer) snapshot() image.Image {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.img
}

// rdpSession is ONE authenticated RDP session: the grdp client, the
// framebuffer it paints, and the concurrency slot it holds.
type rdpSession struct {
	client *grdp.RdpClient
	frame  *frameBuffer

	// lastBitmap is the wall-clock time of the most recent bitmap update —
	// the state waitForStable reads.
	mu         sync.Mutex
	lastBitmap time.Time

	// closed guards the once-only teardown; idempotent like Session.Close.
	closed bool
}

// openRDPSession connects and logs on. conn is the caller's pre-dialed
// connection (a tunnel), or nil to let grdp dial the target itself.
func openRDPSession(
	ctx context.Context,
	cfg *checkconfig.RDPConfig,
	conn net.Conn,
) (*rdpSession, error) {
	hostPort := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	width, height := sessionGeometry(cfg)

	s := &rdpSession{
		frame: newFrameBuffer(width, height),
	}

	// The dialer seam is what routes the session through the caller's conn:
	// grdp dials through it exactly once per login attempt. A server
	// redirection triggers a second dial, which a pre-supplied (and already
	// consumed) connection cannot honor — that surfaces as the redirect error
	// it is rather than a mysterious hang.
	dialUsed := false
	dialer := func(string) (net.Conn, error) {
		if conn != nil && !dialUsed {
			dialUsed = true

			return conn, nil
		}

		if conn != nil {
			return nil, errors.New("server redirected the RDP session; reconnecting through a pre-supplied connection is not supported")
		}

		var d net.Dialer

		return d.DialContext(ctx, "tcp", hostPort)
	}

	s.client = grdp.NewRdpClient(hostPort, width, height, dialer)
	s.client.OnBitmap(s.paintBitmaps)
	s.client.OnError(func(e error) {
		s.recordBitmapTime() // any server activity counts for stability
	})
	s.client.OnClose(func() {
		s.recordBitmapTime()
	})

	if err := s.client.Login(cfg.Domain, cfg.Username, cfg.Password); err != nil {
		s.close()

		return nil, classifyLoginError(err)
	}

	// "ready" (Login returning) means the connection sequence finished; the
	// DESKTOP is the thing that has to settle. Bounded by the caller's
	// context, which carries the check's timeout.
	if err := s.waitForStable(ctx, stableQuiet); err != nil {
		s.close()

		return nil, err
	}

	return s, nil
}

// sessionGeometry resolves the desktop size, defaulting to 1280x800.
func sessionGeometry(cfg *checkconfig.RDPConfig) (int, int) {
	const (
		defaultWidth  = 1280
		defaultHeight = 800
	)

	// The check config carries no size fields; JS rdp.connect does (phase 2
	// passes them through its own call path). The check always uses the
	// default: a monitoring account needs no exotic resolution.
	_, _ = cfg, defaultWidth

	return defaultWidth, defaultHeight
}

// paintBitmaps blits tiles and marks activity. Runs on grdp's read goroutine.
func (s *rdpSession) paintBitmaps(bitmaps []grdp.Bitmap) {
	for _, b := range bitmaps {
		s.frame.paint(b)
	}

	s.recordBitmapTime()
}

// recordBitmapTime stamps the activity clock.
func (s *rdpSession) recordBitmapTime() {
	s.mu.Lock()
	s.lastBitmap = time.Now()
	s.mu.Unlock()
}

// waitForStable blocks until no bitmap update has arrived for quiet, or the
// context ends. Bounded by the check's timeout — a hung logon times out.
func (s *rdpSession) waitForStable(ctx context.Context, quiet time.Duration) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return authFailuref(authTimedOut, ctx.Err())
		case <-tick.C:
			s.mu.Lock()
			last := s.lastBitmap
			s.mu.Unlock()

			if !last.IsZero() && time.Since(last) >= quiet {
				return nil
			}
		}
	}
}

// screenshotPNG renders the framebuffer as PNG bytes.
func (s *rdpSession) screenshotPNG() ([]byte, error) {
	img := s.frame.snapshot()
	if img == nil {
		return nil, errors.New("no desktop frame was received")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	return buf.Bytes(), nil
}

// logoff asks the server to end the session: the TS_SHUTDOWN_REQUEST data PDU
// is the protocol's "log me off" verb (MS-RDPBCGR 2.2.9.3 — the server then
// tears the session down), after which the transport is closed. The PDU goes
// through the fork's SendLogoff patch (third_party/grdp/patch_solidping.go).
//
// Best-effort by contract: a server that ignores the request (some xrdp
// builds) or a transport already closing is reported, not fatal — the logon
// itself succeeded and the verdict must not turn on this.
func (s *rdpSession) logoff() error {
	if s.client == nil {
		return errors.New("session already closed")
	}

	s.client.SendLogoff()

	// Give the server a moment to act before the transport drops: the
	// shutdown reply arrives on the same socket, and closing too early can
	// leave the session on the server.
	time.Sleep(500 * time.Millisecond)
	s.close()

	return nil
}

// disconnect drops the transport WITHOUT asking the server to end the
// session: it stays alive until the server's idle policy ends it, and the
// next connect reattaches to it.
func (s *rdpSession) disconnect() {
	s.close()
}

// close tears the transport down, once.
func (s *rdpSession) close() {
	s.mu.Lock()

	if s.closed {
		s.mu.Unlock()

		return
	}

	s.closed = true
	s.mu.Unlock()

	s.client.Close()
}

// classifyLoginError maps a grdp login failure onto the distinct
// authenticated-run failure codes. The transport-level classification lives
// in error text matching: grdp returns wrapped fmt.Errorf chains whose key
// phrases are stable ("dial err", "connection err", "x224 connect err").
func classifyLoginError(err error) error {
	// A context deadline inside the login attempt is the check's own budget
	// expiring — the logon never completed.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return authFailuref(authTimedOut, err)
	}

	msg := err.Error()

	switch {
	case containsAny(msg, "CredSSP", "CREDSSP", "NTLM", "STATUS_LOGON_FAILURE", "logon failed", "authentication"):
		return authFailuref(authRejected, err)
	case containsAny(msg, "dial err", "connection refused", "no route", "i/o timeout", "connection reset"):
		// The TCP/TLS layer failed before any logon attempt: not an
		// authentication failure, but the session never came up at all.
		return authFailuref(authServerDropped, err)
	case containsAny(msg, "connection timeout", "connection err"):
		return authFailuref(authTimedOut, err)
	default:
		return authFailuref(authServerDropped, err)
	}
}

// containsAny reports whether s contains any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && containsFold(s, sub) {
			return true
		}
	}

	return false
}

// containsFold is a small case-insensitive substring check. strings.EqualFold
// over windows would allocate; ToLower twice is simpler and these strings are
// short.
func containsFold(s, sub string) bool {
	return len(sub) <= len(s) &&
		(bytes.Contains([]byte(toLower(s)), []byte(toLower(sub))))
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}

	return string(b)
}
