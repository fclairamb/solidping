package watchdog

import (
	"errors"
	"fmt"
)

// ErrRegionHealthUnavailable is reported by the dark-region detector when no
// RegionHealth reporter was wired. It is deliberately an ERROR rather than a
// silent skip: a watchdog that quietly stops looking at the one failure mode
// it was written for is worse than no watchdog.
var ErrRegionHealthUnavailable = errors.New("watchdog: region health reporter not wired")

// errDetectorPanicked wraps a recovered panic so a detector bug shows up as a
// detector failure (logged, metered, and excluded from resolution) instead of
// killing the run.
func errDetectorPanicked(recovered any) error {
	if err, ok := recovered.(error); ok {
		return fmt.Errorf("watchdog: detector panicked: %w", err)
	}

	return fmt.Errorf("watchdog: detector panicked: %v", recovered) //nolint:err113 // recovered value is not an error
}
