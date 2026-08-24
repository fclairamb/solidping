package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// PlatformWatchdogJobDefinition is the factory for the hourly platform
// watchdog (spec 2026-08-24-10).
type PlatformWatchdogJobDefinition struct{}

// Type returns the platform watchdog job type.
func (d *PlatformWatchdogJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypePlatformWatchdog
}

// PlatformWatchdogJobConfig is the empty config for the watchdog. Everything
// tunable lives in the `platform_watchdog` system parameter instead, so an
// operator can retune the thresholds live without touching the job row.
type PlatformWatchdogJobConfig struct{}

// CreateJobRun builds an executable instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *PlatformWatchdogJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg PlatformWatchdogJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing platform watchdog config: %w", err)
		}
	}

	return &PlatformWatchdogJobRun{}, nil
}

// PlatformWatchdogJobRun is the runtime state for one watchdog run.
type PlatformWatchdogJobRun struct{}

// Run evaluates the platform's own vitals and reports the transitions to the
// designated operators.
//
// This is the platform's only channel for reporting on itself. Everything else
// solidping alerts on is check-level, which means the worst failure mode of a
// monitoring product — going blind — is exactly the one that produces zero
// signal. The run is therefore deliberately fail-soft: a detector that errors
// is recorded and skipped, a delivery that fails is logged, and neither takes
// the run (or the remaining detectors) down.
func (r *PlatformWatchdogJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	cfg, err := watchdog.LoadConfig(ctx, jctx.DBService)
	if err != nil {
		log.ErrorContext(ctx, "Failed to load the platform watchdog configuration", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("load watchdog config: %w", err))
	}

	if !cfg.Enabled {
		// Disabled means NO side effects: no detector queries, no state
		// writes, no metrics, no delivery. Only the reschedule, so flipping
		// the parameter to true takes effect within one interval.
		log.DebugContext(ctx, "Platform watchdog is disabled; skipping evaluation")
		r.rescheduleSelf(ctx, jctx, cfg.Interval())

		return nil
	}

	svc := watchdog.NewService(jctx.DBService, regionHealthReporterFor(jctx))
	if jctx.Services != nil && jctx.Services.Clock != nil {
		svc.SetNow(jctx.Services.Clock.Now)
	}

	report := svc.Evaluate(ctx, cfg)

	watchdog.PublishMetrics(report)
	logWatchdogReport(ctx, log, report)

	transitions, err := svc.Reconcile(ctx, report.Filtered(cfg.Severity()), cfg)
	if err != nil {
		log.ErrorContext(ctx, "Failed to reconcile platform watchdog anomaly state", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("reconcile watchdog state: %w", err))
	}

	digest := watchdog.BuildDigest(transitions, report, report.GeneratedAt)
	if !digest.Empty() {
		deliverWatchdogDigest(ctx, jctx, log, cfg, digest)
	}

	r.rescheduleSelf(ctx, jctx, cfg.Interval())

	return nil
}

// regionHealthReporterFor builds the spec-09 ghost detector the dark-region
// detector calls. It is the REAL checks.Service, so "dark" has exactly one
// definition in the codebase; RegionHealth only reads the database, so the
// notifier/credentials/entitlements dependencies are passed through when
// available and left nil otherwise.
func regionHealthReporterFor(jctx *jobdef.JobContext) watchdog.RegionHealthReporter {
	if jctx.DBService == nil {
		return nil
	}

	var (
		eventNotifier notifier.EventNotifier
		creds         credentials.Service
		entSvc        *entcore.Service
	)

	if jctx.Services != nil {
		eventNotifier = jctx.Services.EventNotifier
		creds = jctx.Services.Credentials
		entSvc = jctx.Services.Entitlements
	}

	return checks.NewService(jctx.DBService, eventNotifier, creds, entSvc)
}

// logWatchdogReport writes the run's findings to the log unconditionally —
// including the anomalies below the delivery bar and the detectors that could
// not run. Logs and metrics are the observability floor: an instance with no
// recipients configured still has to leave a trace of what it saw.
func logWatchdogReport(ctx context.Context, log *slog.Logger, report *watchdog.Report) {
	for detector, err := range report.Failed {
		log.ErrorContext(ctx, "Platform watchdog detector failed; the other detectors still ran",
			"detector", detector, "error", err)
	}

	if len(report.Anomalies) == 0 {
		log.InfoContext(ctx, "Platform watchdog found no anomalies",
			"detectorsFailed", len(report.Failed))

		return
	}

	for _, anomaly := range report.Anomalies {
		log.WarnContext(ctx, "Platform watchdog anomaly",
			"fingerprint", anomaly.Fingerprint(),
			"severity", anomaly.Severity.String(),
			"headline", anomaly.Headline,
			"detail", anomaly.Detail)
	}
}

// rescheduleSelf keeps the watchdog alive on its own schedule, exactly like
// the snooze sweep. CreateJob dedupes on type+config+org+pending, so a restart
// cannot stack duplicates.
func (r *PlatformWatchdogJobRun) rescheduleSelf(
	ctx context.Context, jctx *jobdef.JobContext, interval time.Duration,
) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	if interval <= 0 {
		interval = watchdog.DefaultInterval
	}

	scheduledAt := time.Now().Add(interval)

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypePlatformWatchdog), nil, &jobsvc.JobOptions{
			ScheduledAt: &scheduledAt,
		})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule the platform watchdog", "error", err)
	}
}
