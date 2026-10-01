package checkjs

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkvnc"
)

// fakeVNCSession records the calls a script made so every binding test
// asserts on the sequence the script drove.
type fakeVNCSession struct {
	mu    sync.Mutex
	calls []string

	changeErr error
	inputErr  error
	pixel     checkvnc.PixelColor

	closed bool
}

func (f *fakeVNCSession) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *fakeVNCSession) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

func (f *fakeVNCSession) input(call string) error {
	f.record(call)

	return f.inputErr
}

func (f *fakeVNCSession) WaitForStable(_ context.Context, _ time.Duration) error {
	f.record("waitForStable")

	return nil
}

func (f *fakeVNCSession) WaitForChange(_ context.Context, _ time.Duration) error {
	f.record("waitForChange")

	return f.changeErr
}

func (f *fakeVNCSession) Click(x, y int) error {
	return f.input(fmt.Sprintf("click(%d,%d)", x, y))
}

func (f *fakeVNCSession) RightClick(x, y int) error {
	return f.input(fmt.Sprintf("rightClick(%d,%d)", x, y))
}

func (f *fakeVNCSession) DoubleClick(x, y int) error {
	return f.input(fmt.Sprintf("doubleClick(%d,%d)", x, y))
}

func (f *fakeVNCSession) Move(x, y int) error {
	return f.input(fmt.Sprintf("move(%d,%d)", x, y))
}

func (f *fakeVNCSession) Type(text string) error { return f.input("type(" + text + ")") }

func (f *fakeVNCSession) Key(key string) error { return f.input("key(" + key + ")") }

func (f *fakeVNCSession) Pixel(x, y int) (checkvnc.PixelColor, error) {
	f.record(fmt.Sprintf("pixel(%d,%d)", x, y))

	return f.pixel, nil
}

func (f *fakeVNCSession) RegionHash(x, y, w, h int) (uint64, error) {
	f.record(fmt.Sprintf("regionHash(%d,%d,%d,%d)", x, y, w, h))

	return 0x1234, nil
}

func (f *fakeVNCSession) ScreenshotPNG() ([]byte, error) {
	f.record("screenshot")

	return []byte("fake-png"), nil
}

func (f *fakeVNCSession) End() {
	f.mu.Lock()
	f.calls = append(f.calls, "end")
	f.closed = true
	f.mu.Unlock()
}

func (f *fakeVNCSession) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closed
}

// openVNCTestSession installs a seam that always returns the one fake, and
// counts how many times it was opened.
func openVNCTestSession(t *testing.T, fake *fakeVNCSession) *int {
	t.Helper()

	prev := checkvnc.OpenVNCSession
	t.Cleanup(func() { checkvnc.OpenVNCSession = prev })

	opened := 0
	checkvnc.OpenVNCSession = func(_ context.Context, _ *checkvnc.VNCConfig, _ net.Conn) (checkvnc.VNCSession, error) {
		opened++

		return fake, nil
	}

	return &opened
}
