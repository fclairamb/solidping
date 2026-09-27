package models_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestHonorOnDemand pins spec 2026-09-25-34's trust rule: an OnDemand marker
// survives only when the job's lease carried a "Capture now" request.
func TestHonorOnDemand(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	marked := func() *checkerdef.Diagnostics {
		return &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{OnDemand: true}}
	}

	unrequested := &models.CheckJob{}
	diag := marked()
	unrequested.HonorOnDemand(diag)
	r.False(diag.Screenshot.OnDemand, "no request on the lease: the marker is dropped")

	claimedAt := time.Now()
	requested := &models.CheckJob{CaptureClaimedAt: &claimedAt}
	diag = marked()
	requested.HonorOnDemand(diag)
	r.True(diag.Screenshot.OnDemand, "the lease carried a request: the marker stands")

	// Nil-safe on results with no capture.
	unrequested.HonorOnDemand(nil)
	unrequested.HonorOnDemand(&checkerdef.Diagnostics{})
}

// TestOnDemandFailure pins spec 2026-09-27-01's rule for when a result answers
// a "Capture now" request with a failure, and which reason it carries.
func TestOnDemandFailure(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, 9, 26, 22, 50, 31, 0, time.UTC)
	claimed := &models.CheckJob{CaptureClaimedAt: &requestedAt}
	outputErr := map[string]any{checkerdef.OutputKeyError: "cannot reach the remote Chrome (CDP) endpoint"}

	cases := []struct {
		name        string
		job         *models.CheckJob
		diagnostics *checkerdef.Diagnostics
		output      map[string]any
		failed      bool
		reason      string
	}{
		{"no request on the lease", &models.CheckJob{}, nil, outputErr, false, ""},
		{"no request, capture error", &models.CheckJob{}, &checkerdef.Diagnostics{ScreenshotError: "x"}, nil, false, ""},
		{
			"request answered with a capture", claimed,
			&checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{Available: true}}, nil, false, "",
		},
		{
			"checker's own reason wins", claimed,
			&checkerdef.Diagnostics{ScreenshotError: "the capture timed out after 5s"}, outputErr,
			true, "the capture timed out after 5s",
		},
		{"no browser: the run's error", claimed, nil, outputErr, true, "cannot reach the remote Chrome (CDP) endpoint"},
		{
			"nothing said at all", claimed, &checkerdef.Diagnostics{}, nil,
			true, "the run finished without taking a screenshot",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			at, reason, failed := tc.job.OnDemandFailure(tc.diagnostics, tc.output)
			r.Equal(tc.failed, failed)
			r.Equal(tc.reason, reason)

			if failed {
				r.True(at.Equal(requestedAt), "the failure names the request it answers")
			}
		})
	}

	// An agent's reason is bounded before it is stored.
	long := strings.Repeat("é", models.MaxCaptureFailureReasonLen+50)
	_, reason, failed := claimed.OnDemandFailure(&checkerdef.Diagnostics{ScreenshotError: long}, nil)
	require.True(t, failed)
	require.Len(t, []rune(reason), models.MaxCaptureFailureReasonLen)
}
