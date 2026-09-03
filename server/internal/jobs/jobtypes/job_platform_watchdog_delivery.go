package jobtypes

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// deliverWatchdogDigest sends ONE digest per run to every configured
// recipient, through the recipient's OWN notification routes.
//
// No new medium and no hardcoded webhook: operators already maintain their
// contact preferences for incident paging, and the watchdog inherits them. The
// only thing this adds is the guarantee that a drop is never silent — every
// recipient that could not be reached is named in the log, because a silent
// drop on an alerting path is the exact bug this spec exists to kill.
//
// The fan-out itself now lives in internal/opsnotify, shared with the
// instance-level operator notifications: it is the same problem ("tell this
// person, on whatever they already set up"), and it used to have the word
// "watchdog" baked into every function name.
func deliverWatchdogDigest(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	cfg *watchdog.Config, digest watchdog.Digest,
) {
	if len(cfg.Recipients) == 0 {
		// Enabled with nobody to tell. The anomalies are still logged and
		// metered above; this WARN is what makes the misconfiguration visible
		// instead of letting an operator believe they are covered.
		log.WarnContext(ctx,
			"Platform watchdog is enabled but has no recipients; the digest was logged, not delivered",
			"subject", digest.Subject)

		return
	}

	deps := operatorNoticeDeps(jctx)

	notice := opsnotify.Notice{
		Event:   opsnotify.EventWatchdogDigest,
		Subject: digest.Subject,
		Body:    digest.Text,
	}

	// Watchdog recipients are NOT filtered on super_admin. They are a separate,
	// explicitly configured list whose payload is the platform's own vitals —
	// no customer content — unlike operator notifications, which can quote a
	// support thread and therefore resolve against live super-admin status.
	for _, userUID := range cfg.Recipients {
		opsnotify.DeliverToUser(ctx, deps, log, userUID, notice)
	}
}

// operatorNoticeDeps builds the notice transport for a job run.
//
// It goes through the job context rather than a registry field so that a
// minimally wired JobContext (which is what the job unit tests build) still
// gets a working email path.
func operatorNoticeDeps(jctx *jobdef.JobContext) opsnotify.Deps {
	return opsnotifywire.Build(jctx.DBService, jctx.Services, jctx.AppConfig)
}
