package checkworker

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
)

// isConnLoss reports whether err stems from a lost (or closing) agent
// connection: an expected transient state of a remote agent, not a server bug.
func isConnLoss(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, backend.ErrConnLost) ||
		errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

// connOutage tracks one ongoing connection outage. The first failure logs a
// WARN; later ones are counted silently; the first success afterwards logs a
// summary with the count.
type connOutage struct {
	lost  atomic.Bool
	count atomic.Int64
}

// active reports whether an outage is ongoing.
func (o *connOutage) active() bool { return o.lost.Load() }

func (o *connOutage) noteLoss(ctx context.Context, logger *slog.Logger, stage string, err error) {
	o.count.Add(1)
	if o.lost.CompareAndSwap(false, true) {
		logger.WarnContext(ctx, "Agent connection lost: jobs are left for re-claim",
			"stage", stage, "error", err)
	}
}

func (o *connOutage) noteRecovered(ctx context.Context, logger *slog.Logger) {
	if !o.lost.CompareAndSwap(true, false) {
		return
	}

	logger.InfoContext(ctx, "Agent connection restored",
		"failures_during_outage", o.count.Swap(0))
}
