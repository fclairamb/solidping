package checkvnc

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"net"
	"strings"
	"sync"
	"time"
)

// The scripted-control path: a long-lived authenticated session the `vnc` JS
// object drives. A reader goroutine keeps requesting incremental
// FramebufferUpdates and paints them into a frame buffer, so pixel reads, region
// hashes, stability and change detection all work on a live desktop.

// Errors a session method returns.
var (
	// ErrSessionDead is a session whose transport is gone (the server dropped
	// us). Infrastructure: the JS runtime throws it.
	ErrSessionDead = errors.New("the VNC session is gone (closed or dropped by the server)")
	// ErrChangeTimedOut is WaitForChange giving up: no update arrived in time.
	// A value the script inspects, not a throw.
	ErrChangeTimedOut = errors.New("no screen change before the timeout")
	// ErrUnknownKey is Key rejecting a name outside the key table.
	ErrUnknownKey = errors.New("unknown key")
	// ErrRegionEmpty is RegionHash rejecting a zero-sized rectangle.
	ErrRegionEmpty = errors.New("region must be at least 1x1")
	// ErrOutsideDesktop is a coordinate or region outside the framebuffer.
	ErrOutsideDesktop = errors.New("outside the desktop")
)

// Pointer button-mask bits (RFC 6143 §7.5.5) and message types.
const (
	buttonLeft  = 1
	buttonRight = 4

	msgKeyEvent     = 4
	msgPointerEvent = 5

	// keysymUnicodeBase maps a Unicode code point without a Latin-1 keysym.
	keysymUnicodeBase = 0x01000000
	latin1Max         = 0xFF

	pollInterval = 50 * time.Millisecond
)

// PixelColor is one pixel as a script reads it.
type PixelColor struct {
	R, G, B uint8
}

// VNCSession is the slice of the real session the `vnc` JS object drives.
// Exported as an interface so the binding tests run against a fake through the
// OpenVNCSession seam: CI has no VNC server.
//
//nolint:interfacebloat // mirrors the documented script surface
type VNCSession interface {
	// WaitForStable blocks until no framebuffer update has arrived for quiet.
	WaitForStable(ctx context.Context, quiet time.Duration) error
	// WaitForChange blocks until an update arrives after the call started (or
	// the timeout expires: ErrChangeTimedOut).
	WaitForChange(ctx context.Context, timeout time.Duration) error
	// Click / RightClick / DoubleClick / Move drive the pointer.
	Click(x, y int) error
	RightClick(x, y int) error
	DoubleClick(x, y int) error
	Move(x, y int) error
	// Type sends text as one KeyEvent down/up per rune.
	Type(text string) error
	// Key presses one named key or combo ("enter", "ctrl+alt+delete").
	Key(key string) error
	// Pixel reads one pixel as RGB.
	Pixel(x, y int) (PixelColor, error)
	// RegionHash hashes a rectangle of the framebuffer.
	RegionHash(x, y, w, h int) (uint64, error)
	// ScreenshotPNG renders the desktop as PNG bytes.
	ScreenshotPNG() ([]byte, error)
	// End closes the connection (VNC has no logoff concept).
	End()
	// Closed reports whether the session is gone.
	Closed() bool
}

// OpenVNCSession is the seam `vnc.connect` goes through. Production points at
// the real path; tests replace it. conn may be nil (dial the target).
//
//nolint:gochecknoglobals // test seam, mirrors OpenRDPSession
var OpenVNCSession = func(ctx context.Context, cfg *VNCConfig, conn net.Conn) (VNCSession, error) {
	s, err := openVNCSession(ctx, cfg, conn)
	if err != nil {
		return nil, err
	}

	return s, nil
}

// Infra reports whether err means the session infrastructure is gone, as
// opposed to a target verdict or a script bug: the JS runtime throws
// infrastructure errors and returns the others as `{ ok: false }`.
func Infra(err error) bool {
	return errors.Is(err, ErrSessionDead)
}

type vncSession struct {
	conn   net.Conn
	reader *bufio.Reader
	width  uint16
	height uint16

	writeMu sync.Mutex

	mu         sync.Mutex
	shown      *image.RGBA
	lastUpdate time.Time
	closed     bool
	readErr    error

	stopCtx func() bool
}

// openVNCSession dials (unless conn is supplied), handshakes, authenticates,
// starts the update loop and waits for the first frame.
func openVNCSession(ctx context.Context, cfg *VNCConfig, conn net.Conn) (*vncSession, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	openCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialed := conn == nil
	if dialed {
		port := cfg.Port
		if port == 0 {
			port = defaultPort
		}

		dialed, err := dialTarget(openCtx, cfg.Host, port, map[string]any{})
		if err != nil {
			return nil, failure(FailureConnection, err, "connection failed: %v", err)
		}

		conn = dialed
	}

	if deadline, ok := openCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	var redial redialFunc
	if dialed {
		redial = sessionRedial(openCtx, cfg)
	}

	session, err := negotiate(openCtx, conn, cfg, redial)
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	// The open deadline no longer applies: the runtime budget bounds every
	// call, and the parent context closes the connection.
	_ = session.conn.SetDeadline(time.Time{})
	session.stopCtx = context.AfterFunc(ctx, session.End)

	go session.readLoop()

	if err := session.waitFirstFrame(openCtx); err != nil {
		session.End()

		return nil, err
	}

	return session, nil
}

// sessionRedial is the fallback dialer of a session that dialed itself.
func sessionRedial(ctx context.Context, cfg *VNCConfig) redialFunc {
	return func() (net.Conn, *handshakeResult, error) {
		port := cfg.Port
		if port == 0 {
			port = defaultPort
		}

		fresh, err := dialTarget(ctx, cfg.Host, port, map[string]any{})
		if err != nil {
			return nil, nil, failure(FailureConnection, err, "connection failed: %v", err)
		}

		buffered := newBufferedConn(fresh)

		offer, err := handshake(buffered)
		if err != nil {
			_ = fresh.Close()

			return nil, nil, classifyHandshakeError(err)
		}

		return buffered, offer, nil
	}
}

// negotiate runs handshake, authentication and ServerInit, and sends the
// initial pixel format, encodings and full-screen request.
func negotiate(ctx context.Context, conn net.Conn, cfg *VNCConfig, redial redialFunc) (*vncSession, error) {
	buffered := newBufferedConn(conn)

	offer, err := handshake(buffered)
	if err != nil {
		return nil, classifyHandshakeError(err)
	}

	if cfg.RequiresAuth() && offer.offers(secNone) {
		return nil, failure(FailureNoAuthOffered, nil,
			"server offers security type None: anyone can attach without a password (offered: %s)",
			describeSecurityTypes(offer.securityTypes))
	}

	outcome, err := secure(ctx, buffered, offer, cfg, redial)
	if err != nil {
		return nil, err
	}

	// After VeNCrypt the stream is TLS: buffer its plaintext side instead.
	stream, ok := outcome.conn.(*bufferedConn)
	if !ok {
		stream = newBufferedConn(outcome.conn)
	}

	session, err := startSession(stream)
	if err != nil {
		// secure may have redialed or wrapped in TLS: close what it left.
		_ = stream.Close()

		return nil, err
	}

	return session, nil
}

// startSession runs ClientInit/ServerInit on the authenticated stream and
// sends the initial pixel format, encodings and full-screen request.
func startSession(stream *bufferedConn) (*vncSession, error) {
	init, err := clientServerInit(stream)
	if err != nil {
		return nil, err
	}

	if int(init.width)*int(init.height) > maxFramePixels || init.width == 0 || init.height == 0 {
		return nil, failure(FailureProtocol, nil, "unsupported framebuffer %dx%d", init.width, init.height)
	}

	if err := requestFrame(stream, init.width, init.height); err != nil {
		return nil, err
	}

	return &vncSession{
		conn: stream, reader: stream.reader, width: init.width, height: init.height,
		shown: image.NewRGBA(image.Rect(0, 0, int(init.width), int(init.height))),
	}, nil
}

// readLoop paints updates until the connection ends.
func (s *vncSession) readLoop() {
	work := image.NewRGBA(s.shown.Bounds())

	for {
		var msgType [1]byte
		if _, err := s.reader.Read(msgType[:]); err != nil {
			s.fail(err)

			return
		}

		if err := s.handleMessage(msgType[0], work); err != nil {
			s.fail(err)

			return
		}
	}
}

func (s *vncSession) handleMessage(msgType byte, work *image.RGBA) error {
	switch msgType {
	case msgFramebufferUpdate:
		var dirty image.Rectangle

		if _, err := readFramebufferUpdate(s.reader, work, &dirty); err != nil {
			return err
		}

		s.publish(work, dirty)

		return s.requestIncremental()
	case msgSetColorMapEntries:
		return skipColorMap(s.reader)
	case msgBell:
	case msgServerCutText:
		return skipCutText(s.reader)
	default:
		return failure(FailureProtocol, nil, "unexpected server message type %d", msgType)
	}

	return nil
}

// publish copies the painted area into the visible frame and stamps activity.
func (s *vncSession) publish(work *image.RGBA, dirty image.Rectangle) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !dirty.Empty() {
		for y := dirty.Min.Y; y < dirty.Max.Y; y++ {
			from := work.PixOffset(dirty.Min.X, y)
			to := from + dirty.Dx()*bytesPerPixel
			copy(s.shown.Pix[from:to], work.Pix[from:to])
		}
	}

	if !dirty.Empty() {
		s.lastUpdate = time.Now()
	}
}

func (s *vncSession) requestIncremental() error {
	msg := []byte{msgFramebufferUpdateRequest, 1}
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, s.width)
	msg = binary.BigEndian.AppendUint16(msg, s.height)

	return s.write(msg)
}

func (s *vncSession) write(msg []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.Closed() {
		return ErrSessionDead
	}

	if _, err := s.conn.Write(msg); err != nil {
		return fmt.Errorf("%w: %w", ErrSessionDead, err)
	}

	return nil
}

// fail records the read error and tears the connection down.
func (s *vncSession) fail(err error) {
	s.mu.Lock()
	if s.readErr == nil && !s.closed {
		s.readErr = err
	}
	s.mu.Unlock()

	s.End()
}

func (s *vncSession) waitFirstFrame(ctx context.Context) error {
	tick := time.NewTicker(pollInterval / 5)
	defer tick.Stop()

	for {
		s.mu.Lock()
		got, readErr := !s.lastUpdate.IsZero(), s.readErr
		s.mu.Unlock()

		switch {
		case got:
			return nil
		case readErr != nil:
			var classified *FailureError
			if errors.As(readErr, &classified) {
				return classified
			}

			return failure(FailureNoFrame, readErr, "no frame received: %v", readErr)
		}

		select {
		case <-ctx.Done():
			return failure(FailureNoFrame, ctx.Err(), "no frame received in time: %v", ctx.Err())
		case <-tick.C:
		}
	}
}

// End closes the connection. Idempotent.
func (s *vncSession) End() {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()

	if already {
		return
	}

	if s.stopCtx != nil {
		s.stopCtx()
	}

	_ = s.conn.Close()
}

// Closed reports whether the session was ended or dropped.
func (s *vncSession) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

func (s *vncSession) lastUpdateTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastUpdate
}

// WaitForStable blocks until no update arrived for quiet.
func (s *vncSession) WaitForStable(ctx context.Context, quiet time.Duration) error {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		if s.Closed() {
			return ErrSessionDead
		}

		if last := s.lastUpdateTime(); !last.IsZero() && time.Since(last) >= quiet {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("screen never stabilized: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// WaitForChange blocks until an update arrives after the call started.
func (s *vncSession) WaitForChange(ctx context.Context, timeout time.Duration) error {
	baseline := s.lastUpdateTime()
	deadline := time.Now().Add(timeout)

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		if s.lastUpdateTime().After(baseline) {
			return nil
		}

		if s.Closed() {
			return ErrSessionDead
		}

		if !time.Now().Before(deadline) {
			return ErrChangeTimedOut
		}

		select {
		case <-ctx.Done():
			return ErrChangeTimedOut
		case <-tick.C:
		}
	}
}

func (s *vncSession) pointer(mask uint8, posX, posY int) error {
	if posX < 0 || posY < 0 || posX >= int(s.width) || posY >= int(s.height) {
		return fmt.Errorf("point (%d, %d) %w (%dx%d)", posX, posY, ErrOutsideDesktop, s.width, s.height)
	}

	msg := []byte{msgPointerEvent, mask}
	msg = binary.BigEndian.AppendUint16(msg, uint16(posX))
	msg = binary.BigEndian.AppendUint16(msg, uint16(posY))

	return s.write(msg)
}

func (s *vncSession) press(button uint8, posX, posY int, times int) error {
	for range times {
		if err := s.pointer(button, posX, posY); err != nil {
			return err
		}

		if err := s.pointer(0, posX, posY); err != nil {
			return err
		}
	}

	return nil
}

// Click presses and releases the left button at (x, y).
func (s *vncSession) Click(x, y int) error { return s.press(buttonLeft, x, y, 1) }

// RightClick presses and releases the right button at (x, y).
func (s *vncSession) RightClick(x, y int) error { return s.press(buttonRight, x, y, 1) }

// DoubleClick is two press/release pairs.
func (s *vncSession) DoubleClick(x, y int) error { return s.press(buttonLeft, x, y, 2) }

// Move moves the pointer with no button held.
func (s *vncSession) Move(x, y int) error { return s.pointer(0, x, y) }

func (s *vncSession) keyEvent(down bool, keysym uint32) error {
	flag := byte(0)
	if down {
		flag = 1
	}

	msg := []byte{msgKeyEvent, flag, 0, 0}
	msg = binary.BigEndian.AppendUint32(msg, keysym)

	return s.write(msg)
}

// runeKeysym maps a rune to its X keysym: Latin-1 is identity, the rest is the
// Unicode keysym range; newline and tab map to Return and Tab.
func runeKeysym(char rune) uint32 {
	switch {
	case char == '\n' || char == '\r':
		return 0xff0d
	case char == '\t':
		return 0xff09
	case char <= latin1Max:
		return uint32(char)
	default:
		return keysymUnicodeBase + uint32(char)
	}
}

// Type sends one KeyEvent down/up per rune.
func (s *vncSession) Type(text string) error {
	for _, r := range text {
		keysym := runeKeysym(r)

		if err := s.keyEvent(true, keysym); err != nil {
			return err
		}

		if err := s.keyEvent(false, keysym); err != nil {
			return err
		}
	}

	return nil
}

// keysymTable maps the key names Key accepts onto X keysyms.
//
//nolint:gochecknoglobals // constant vocabulary
var keysymTable = map[string]uint32{
	"enter": 0xff0d, "return": 0xff0d, "esc": 0xff1b, "escape": 0xff1b,
	"backspace": 0xff08, "tab": 0xff09, "space": 0x20,
	"ctrl": 0xffe3, "control": 0xffe3, "alt": 0xffe9, "shift": 0xffe1,
	"win": 0xffeb, "super": 0xffeb, "meta": 0xffeb, "cmd": 0xffeb,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
	"pgup": 0xff55, "pageup": 0xff55, "pgdn": 0xff56, "pagedown": 0xff56,
	"home": 0xff50, "end": 0xff57, "insert": 0xff63,
	"delete": 0xffff, "del": 0xffff, "capslock": 0xffe5,
}

// parseKeyCombo splits "ctrl+alt+delete" and maps each name to a keysym.
func parseKeyCombo(combo string) ([]uint32, error) {
	var out []uint32

	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if keysym, ok := keysymTable[part]; ok {
			out = append(out, keysym)

			continue
		}

		if n, ok := functionKey(part); ok {
			out = append(out, n)

			continue
		}

		if runes := []rune(part); len(runes) == 1 {
			out = append(out, runeKeysym(runes[0]))

			continue
		}

		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, part)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, combo)
	}

	return out, nil
}

// functionKey parses f1..f12.
func functionKey(name string) (uint32, bool) {
	var n int
	if _, err := fmt.Sscanf(name, "f%d", &n); err != nil || n < 1 || n > 12 || fmt.Sprintf("f%d", n) != name {
		return 0, false
	}

	return 0xffbe + uint32(n-1), true
}

// Key holds every key but the last, presses the last, then releases the
// modifiers in reverse order.
func (s *vncSession) Key(key string) error {
	keysyms, err := parseKeyCombo(key)
	if err != nil {
		return err
	}

	for _, keysym := range keysyms {
		if err := s.keyEvent(true, keysym); err != nil {
			return err
		}
	}

	for i := len(keysyms) - 1; i >= 0; i-- {
		if err := s.keyEvent(false, keysyms[i]); err != nil {
			return err
		}
	}

	return nil
}

// Pixel reads one pixel.
func (s *vncSession) Pixel(posX, posY int) (PixelColor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !image.Pt(posX, posY).In(s.shown.Bounds()) {
		return PixelColor{}, fmt.Errorf("pixel (%d, %d) %w (%dx%d)", posX, posY, ErrOutsideDesktop, s.width, s.height)
	}

	off := s.shown.PixOffset(posX, posY)

	return PixelColor{R: s.shown.Pix[off], G: s.shown.Pix[off+1], B: s.shown.Pix[off+2]}, nil
}

// RegionHash is an FNV-1a hash over the rectangle's RGBA bytes.
func (s *vncSession) RegionHash(left, top, width, height int) (uint64, error) {
	if width <= 0 || height <= 0 {
		return 0, fmt.Errorf("%w, got %dx%d", ErrRegionEmpty, width, height)
	}

	region := image.Rect(left, top, left+width, top+height)

	s.mu.Lock()
	defer s.mu.Unlock()

	if !region.In(s.shown.Bounds()) {
		return 0, fmt.Errorf("region %v %w (%dx%d)", region, ErrOutsideDesktop, s.width, s.height)
	}

	hasher := fnv.New64a()

	for row := top; row < top+height; row++ {
		off := s.shown.PixOffset(left, row)
		_, _ = hasher.Write(s.shown.Pix[off : off+width*bytesPerPixel])
	}

	return hasher.Sum64(), nil
}

// ScreenshotPNG renders the current frame.
func (s *vncSession) ScreenshotPNG() ([]byte, error) {
	s.mu.Lock()
	snapshot := image.NewRGBA(s.shown.Bounds())
	copy(snapshot.Pix, s.shown.Pix)
	s.mu.Unlock()

	return encodePNG(snapshot)
}

var _ VNCSession = (*vncSession)(nil)
