package checkbrowser

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestForcedCaptureKeepsAnyVerdict pins "Capture now" (spec 2026-09-25-34): an
// on-demand run keeps its capture whatever the verdict and whatever the
// check's `screenshot` toggle says — except StatusError, where there is no
// browser and so no page. The unforced rows are the positive control: the same
// checker, the same verdicts, and no capture, so the forced rows cannot pass
// because captures started happening everywhere.
//
//nolint:paralleltest // mutates the process-wide settings
func TestForcedCaptureKeepsAnyVerdict(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	cases := []struct {
		name    string
		forced  bool
		enabled bool
		status  checkerdef.Status
		want    bool
	}{
		{"forced, toggle off, up", true, false, checkerdef.StatusUp, true},
		{"forced, toggle on, up", true, true, checkerdef.StatusUp, true},
		{"forced, toggle off, down", true, false, checkerdef.StatusDown, true},
		{"forced, infrastructure error", true, true, checkerdef.StatusError, false},
		{"not forced, toggle on, up", false, true, checkerdef.StatusUp, false},
		{"not forced, toggle off, down", false, false, checkerdef.StatusDown, false},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			calls := 0
			checker := screenshotChecker(tc.status, func(context.Context) (Capture, error) {
				calls++

				return fakeCapture(), nil
			})

			ctx := t.Context()
			if tc.forced {
				ctx = checkerdef.WithForcedCapture(ctx)
			}

			result, err := checker.Execute(ctx, screenshotSpec(tc.enabled))
			r.NoError(err)
			r.Equal(tc.status, result.Status, "a forced capture must never change the verdict")

			if !tc.want {
				r.Zero(calls)

				if result.Diagnostics != nil {
					r.Nil(result.Diagnostics.Screenshot)
				}

				return
			}

			r.Equal(1, calls)
			r.NotNil(result.Diagnostics)
			r.NotNil(result.Diagnostics.Screenshot)
			r.Equal(fakeCapture().Image, result.Diagnostics.Screenshot.Image)
		})
	}
}
