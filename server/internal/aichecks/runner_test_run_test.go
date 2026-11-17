package aichecks_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
)

// shotSession is a browser that only knows how to be photographed.
type shotSession struct{ checkjs.BrowserSession }

func (shotSession) Screenshot(context.Context) (checkbrowser.Capture, error) {
	return checkbrowser.Capture{Image: []byte("png-bytes"), Format: checkerdef.ImageFormatPNG}, nil
}

func (shotSession) Close() {}

// TestRunnerTestKeepsTheCapture: the test-run endpoint serves the capture the
// script took, which the authoring loop's Run never carries.
//
//nolint:paralleltest // swaps the process-wide OpenBrowser seam
func TestRunnerTestKeepsTheCapture(t *testing.T) {
	previous := checkjs.OpenBrowser
	checkjs.OpenBrowser = func(context.Context) (checkjs.BrowserSession, error) { return shotSession{}, nil }

	t.Cleanup(func() { checkjs.OpenBrowser = previous })

	script := `var page = browser.open(); page.screenshot(); return { status: "down" };`

	res, err := (&aichecks.Runner{}).Test(t.Context(), script, nil, nil, 10*time.Second)
	require.NoError(t, err)
	require.Equal(t, "down", res.Status)
	require.NotNil(t, res.Screenshot)
	require.Equal(t, []byte("png-bytes"), res.Screenshot.Image)
}

func TestRunnerTestRejectsAnInvalidScript(t *testing.T) {
	t.Parallel()

	_, err := (&aichecks.Runner{}).Test(t.Context(), "", nil, nil, time.Second)
	require.Error(t, err)
}
