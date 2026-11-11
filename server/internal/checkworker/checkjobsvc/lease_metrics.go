package checkjobsvc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// ObserveRelease records the outcome of the release that ends a check run
// (spec 2026-11-11-06): the claim-to-release time, when the job carries its
// claim time, and a lost lease — the release refused with
// ErrJobClaimedByAnother because the lease expired and a peer re-claimed the
// job. The lost-lease log line carries what is needed to size
// SP_CHECKERS_LEASE_MARGIN from it alone: when the lease expired, when the
// release came, by how much it was missed, the check period and timeout.
//
// region is the region of the result (the job's, falling back to the
// worker's); an empty region is reported as such.
func ObserveRelease(ctx context.Context, logger *slog.Logger, job *models.CheckJob, region string, err error) {
	now := time.Now()

	if job.ClaimedAt != nil && (err == nil || errors.Is(err, ErrJobClaimedByAnother)) {
		prommetrics.RecordCheckLeaseHeld(region, now.Sub(*job.ClaimedAt).Seconds())
	}

	if !errors.Is(err, ErrJobClaimedByAnother) {
		return
	}

	prommetrics.RecordCheckLeaseLost(region)

	if logger == nil {
		logger = slog.Default()
	}

	attrs := []any{
		"job_uid", job.UID,
		"check_uid", job.CheckUID,
		"region", region,
		"now", now,
		"period", time.Duration(job.Period).String(),
	}

	if timeout, ok := PerCheckTimeout(job.Config); ok {
		attrs = append(attrs, "timeout", timeout.String())
	} else {
		attrs = append(attrs, "timeout", "default")
	}

	// Only this process's claimed copy knows its own lease. A job rebuilt from
	// the database at result time (the agent path) carries the row as it is
	// now, which is the peer's lease, so it would report a wrong expiry.
	if job.ClaimedAt != nil && job.LeaseExpiresAt != nil {
		attrs = append(attrs,
			"lease_expires_at", *job.LeaseExpiresAt,
			"missed_by", now.Sub(*job.LeaseExpiresAt).String(),
			"held", now.Sub(*job.ClaimedAt).String(),
			"lease_window", job.LeaseExpiresAt.Sub(*job.ClaimedAt).String(),
		)
	}

	logger.WarnContext(ctx,
		"Check lease lost: another worker re-claimed the job before its release "+
			"(raise SP_CHECKERS_LEASE_MARGIN for this region)",
		attrs...)
}
