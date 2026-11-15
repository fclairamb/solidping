package checkbrowser

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/chromedp/cdproto"
	"github.com/stretchr/testify/require"
)

// errPlainDetachedText carries the CDP wording without being a CDP error.
var errPlainDetachedText = errors.New("plain: Not attached to an active page")

// TestCaptureErrorMessageNamesADetachedPage: a capture whose page closed or
// crashed first fails with CDP -32000 "Not attached to an active page". That
// is the monitored page's doing, so the operator reads a plain sentence rather
// than the raw protocol error, and the log is not raised as a worker fault.
func TestCaptureErrorMessageNamesADetachedPage(t *testing.T) {
	t.Parallel()

	detached := &cdproto.Error{Code: cdpServerError, Message: "Not attached to an active page"}
	gone := &cdproto.Error{Code: cdpServerError, Message: "Session with given id not found"}
	other := &cdproto.Error{Code: cdpServerError, Message: "Internal error"}

	cases := []struct {
		name     string
		err      error
		detached bool
		message  string
	}{
		{"detached", detached, true, "the page was closed or crashed before the capture"},
		{"wrapped", fmt.Errorf("capture: %w", detached), true, "the page was closed or crashed before the capture"},
		{"session gone", gone, true, "the page was closed or crashed before the capture"},
		{"other cdp error", other, false, other.Error()},
		{"plain error", errRendererGone, false, errRendererGone.Error()},
		{"deadline", context.DeadlineExceeded, false, "the capture timed out"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(tc.detached, pageDetached(tc.err))
			r.Equal(tc.message, CaptureErrorMessage(tc.err, 0))
		})
	}

	r := require.New(t)
	r.False(pageDetached(nil))
	r.False(pageDetached(errPlainDetachedText))
}
