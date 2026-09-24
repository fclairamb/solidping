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
	"strings"
	"sync"
	"time"

	grdp "github.com/fclairamb/solidping/server/third_party/grdp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// authFailure is the machine-readable failure-code vocabulary for an
// authenticated run. Each maps to a distinct code in the result output so a
// Down verdict is diagnosable without log access (spec: "New failure states
// with distinct error codes").
type authFailure string

const (
	// AuthRejected is credentials rejected by CredSSP/NLA: bad password,
	// locked account, or a domain that disabled NTLM.
	AuthRejected authFailure = "auth_rejected"
	// AuthTimedOut is the logon sequence never reaching a stable desktop
	// within the check's budget: hung profile, slow logon scripts.
	AuthTimedOut authFailure = "logon_timed_out"
	// AuthServerDropped is the server ending the session after the logon
	// started: licensing, policy, "another user is logged on".
	AuthServerDropped authFailure = "session_disconnected"
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

// ErrSlotTimeout is the RDP slot wait giving up: every one of the
// MaxConcurrentRDP slots was busy for as long as the caller's context
// allowed. Deliberately NOT an infrastructure failure — the worker is
// merely full — so it maps to a timeout verdict (the check) or a
// `{ timedOut: true }` value (JS) rather than an error.
var ErrSlotTimeout = errors.New("timed out waiting for a free RDP slot " +
	"(at most 4 RDP logons run at a time on one worker)")

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

// ErrAuthFailure is a distinct authenticated-run failure carrying its machine
// code. Classified where it is raised, rendered where the verdict is built.
// Exported because the JS runtime's rdp.connect inspects the code to hand the
// script a `failureCode` value.
type ErrAuthFailure struct {
	// Reason is the machine-readable code (auth_rejected, logon_timed_out,
	// session_disconnected).
	Reason authFailure
	// Msg is the human-readable failure sentence.
	Msg string
	// Cause is the wrapped transport error, if any.
	Cause error
}

// Code returns the machine-readable failure code.
func (e *ErrAuthFailure) Code() string { return string(e.Reason) }

func (e *ErrAuthFailure) Error() string { return e.Msg }

func (e *ErrAuthFailure) Unwrap() error { return e.Cause }

// authFailuref builds an ErrAuthFailure with a wrapped cause. Exported for
// the JS bindings' tests, which seed failures through the seam.
func authFailuref(reason authFailure, cause error) *ErrAuthFailure {
	switch reason {
	case AuthRejected:
		return &ErrAuthFailure{Reason: reason, Msg: msgAuthRejected, Cause: cause}
	case AuthTimedOut:
		return &ErrAuthFailure{Reason: reason, Msg: msgLogonTimedOut, Cause: cause}
	case AuthServerDropped:
		return &ErrAuthFailure{Reason: reason, Msg: msgServerDropped, Cause: cause}
	default:
		return &ErrAuthFailure{Reason: reason, Msg: cause.Error(), Cause: cause}
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
	// client is the grdp client this session drives. Typed as inputClient (a
	// private interface over the *RdpClient methods the check needs) so the
	// input-verb sequence is testable without an RDP server.
	client inputClient
	frame  *frameBuffer

	// lastBitmap is the wall-clock time of the most recent bitmap update —
	// the state waitForStable reads.
	mu         sync.Mutex
	lastBitmap time.Time

	// release returns the concurrency slot. Called exactly once, by close.
	release func()

	// closed guards the once-only teardown; idempotent like Session.Close.
	closed bool
}

// openRDPSession connects and logs on. conn is the caller's pre-dialed
// connection (a tunnel), or nil to let grdp dial the target itself.
func openRDPSession(
	ctx context.Context,
	cfg *checkconfig.RDPConfig,
	width, height int,
	conn net.Conn,
) (*rdpSession, error) {
	// The concurrency cap lives here, around the whole session, and its wait
	// counts against the caller's context — an execution that never gets a
	// slot reports a timeout rather than queueing invisibly.
	release, acquired := acquireRDPSlot(ctx)
	if !acquired {
		return nil, ErrSlotTimeout
	}

	s, err := openRDPSessionLogged(ctx, cfg, width, height, conn)
	if err != nil {
		release()

		return nil, err
	}

	s.release = release

	return s, nil
}

// openRDPSessionLogged is openRDPSession without the slot: connect, logon,
// settle. The slot ownership travels on the session so Close releases it.
func openRDPSessionLogged(
	ctx context.Context,
	cfg *checkconfig.RDPConfig,
	width, height int,
	conn net.Conn,
) (*rdpSession, error) {
	hostPort := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	width, height = sessionGeometry(width, height)

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

		if tunneled := checkerdef.TunnelDialerFrom(ctx); tunneled != nil {
			return tunneled.DialContext(ctx, "tcp", hostPort)
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
	if err := s.WaitForStable(ctx, stableQuiet); err != nil {
		s.close()

		return nil, err
	}

	return s, nil
}

// sessionGeometry resolves the desktop size: the JS caller's values when
// given, else 1280x800.
func sessionGeometry(width, height int) (int, int) {
	const (
		defaultWidth  = 1280
		defaultHeight = 800
	)

	if width <= 0 || width > 8192 {
		width = defaultWidth
	}

	if height <= 0 || height > 8192 {
		height = defaultHeight
	}

	return width, height
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
func (s *rdpSession) WaitForStable(ctx context.Context, quiet time.Duration) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return authFailuref(AuthTimedOut, ctx.Err())
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

// ScreenshotPNG renders the framebuffer as PNG bytes.
func (s *rdpSession) ScreenshotPNG() ([]byte, error) {
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

	if s.client != nil {
		s.client.Close()
	}

	if s.release != nil {
		s.release()
	}
}

// classifyLoginError maps a grdp login failure onto the distinct
// authenticated-run failure codes. The transport-level classification lives
// in error text matching: grdp returns wrapped fmt.Errorf chains whose key
// phrases are stable ("dial err", "connection err", "x224 connect err").
func classifyLoginError(err error) error {
	// A context deadline inside the login attempt is the check's own budget
	// expiring — the logon never completed.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return authFailuref(AuthTimedOut, err)
	}

	msg := err.Error()

	switch {
	case containsAny(msg, "CredSSP", "CREDSSP", "NTLM", "STATUS_LOGON_FAILURE", "logon failed", "authentication"):
		return authFailuref(AuthRejected, err)
	case containsAny(msg, "dial err", "connection refused", "no route", "i/o timeout", "connection reset"):
		// The TCP/TLS layer failed before any logon attempt: not an
		// authentication failure, but the session never came up at all.
		return authFailuref(AuthServerDropped, err)
	case containsAny(msg, "connection timeout", "connection err"):
		return authFailuref(AuthTimedOut, err)
	default:
		return authFailuref(AuthServerDropped, err)
	}
}

// Infra reports whether err means the SESSION infrastructure is gone (the
// transport died, the session was never opened) as opposed to a target-side
// verdict (auth rejected, change timeout). The split the JS runtime turns
// into throw-vs-return: infrastructure throws and becomes status `error`,
// everything else comes back as `{ ok: false }` and lets the script decide
// the target is down. A context deadline is deliberately NOT infrastructure:
// that is the check's own timeout expiring.
func Infra(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, ErrNotReady) || errors.Is(err, ErrSessionDead) {
		return true
	}

	if errors.Is(err, ErrChangeTimedOut) || errors.Is(err, ErrSlotTimeout) {
		return false
	}

	var authErr *ErrAuthFailure
	if errors.As(err, &authErr) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	return false
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

// inputClient is the slice of *grdp.RdpClient the input verbs drive. The
// real client satisfies it; tests record through a fake — the seam that
// makes the driver sequence testable without an RDP server.
type inputClient interface {
	EventReady() bool
	SendScancode(sc uint16, release bool)
	SendUnicodeKey(r rune)
	MouseMove(x, y int)
	MouseDown(button int, x, y int)
	MouseUp(button int, x, y int)
	SendLogoff()
	// On* mirror the real client's chainable signatures.
	OnBitmap(paint func([]grdp.Bitmap)) *grdp.RdpClient
	OnError(f func(e error)) *grdp.RdpClient
	OnClose(f func()) *grdp.RdpClient
	Login(domain, user, password string) error
	Close()
}

// RDPSession is the slice of rdpSession the `rdp` JS object drives. Exported
// as an interface for the same reason BrowserSession is: CI's backend job has
// no RDP server, so every binding test runs against a fake through the
// OpenRDPSession seam below. Production gets the real *rdpSession.
//
// One method per documented rdp API call.
//
//nolint:interfacebloat // mirrors the documented script surface
type RDPSession interface {
	// WaitForStable blocks until no bitmap update has arrived for quiet.
	WaitForStable(ctx context.Context, quiet time.Duration) error
	// WaitForChange blocks until a bitmap update arrives after the session
	// reached stability (or the timeout expires — then ErrChangeTimedOut).
	WaitForChange(ctx context.Context, timeout time.Duration) error
	// Click / RightClick / DoubleClick / Move drive the pointer.
	Click(x, y int) error
	RightClick(x, y int) error
	DoubleClick(x, y int) error
	Move(x, y int) error
	// Type sends text as Unicode input events (layout-independent).
	Type(text string) error
	// Key presses one named key or combo ("enter", "ctrl+alt+end", "win+r").
	Key(key string) error
	// Pixel reads one pixel as RGB.
	Pixel(x, y int) (PixelColor, error)
	// RegionHash hashes a rectangle of the framebuffer.
	RegionHash(x, y, w, h int) (uint64, error)
	// ScreenshotPNG renders the desktop as PNG bytes.
	ScreenshotPNG() ([]byte, error)
	// EndLogoff / EndDisconnect end the session per the two modes.
	EndLogoff() error
	EndDisconnect()
	// Closed reports whether the session is gone (idempotent teardown done).
	Closed() bool
}

// PixelColor is one pixel, the shape a script reads it in. Alpha is always
// opaque — the RDP framebuffer has none.
type PixelColor struct {
	R, G, B uint8
}

// OpenRDPSession is the seam `rdp.connect` goes through: slot, logon, settle.
// Production points at the real path; tests replace it. Tunnel dialing is
// honored inside the session the same way the pre-auth check does it.
//
//nolint:gochecknoglobals // test seam, mirrors OpenBrowser
var OpenRDPSession = func(
	ctx context.Context,
	cfg *checkconfig.RDPConfig,
	width, height int,
	conn net.Conn,
) (RDPSession, error) {
	s, err := openRDPSession(ctx, cfg, width, height, conn)
	if err != nil {
		return nil, err
	}

	return s, nil
}

// --- RDPSession implementation ---

// ErrSessionDead is the failure a session method hits when the transport is
// gone (the server dropped us, the tunnel closed). Infrastructure by
// classification: the JS runtime throws it as `error`.
var ErrSessionDead = errors.New("the RDP session is gone (closed or dropped by the server)")

// ErrChangeTimedOut is WaitForChange giving up: no bitmap update arrived
// within the caller's timeout. A VALUE the script inspects (a stuck login
// screen never changes), not a throw.
var ErrChangeTimedOut = errors.New("no screen change before the timeout")

// ErrNotReady is an input method called before the session accepts events —
// the connection sequence has not finished, or the server dropped the
// session. Treated as infrastructure: there is no target verdict to return.
var ErrNotReady = errors.New("the RDP session is not ready to accept input")

// errNotStable is WaitForStable failing because the check's context ended
// first. It carries authTimedOut: a hung logon never settles.
// (Rendered by authErrorResult; declared here so tests can reference it.)

// ready reports whether the session accepts input and is still alive.
func (s *rdpSession) ready() bool {
	return s.client != nil && !s.closed && s.client.EventReady()
}

// dead reports whether the session is gone.
func (s *rdpSession) dead() bool {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	return closed
}

// Click moves the pointer to x,y and presses+releases the LEFT button
// (button index 0 in grdp's mapping: PTRFLAGS_BUTTON1).
func (s *rdpSession) Click(x, y int) error {
	return s.click(x, y, 0)
}

// RightClick presses+releases the RIGHT button (index 2).
func (s *rdpSession) RightClick(x, y int) error {
	return s.click(x, y, 2)
}

// DoubleClick presses+releases twice quickly.
func (s *rdpSession) DoubleClick(x, y int) error {
	if err := s.click(x, y, 0); err != nil {
		return err
	}

	// The double-click interval is client-side convention; 100 ms sits inside
	// every Windows default double-click time (200-500 ms).
	time.Sleep(100 * time.Millisecond)

	return s.click(x, y, 0)
}

func (s *rdpSession) click(x, y, button int) error {
	if !s.ready() {
		return ErrNotReady
	}

	s.client.MouseMove(x, y)
	s.client.MouseDown(button, x, y)
	s.client.MouseUp(button, x, y)

	return nil
}

// Move repositions the pointer without pressing anything.
func (s *rdpSession) Move(x, y int) error {
	if !s.ready() {
		return ErrNotReady
	}

	s.client.MouseMove(x, y)

	return nil
}

// Type sends text as Unicode input events (TS_UNICODE_KEYBOARD_EVENT), one
// press+release pair per rune. Layout-independent by design.
func (s *rdpSession) Type(text string) error {
	for _, r := range text {
		if !s.ready() {
			return ErrNotReady
		}

		s.client.SendUnicodeKey(r)
	}

	return nil
}

// keyTable maps the named keys key() accepts onto PS/2 Set 1 make codes.
// Extended keys carry the 0xE0 prefix; SendScancode strips it into the
// extended flag.
var keyTable = map[string]uint16{
	"enter": 0x001C, "return": 0x001C, "esc": 0x0001, "escape": 0x0001,
	"backspace": 0x000E, "tab": 0x000F, "space": 0x0039, "capslock": 0x003A,
	"f1": 0x003B, "f2": 0x003C, "f3": 0x003D, "f4": 0x003E,
	"f5": 0x003F, "f6": 0x0040, "f7": 0x0041, "f8": 0x0042,
	"f9": 0x0043, "f10": 0x0044, "f11": 0x0057, "f12": 0x0058,
	"a": 0x001E, "b": 0x0030, "c": 0x002E, "d": 0x0020, "e": 0x0012,
	"f": 0x0021, "g": 0x0022, "h": 0x0023, "i": 0x0017, "j": 0x0024,
	"k": 0x0025, "l": 0x0026, "m": 0x0032, "n": 0x0031, "o": 0x0018,
	"p": 0x0019, "q": 0x0010, "r": 0x0013, "s": 0x001F, "t": 0x0014,
	"u": 0x0016, "v": 0x002F, "w": 0x0011, "x": 0x002D, "y": 0x0015,
	"z": 0x002C,
	"1": 0x0002, "2": 0x0003, "3": 0x0004, "4": 0x0005, "5": 0x0006,
	"6": 0x0007, "7": 0x0008, "8": 0x0009, "9": 0x000A, "0": 0x000B,
	"ctrl": 0x001D, "alt": 0x0038, "shift": 0x002A, "win": 0xE05B,
	"left": 0xE04B, "right": 0xE04D, "up": 0xE048, "down": 0xE050,
	"pgup": 0xE049, "pgdn": 0xE051, "home": 0xE047, "end": 0xE04F,
	"insert": 0xE052, "delete": 0xE053, "del": 0xE053,
	"enter_numpad": 0xE01C, "apps": 0xE05D,
}

// parseKeyCombo splits "ctrl+alt+end" into its parts, validating every name
// against keyTable. Returned as scancodes in the order they must be held.
func parseKeyCombo(combo string) ([]uint16, error) {
	parts := splitCombo(combo)

	scancodes := make([]uint16, 0, len(parts))
	for _, part := range parts {
		sc, ok := keyTable[part]
		if !ok {
			return nil, fmt.Errorf("unknown key %q", part)
		}

		scancodes = append(scancodes, sc)
	}

	return scancodes, nil
}

// splitCombo lowercases and splits on '+', tolerating surrounding spaces.
func splitCombo(combo string) []string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

// Key presses one named key or combo: modifiers held, the last key
// pressed+released, modifiers released in reverse order. "enter" is a bare
// press+release of one key.
func (s *rdpSession) Key(key string) error {
	scancodes, err := parseKeyCombo(key)
	if err != nil {
		return err
	}

	if !s.ready() {
		return ErrNotReady
	}

	// Hold everything but the last.
	for _, sc := range scancodes[:len(scancodes)-1] {
		s.client.SendScancode(sc, false)
	}

	last := scancodes[len(scancodes)-1]
	s.client.SendScancode(last, false)
	s.client.SendScancode(last, true)

	// Release modifiers in reverse hold order.
	for i := len(scancodes) - 2; i >= 0; i-- {
		s.client.SendScancode(scancodes[i], true)
	}

	return nil
}

// Pixel reads one pixel. Coordinates outside the framebuffer are an error —
// a script asserting on (10,10) wants a real read, not a silently-zero one.
func (s *rdpSession) Pixel(x, y int) (PixelColor, error) {
	s.frame.mu.Lock()
	defer s.frame.mu.Unlock()

	if s.frame.img == nil {
		return PixelColor{}, errors.New("no desktop frame received yet")
	}

	bounds := s.frame.img.Bounds()
	if x < bounds.Min.X || x >= bounds.Max.X || y < bounds.Min.Y || y >= bounds.Max.Y {
		return PixelColor{}, fmt.Errorf("pixel (%d, %d) outside the %dx%d desktop", x, y, bounds.Dx(), bounds.Dy())
	}

	off := s.frame.img.PixOffset(x, y)

	return PixelColor{R: s.frame.img.Pix[off], G: s.frame.img.Pix[off+1], B: s.frame.img.Pix[off+2]}, nil
}

// RegionHash returns a stable FNV-1a hash over the raw RGBA bytes of the
// rectangle. "Stable" means: same visible pixels -> same hash across runs and
// across workers, so a script can compare hashes over time and across
// regions. Invalid rectangles (non-positive size, out of bounds) are errors
// rather than clamped reads.
func (s *rdpSession) RegionHash(x, y, w, h int) (uint64, error) {
	s.frame.mu.Lock()
	defer s.frame.mu.Unlock()

	if s.frame.img == nil {
		return 0, errors.New("no desktop frame received yet")
	}

	if w <= 0 || h <= 0 {
		return 0, fmt.Errorf("region must be at least 1x1, got %dx%d", w, h)
	}

	bounds := s.frame.img.Bounds()
	if x < bounds.Min.X || y < bounds.Min.Y ||
		x+w > bounds.Max.X || y+h > bounds.Max.Y {
		return 0, fmt.Errorf(
			"region (%d, %d, %dx%d) outside the %dx%d desktop", x, y, w, h, bounds.Dx(), bounds.Dy())
	}

	const (
		fnvOffset uint64 = 14695981039346656037
		fnvPrime  uint64 = 1099511628211
	)

	hash := fnvOffset
	for row := y; row < y+h; row++ {
		off := s.frame.img.PixOffset(x, row)
		for i := 0; i < w*4; i++ {
			hash ^= uint64(s.frame.img.Pix[off+i])
			hash *= fnvPrime
		}
	}

	return hash, nil
}

// waitForChange blocks until a bitmap update arrives after the session's
// current stability point, or the timeout expires.
func (s *rdpSession) WaitForChange(ctx context.Context, timeout time.Duration) error {
	s.mu.Lock()
	baseline := s.lastBitmap
	s.mu.Unlock()

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ErrChangeTimedOut
		case <-tick.C:
			s.mu.Lock()
			last := s.lastBitmap
			s.mu.Unlock()

			if last.After(baseline) {
				return nil
			}

			if !timeoutNotPassed(deadline, time.Now()) {
				return ErrChangeTimedOut
			}
		}
	}
}

// timeoutNotPassed reports whether the wall clock is still inside deadline.
func timeoutNotPassed(deadline, now time.Time) bool {
	return now.Before(deadline)
}

// EndLogoff asks the server to end the session (TS_SHUTDOWN_REQUEST) and
// closes the transport. Returns the best-effort failure, if any.
func (s *rdpSession) EndLogoff() error {
	if s.dead() {
		return nil
	}

	return s.logoff()
}

// EndDisconnect drops the transport, leaving the session running on the
// server until its idle policy ends it.
func (s *rdpSession) EndDisconnect() {
	s.disconnect()
}

// Closed reports whether the session has been torn down.
func (s *rdpSession) Closed() bool { return s.dead() }
