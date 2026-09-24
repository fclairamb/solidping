package checkjs

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkrdp"
	checkrdpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// fakeRDPSession records the calls a script made, the way the browser fake
// does, so every binding test asserts on the SEQUENCE the script drove.
type fakeRDPSession struct {
	mu    sync.Mutex
	calls []string

	stableErr error
	changeErr error
	inputErr  error
	pixel     checkrdp.PixelColor
	pixelErr  error
	hashErr   error

	screenshotN   int
	logoffErr     error
	endCalled     bool
	endDisconnect bool
}

func (f *fakeRDPSession) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *fakeRDPSession) WaitForStable(_ context.Context, quiet time.Duration) error {
	f.record("waitForStable")

	if f.stableErr != nil {
		return f.stableErr
	}

	time.Sleep(quiet)

	return nil
}

func (f *fakeRDPSession) WaitForChange(_ context.Context, _ time.Duration) error {
	f.record("waitForChange")

	return f.changeErr
}

func (f *fakeRDPSession) Click(x, y int) error {
	return f.input(fmt.Sprintf("click(%d,%d)", x, y))
}

func (f *fakeRDPSession) RightClick(x, y int) error {
	return f.input(fmt.Sprintf("rightClick(%d,%d)", x, y))
}

func (f *fakeRDPSession) DoubleClick(x, y int) error {
	return f.input(fmt.Sprintf("doubleClick(%d,%d)", x, y))
}

func (f *fakeRDPSession) Move(x, y int) error {
	return f.input(fmt.Sprintf("move(%d,%d)", x, y))
}

func (f *fakeRDPSession) Type(text string) error {
	return f.input("type(" + text + ")")
}

func (f *fakeRDPSession) Key(key string) error {
	return f.input("key(" + key + ")")
}

func (f *fakeRDPSession) Pixel(x, y int) (checkrdp.PixelColor, error) {
	f.record(fmt.Sprintf("pixel(%d,%d)", x, y))

	return f.pixel, f.pixelErr
}

func (f *fakeRDPSession) RegionHash(x, y, w, h int) (uint64, error) {
	f.record(fmt.Sprintf("regionHash(%d,%d,%d,%d)", x, y, w, h))

	return 0x1234, f.hashErr
}

func (f *fakeRDPSession) ScreenshotPNG() ([]byte, error) {
	f.screenshotN++
	f.record("screenshot")

	return []byte("fake-png"), nil
}

func (f *fakeRDPSession) EndLogoff() error {
	f.record("logoff")

	return f.logoffErr
}

func (f *fakeRDPSession) EndDisconnect() {
	f.endDisconnect = true
	f.record("disconnect")
}

// Closed reports whether the session was torn down. The fake flips endCalled
// only through an explicit end; the runtime's defer treats an open session as
// "needs a log off".
func (f *fakeRDPSession) Closed() bool { return f.endCalled }

// input records a driver call and returns the seeded input error.
func (f *fakeRDPSession) input(call string) error {
	f.record(call)

	return f.inputErr
}

// openRDPTestSession is the seam override a test installs: it always returns
// the ONE fake.
func openRDPTestSession(t *testing.T, fake *fakeRDPSession) {
	t.Helper()

	prev := checkrdp.OpenRDPSession
	t.Cleanup(func() { checkrdp.OpenRDPSession = prev })

	checkrdp.OpenRDPSession = func(
		_ context.Context, _ *checkrdpconfig.RDPConfig, _, _ int, _ net.Conn,
	) (checkrdp.RDPSession, error) {
		return fake, nil
	}
}
