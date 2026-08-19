package backend

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// DirectBackend implements WorkerBackend by calling the database and services
// directly. This is the production path when the worker runs in the same
// process as the API server; its SubmitResult mirrors the exact
// save-result → process-incidents → release-lease sequence CheckWorker
// performed inline before the WorkerBackend refactor.
type DirectBackend struct {
	dbService   db.Service
	checkJobSvc checkjobsvc.Service
	incidentSvc *incidents.Service
	events      notifier.EventNotifier
	// creds opens the per-job config_private envelope at claim time so the
	// worker loop only ever sees one merged plaintext config. nil (or a
	// service with no master key) is the documented encryption-disabled
	// deployment, where secrets are plaintext in the public config and no
	// envelope exists to open.
	creds credentials.Service
}

// NewDirectBackend creates a DirectBackend. creds may be nil (tests, or a
// deployment with no master key); jobs carrying an encrypted envelope then
// fail loudly rather than running without their secrets.
func NewDirectBackend(
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	incidentSvc *incidents.Service,
	events notifier.EventNotifier,
	creds credentials.Service,
) *DirectBackend {
	return &DirectBackend{
		dbService:   dbService,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidentSvc,
		events:      events,
		creds:       creds,
	}
}

// Register registers or updates a worker in the database.
func (b *DirectBackend) Register(
	ctx context.Context, worker *models.Worker,
) (*models.Worker, error) {
	return b.dbService.RegisterOrUpdateWorker(ctx, worker)
}

// Heartbeat updates the worker's last_active_at timestamp and the reported
// capability set.
func (b *DirectBackend) Heartbeat(
	ctx context.Context, workerUID string, capabilities []string,
) error {
	return b.dbService.UpdateWorkerHeartbeat(ctx, workerUID, capabilities)
}

// ClaimJobs claims up to fastLimit jobs for the given worker with the slow
// lane bounded by slowLimit (spec 2026-07-01-03 D3). Claimed jobs come back
// with their secrets already merged — see mergeClaimedSecrets.
func (b *DirectBackend) ClaimJobs(
	ctx context.Context,
	workerUID string,
	region *string,
	fastLimit int,
	slowLimit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, time.Duration, error) {
	jobs, nextIn, err := b.checkJobSvc.ClaimJobs(
		ctx, workerUID, region, fastLimit, slowLimit, maxAhead,
	)
	if err != nil {
		return nil, 0, err
	}

	return b.mergeClaimedSecrets(ctx, workerUID, jobs), nextIn, nil
}

// ClaimJobsForCheck claims any due job rows for one check (express path).
func (b *DirectBackend) ClaimJobsForCheck(
	ctx context.Context,
	workerUID string,
	region *string,
	checkUID string,
) ([]*models.CheckJob, error) {
	jobs, err := b.checkJobSvc.ClaimJobsForCheck(ctx, workerUID, region, checkUID)
	if err != nil {
		return nil, err
	}

	return b.mergeClaimedSecrets(ctx, workerUID, jobs), nil
}

// mergeClaimedSecrets is the in-process half of the "decrypt once at the
// claim/dispatch boundary" invariant: every job leaving a claim carries one
// merged plaintext config and no envelope, so CheckWorker — and every checker
// under it — stays oblivious to encryption entirely. WSBackend does the same
// for the deported path (unsealing its region-sealed envelope), which is why
// the worker loop itself needs no encryption awareness.
//
// A job whose envelope cannot be opened is dropped from the batch and reported
// as an explicit error result, matching WSBackend.submitSealError: running the
// check without its credentials is the failure this guards against (it would
// silently "pass" against endpoints that don't enforce the credential), and a
// silent skip would wedge the job until lease expiry and retry forever with
// nothing in the check's history to explain it. The error result also releases
// the lease and drives incident processing, so the check goes visibly red.
func (b *DirectBackend) mergeClaimedSecrets(
	ctx context.Context, workerUID string, jobs []*models.CheckJob,
) []*models.CheckJob {
	if len(jobs) == 0 {
		return jobs
	}

	out := make([]*models.CheckJob, 0, len(jobs))

	for _, job := range jobs {
		outcome, err := checkjobsvc.MergeJobSecrets(ctx, b.creds, job)
		if outcome == checkjobsvc.SecretMergeNoop || outcome == checkjobsvc.SecretMergeMerged {
			out = append(out, job)

			continue
		}

		// Log the cause (which may carry crypto detail) but report only the
		// static, actionable reason in the result — never a config value.
		slog.ErrorContext(ctx, "Cannot decrypt claimed job credentials",
			"error", err, "check_uid", job.CheckUID, "job_uid", job.UID,
			"organization_uid", job.OrganizationUID)

		reason := checkjobsvc.ErrSecretsUndecryptable
		if outcome == checkjobsvc.SecretMergeUnavailable {
			reason = checkjobsvc.ErrSecretsUnavailable
		}

		b.submitSecretsError(ctx, job, workerUID, reason)
	}

	return out
}

// submitSecretsError reports an unopenable envelope as an error result so the
// check's history shows the actionable message instead of the check silently
// never running.
func (b *DirectBackend) submitSecretsError(
	ctx context.Context, job *models.CheckJob, workerUID string, reason error,
) {
	if err := b.SubmitResult(ctx, job, workerUID, &SubmitResultRequest{
		Status:          int(models.ResultStatusError),
		Duration:        0,
		Metrics:         map[string]any{},
		Output:          map[string]any{checkerdef.OutputKeyError: reason.Error()},
		Region:          job.Region,
		NextScheduledAt: nextScheduledAt(job),
	}); err != nil {
		slog.ErrorContext(ctx, "Failed to submit credentials error result",
			"error", err, "check_uid", job.CheckUID, "job_uid", job.UID)
	}
}

// nextScheduledAt mirrors CheckWorker's rescheduling for jobs the backend
// terminates before they ever reach a runner.
func nextScheduledAt(job *models.CheckJob) time.Time {
	interval := time.Duration(job.Period)
	now := time.Now()

	if job.ScheduledAt == nil {
		return now.Add(interval)
	}

	if next := job.ScheduledAt.Add(interval); next.After(now) {
		return next
	}

	return now.Add(interval)
}

// SubmitResult saves the result row (with status tracking), processes
// incidents, and releases the lease — with scheduling state when provided,
// plain otherwise. Incident processing only runs after a successful save (a
// result that never landed must not drive the incident state machine); the
// lease is released regardless so the job never wedges behind a failed write.
func (b *DirectBackend) SubmitResult(
	ctx context.Context,
	job *models.CheckJob,
	workerUID string,
	req *SubmitResultRequest,
) error {
	resultUID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("failed to generate result UID: %w", err)
	}

	status := req.Status
	duration := req.Duration
	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: job.OrganizationUID,
		CheckUID:        job.CheckUID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &workerUID,
		Region:          req.Region,
		Status:          &status,
		Duration:        &duration,
		Metrics:         models.JSONMap(req.Metrics),
		Output:          models.JSONMap(req.Output),
		CreatedAt:       time.Now(),
	}

	saveStart := time.Now()
	saveErr := b.dbService.SaveResultWithStatusTracking(ctx, result)
	prommetrics.RecordCheckStage("save_result", time.Since(saveStart).Seconds())

	if saveErr == nil {
		incStart := time.Now()
		b.processIncidents(ctx, job, result)
		prommetrics.RecordCheckStage("process_incident", time.Since(incStart).Seconds())
	} else {
		slog.ErrorContext(ctx, "Failed to save result", "error", saveErr, "check_uid", job.CheckUID)
	}

	releaseStart := time.Now()

	var releaseErr error
	if req.Sched != nil {
		releaseErr = b.checkJobSvc.ReleaseLeaseWithSchedulingState(
			ctx, job.UID, workerUID, req.NextScheduledAt,
			req.Sched.CostEWMAMs, req.Sched.DelayEWMAMs,
			req.Sched.EffectiveScheduledAt, req.Sched.Lane,
		)
	} else {
		releaseErr = b.checkJobSvc.ReleaseLease(ctx, job.UID, workerUID, req.NextScheduledAt)
	}

	prommetrics.RecordCheckStage("release_lease", time.Since(releaseStart).Seconds())

	if saveErr != nil {
		return fmt.Errorf("failed to save result: %w", saveErr)
	}

	if releaseErr != nil {
		return fmt.Errorf("failed to release lease: %w", releaseErr)
	}

	return nil
}

// processIncidents mirrors CheckWorker's incident hot path: use the check
// attached at claim time, falling back to a fetch when missing.
func (b *DirectBackend) processIncidents(ctx context.Context, job *models.CheckJob, result *models.Result) {
	check := job.Check
	if check == nil {
		var err error

		check, err = b.dbService.GetCheck(ctx, job.OrganizationUID, job.CheckUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to fetch check for incident processing", "error", err)

			return
		}
	}

	if err := b.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		slog.WarnContext(ctx, "Failed to process check result for incidents", "error", err)
	}
}

// ReleaseLease releases the lease and reschedules without writing a result.
func (b *DirectBackend) ReleaseLease(
	ctx context.Context,
	job *models.CheckJob,
	workerUID string,
	nextScheduledAt time.Time,
) error {
	return b.checkJobSvc.ReleaseLease(ctx, job.UID, workerUID, nextScheduledAt)
}

// LastResults returns the latest result per check (passive checks).
func (b *DirectBackend) LastResults(
	ctx context.Context, orgUID string, checkUIDs []string,
) (map[string]*models.Result, error) {
	return b.dbService.GetLastResultForChecks(ctx, orgUID, checkUIDs)
}

// Hints subscribes to check.created events (the in-process express hint).
func (b *DirectBackend) Hints() <-chan string {
	return b.events.Listen("check.created")
}
