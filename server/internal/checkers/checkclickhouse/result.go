package checkclickhouse

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// failure builds the down/timeout result for a failed stage. A cancelled
// context means the deadline (not the server) ended the check, so it reports
// timeoutMessage as StatusTimeout; anything else is a genuine failure of the
// target and reports downMessage as StatusDown.
//
// The diagnostic output built by Execute (host, port, database, tunneled…) is
// carried into the failure too: for a tunneled check, knowing the probe went
// through a bastion is exactly what a failed result needs to be actionable.
func failure(
	ctx context.Context,
	start time.Time,
	metrics, output map[string]any,
	downMessage, timeoutMessage string,
) *checkerdef.Result {
	message, status := downMessage, checkerdef.StatusDown
	if ctx.Err() != nil {
		message, status = timeoutMessage, checkerdef.StatusTimeout
	}

	output[checkerdef.OutputKeyError] = message

	return &checkerdef.Result{
		Status:   status,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
