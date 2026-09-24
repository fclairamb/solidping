package checkrdp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	grdp "github.com/fclairamb/solidping/server/third_party/grdp"
	"github.com/stretchr/testify/require"
)

// TestParseKeyCombo covers combo parsing: single named keys, modifier
// combos, case tolerance, and the unknown-key error.
func TestParseKeyCombo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sc, err := parseKeyCombo("enter")
	r.NoError(err)
	r.Equal([]uint16{0x001C}, sc)

	sc, err = parseKeyCombo("Win + R")
	r.NoError(err)
	r.Equal([]uint16{0xE05B, 0x0013}, sc)

	sc, err = parseKeyCombo("CTRL+Alt+End")
	r.NoError(err)
	r.Equal([]uint16{0x001D, 0x0038, 0xE04F}, sc)

	_, err = parseKeyCombo("ctrl+f13")
	r.ErrorContains(err, "unknown key")
}

func TestKeyComboSendsHoldPressReleaseSequence(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	client := &recordingClient{}
	s := &rdpSession{client: client}

	r.NoError(s.Key("ctrl+alt+end"))

	joined := strings.Join(client.calls, ";")
	// Hold ctrl, hold alt, press+release end (extended — the 0xE0 prefix
	// reaches the fake verbatim; the real client strips it into the flag),
	// release alt, release ctrl.
	r.Equal(
		"sc(0x1d,press);sc(0x38,press);sc(0xe04f,press);sc(0xe04f,release);sc(0x38,release);sc(0x1d,release)",
		joined,
	)

	r.NoError(s.Key("enter"))

	r.Len(client.calls, 8)
	// A bare key is a press+release pair.
	r.Equal("sc(0x1c,press);sc(0x1c,release)",
		strings.Join(client.calls[len(client.calls)-2:], ";"),
	)
}

// TestPixelAndRegionHashBounds pins that out-of-bounds reads are errors, not
// silent zeros, and that regionHash is stable across repeated calls.
type fbCase struct {
	name string
	x, y int
	w, h int
	err  bool
}

func TestPixelAndRegionHashBounds(t *testing.T) {
	t.Parallel()

	s := &rdpSession{frame: newFrameBuffer(64, 32)}

	r := require.New(t)

	color, err := s.Pixel(10, 10)
	r.NoError(err)
	r.Equal(PixelColor{}, color, "the blank framebuffer is black")

	_, err = s.Pixel(64, 0)
	r.ErrorContains(err, "outside")
	_, err = s.Pixel(-1, 0)
	r.Error(err)

	hash, err := s.RegionHash(0, 0, 64, 32)
	r.NoError(err)
	r.NotZero(hash)

	hash2, err := s.RegionHash(0, 0, 64, 32)
	r.NoError(err)
	r.Equal(hash, hash2, "same pixels -> same hash (stability)")

	_, err = s.RegionHash(0, 0, 0, 0)
	r.ErrorContains(err, "at least 1x1")

	_, err = s.RegionHash(0, 0, 65, 32)
	r.ErrorContains(err, "outside")
}

// TestRegionHashStableAcrossBuffers pins cross-run stability: two frame
// buffers with the same visible pixels hash identically, and a single changed
// pixel changes the hash.
func TestRegionHashStableAcrossBuffers(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	a := newFrameBuffer(32, 32)
	b := newFrameBuffer(32, 32)

	sa := &rdpSession{frame: a}
	sb := &rdpSession{frame: b}

	ha, err := sa.RegionHash(0, 0, 32, 32)
	r.NoError(err)
	hb, err := sb.RegionHash(0, 0, 32, 32)
	r.NoError(err)
	r.Equal(ha, hb)

	// One changed pixel changes the hash.
	b.mu.Lock()
	off := b.img.PixOffset(5, 5)
	b.img.Pix[off], b.img.Pix[off+1], b.img.Pix[off+2] = 1, 2, 3
	b.mu.Unlock()

	hb2, err := sb.RegionHash(0, 0, 32, 32)
	r.NoError(err)
	r.NotEqual(ha, hb2)
}

// TestWaitForStableBounded pins that the settle wait honors the caller's
// context: a hung logon (no bitmap ever) ends at the deadline as
// logon_timed_out.
func TestWaitForStableBounded(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := &rdpSession{frame: newFrameBuffer(16, 16)}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := s.WaitForStable(ctx, stableQuiet)
	r.Error(err)

	var authErr *ErrAuthFailure
	r.True(errors.As(err, &authErr))
	r.Equal(AuthTimedOut, authErr.Reason)

	// A settled session returns immediately.
	s.recordBitmapTime()
	time.Sleep(50 * time.Millisecond)
	s.recordBitmapTime()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()

	r.NoError(s.WaitForStable(ctx2, stableQuiet))
}

// TestWaitForChangePinsBaseline pins waitForChange: a change after the
// baseline returns; silence returns ErrChangeTimedOut.
func TestWaitForChangePinsBaseline(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s := &rdpSession{frame: newFrameBuffer(32, 32)}
	s.recordBitmapTime()

	// No change within the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	r.ErrorIs(s.WaitForChange(ctx, 150*time.Millisecond), ErrChangeTimedOut)

	// A bitmap arriving after the baseline satisfies the wait.
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.recordBitmapTime()
	}()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()

	r.NoError(s.WaitForChange(ctx2, 2*time.Second))
}

// TestInfraClassification pins the throw-vs-return split for the JS runtime.
func TestInfraClassification(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(Infra(ErrNotReady), "a dead session is infrastructure")
	r.False(Infra(nil))
	r.False(Infra(ErrChangeTimedOut), "a change timeout is a value")
	r.False(Infra(ErrSlotTimeout), "a slot timeout is a timeout verdict")
	r.False(Infra(authFailuref(AuthRejected, nil)), "auth rejection is a target verdict")
	r.False(Infra(context.DeadlineExceeded), "the check's own timeout is not infra")
}

// TestClassifyLoginError pins the distinct failure codes for login errors.
func TestClassifyLoginError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var authErr *ErrAuthFailure

	err := classifyLoginError(errors.New("[x224 connect err] CredSSP: logon failure"))
	r.ErrorAs(err, &authErr)
	r.Equal(AuthRejected, authErr.Reason)

	err = classifyLoginError(context.DeadlineExceeded)
	r.ErrorAs(err, &authErr)
	r.Equal(AuthTimedOut, authErr.Reason)

	err = classifyLoginError(errors.New("[connection timeout]"))
	r.ErrorAs(err, &authErr)
	r.Equal(AuthTimedOut, authErr.Reason)

	err = classifyLoginError(errors.New("[dial err] connection refused"))
	r.ErrorAs(err, &authErr)
	r.Equal(AuthServerDropped, authErr.Reason)
}

// recordingClient records the input sequence s.Key() / s.Type() drive. Only
// EventReady (always true) and SendScancode matter for the sequence test.
type recordingClient struct {
	grdp.RdpClient //nolint:typecheck // shape; every driven method is overridden
	calls          []string
}

func (r *recordingClient) EventReady() bool { return true }

func (r *recordingClient) SendScancode(sc uint16, release bool) {
	state := "press"
	if release {
		state = "release"
	}

	hex := fmt.Sprintf("0x%x", sc)

	r.calls = append(r.calls, "sc("+hex+","+state+")")
}

func (r *recordingClient) SendUnicodeKey(_ rune) {}

func (r *recordingClient) MouseMove(_, _ int) {}

func (r *recordingClient) MouseDown(_, _, _ int) {}

func (r *recordingClient) MouseUp(_, _, _ int) {}

func (r *recordingClient) Close() {}
