package models_test

import (
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
