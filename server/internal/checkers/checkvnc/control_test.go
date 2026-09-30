package checkvnc

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clientEvent is one input message the fake server received.
type clientEvent struct {
	kind   byte
	down   bool
	keysym uint32
	mask   byte
	x, y   uint16
}

const (
	testW = 8
	testH = 4
)

// rawUpdate builds a FramebufferUpdate with one Raw rectangle filled with one
// color (B, G, R, X order on the wire).
func rawUpdate(x, y, w, h uint16, blue, green, red byte) []byte {
	out := make([]byte, 0, 16+int(w)*int(h)*bytesPerPixel)
	out = append(out, msgFramebufferUpdate, 0)
	out = append(out, u16(1)...)
	out = append(out, u16(x)...)
	out = append(out, u16(y)...)
	out = append(out, u16(w)...)
	out = append(out, u16(h)...)
	out = append(out, u32(uint32(encodingRaw))...)

	for range int(w) * int(h) {
		out = append(out, blue, green, red, 0)
	}

	return out
}

// controlServer speaks None-auth RFB, sends a first all-black frame, then
// records every KeyEvent/PointerEvent. Extra frames are pushed on push.
func controlServer(events chan<- clientEvent, push <-chan []byte) func(net.Conn) error {
	return func(conn net.Conn) error {
		steps := []func() error{
			func() error { return writeAll(conn, []byte("RFB 003.008\n")) },
			func() error { _, err := readN(conn, bannerLen); return err },
			func() error { return writeAll(conn, []byte{1, secNone}) },
			func() error { _, err := readN(conn, 1); return err },
			func() error { return writeAll(conn, u32(0)) },
			func() error { _, err := readN(conn, 1); return err },
			func() error { return writeAll(conn, serverInitBytes(testW, testH, "acme")) },
			func() error { _, err := readN(conn, 20+12+10); return err },
			func() error { return writeAll(conn, rawUpdate(0, 0, testW, testH, 0, 0, 0)) },
		}

		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}

		go func() {
			for update := range push {
				_, _ = conn.Write(update)
			}
		}()

		return recordEvents(conn, events)
	}
}

// recordEvents reads client messages until the connection closes.
func recordEvents(conn net.Conn, events chan<- clientEvent) error {
	for {
		head, err := readN(conn, 1)
		if err != nil {
			return nil //nolint:nilerr // the client closing ends the script
		}

		switch head[0] {
		case msgFramebufferUpdateRequest:
			if _, err = readN(conn, 9); err != nil {
				return nil //nolint:nilerr // closed
			}
		case msgKeyEvent:
			body, readErr := readN(conn, 7)
			if readErr != nil {
				return nil //nolint:nilerr // closed
			}

			events <- clientEvent{kind: msgKeyEvent, down: body[0] == 1, keysym: binary.BigEndian.Uint32(body[3:7])}
		case msgPointerEvent:
			body, readErr := readN(conn, 5)
			if readErr != nil {
				return nil //nolint:nilerr // closed
			}

			events <- clientEvent{
				kind: msgPointerEvent, mask: body[0],
				x: binary.BigEndian.Uint16(body[1:3]), y: binary.BigEndian.Uint16(body[3:5]),
			}
		default:
			return fmt.Errorf("%w: client message %d", errFakeServer, head[0])
		}
	}
}

func openControlSession(t *testing.T, push <-chan []byte) (VNCSession, <-chan clientEvent) {
	t.Helper()

	events := make(chan clientEvent, 64)
	host, port, _ := fakeServer(t, controlServer(events, push))

	session, err := OpenVNCSession(context.Background(), &VNCConfig{
		Host: host, Port: port, Password: "pw", RequireAuth: boolPtr(false), Timeout: 3 * time.Second,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(session.End)

	return session, events
}

func drain(events <-chan clientEvent, n int) []clientEvent {
	out := make([]clientEvent, 0, n)

	for range n {
		select {
		case ev := <-events:
			out = append(out, ev)
		case <-time.After(3 * time.Second):
			return out
		}
	}

	return out
}

func TestSessionTypeSendsKeysyms(t *testing.T) {
	t.Parallel()

	session, events := openControlSession(t, nil)
	require.NoError(t, session.Type("é€"))

	got := drain(events, 4)
	require.Equal(t, []clientEvent{
		{kind: msgKeyEvent, down: true, keysym: 0x00e9},
		{kind: msgKeyEvent, down: false, keysym: 0x00e9},
		{kind: msgKeyEvent, down: true, keysym: 0x010020ac},
		{kind: msgKeyEvent, down: false, keysym: 0x010020ac},
	}, got)
}

func TestSessionKeyCombo(t *testing.T) {
	t.Parallel()

	session, events := openControlSession(t, nil)
	require.NoError(t, session.Key("ctrl+alt+delete"))

	const ctrl, alt, del = 0xffe3, 0xffe9, 0xffff

	require.Equal(t, []clientEvent{
		{kind: msgKeyEvent, down: true, keysym: ctrl},
		{kind: msgKeyEvent, down: true, keysym: alt},
		{kind: msgKeyEvent, down: true, keysym: del},
		{kind: msgKeyEvent, down: false, keysym: del},
		{kind: msgKeyEvent, down: false, keysym: alt},
		{kind: msgKeyEvent, down: false, keysym: ctrl},
	}, drain(events, 6))

	require.ErrorIs(t, session.Key("ctrl+bogus"), ErrUnknownKey)
}

func TestSessionKeyNames(t *testing.T) {
	t.Parallel()

	got, err := parseKeyCombo("Enter")
	require.NoError(t, err)
	require.Equal(t, []uint32{0xff0d}, got)

	got, err = parseKeyCombo("f12")
	require.NoError(t, err)
	require.Equal(t, []uint32{0xffc9}, got)

	_, err = parseKeyCombo("f13")
	require.ErrorIs(t, err, ErrUnknownKey)
}

func TestSessionPointer(t *testing.T) {
	t.Parallel()

	session, events := openControlSession(t, nil)
	require.NoError(t, session.Click(3, 2))
	require.NoError(t, session.RightClick(1, 1))
	require.NoError(t, session.DoubleClick(5, 3))
	require.NoError(t, session.Move(0, 0))

	require.Equal(t, []clientEvent{
		{kind: msgPointerEvent, mask: 1, x: 3, y: 2},
		{kind: msgPointerEvent, mask: 0, x: 3, y: 2},
		{kind: msgPointerEvent, mask: 4, x: 1, y: 1},
		{kind: msgPointerEvent, mask: 0, x: 1, y: 1},
		{kind: msgPointerEvent, mask: 1, x: 5, y: 3},
		{kind: msgPointerEvent, mask: 0, x: 5, y: 3},
		{kind: msgPointerEvent, mask: 1, x: 5, y: 3},
		{kind: msgPointerEvent, mask: 0, x: 5, y: 3},
		{kind: msgPointerEvent, mask: 0, x: 0, y: 0},
	}, drain(events, 9))

	require.ErrorIs(t, session.Click(testW, 0), ErrOutsideDesktop)
}

func TestSessionWaitForChangeTimesOut(t *testing.T) {
	t.Parallel()

	session, _ := openControlSession(t, nil)

	err := session.WaitForChange(context.Background(), 200*time.Millisecond)
	require.ErrorIs(t, err, ErrChangeTimedOut)
}

func TestSessionFrameUpdates(t *testing.T) {
	t.Parallel()

	push := make(chan []byte, 1)
	session, _ := openControlSession(t, push)

	require.NoError(t, session.WaitForStable(context.Background(), 100*time.Millisecond))

	before, err := session.RegionHash(0, 0, testW, testH)
	require.NoError(t, err)

	go func() {
		time.Sleep(150 * time.Millisecond)
		push <- rawUpdate(2, 1, 2, 2, 0, 0, 255) // red block
	}()

	require.NoError(t, session.WaitForChange(context.Background(), 3*time.Second))

	after, err := session.RegionHash(0, 0, testW, testH)
	require.NoError(t, err)
	require.NotEqual(t, before, after)

	color, err := session.Pixel(2, 1)
	require.NoError(t, err)
	require.Equal(t, PixelColor{R: 255}, color)

	color, err = session.Pixel(0, 0)
	require.NoError(t, err)
	require.Equal(t, PixelColor{}, color)

	_, err = session.Pixel(testW, 0)
	require.ErrorIs(t, err, ErrOutsideDesktop)

	_, err = session.RegionHash(0, 0, 0, 1)
	require.ErrorIs(t, err, ErrRegionEmpty)

	shot, err := session.ScreenshotPNG()
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(shot, []byte("\x89PNG")))
}

func TestSessionEndIsIdempotentAndBlocksInput(t *testing.T) {
	t.Parallel()

	session, _ := openControlSession(t, nil)
	require.False(t, session.Closed())

	session.End()
	session.End()
	require.True(t, session.Closed())
	require.True(t, Infra(session.Click(1, 1)))
}

func TestOpenSessionRefusesNoneWhenRequireAuth(t *testing.T) {
	t.Parallel()

	events := make(chan clientEvent, 1)
	host, port, _ := fakeServer(t, controlServer(events, nil))

	_, err := OpenVNCSession(context.Background(), &VNCConfig{
		Host: host, Port: port, Password: "pw", Timeout: 2 * time.Second,
	}, nil)

	var failed *FailureError
	require.ErrorAs(t, err, &failed)
	require.Equal(t, FailureNoAuthOffered, failed.Code)
}
