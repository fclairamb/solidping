package checkworker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/version"
)

const (
	// passiveEvalBatch caps one claim. A full batch loops again immediately,
	// so it bounds a transaction, not throughput.
	passiveEvalBatch = 100

	// passiveEvalFallbackPoll is the longest the evaluator sleeps between
	// claims. The claim's next-eligible hint normally wakes it right on the
	// next tick; this only bounds how late a job created or rescheduled since
	// the last claim (a new check, an edit) is picked up.
	passiveEvalFallbackPoll = 10 * time.Second

	// passiveEvalReleaseTimeout bounds the shutdown release of claimed but
	// unevaluated jobs, which runs on a fresh context.
	passiveEvalReleaseTimeout = 5 * time.Second

	// passiveWorkerSlugPrefix / passiveWorkerNamePrefix name the jobs node's
	// workers row: `jobs-<node slug>` / `jobs:<node name>`. Distinct from the
	// check worker's own row, so a node running both roles keeps two.
	passiveWorkerSlugPrefix = "jobs-"
	passiveWorkerNamePrefix = "jobs:"

	// passiveWorkerSlugMaxLen mirrors config.WorkerSlugPattern's 21-character
	// ceiling.
	passiveWorkerSlugMaxLen = 21
	// passiveWorkerSlugHashLen is how many hex characters of the node slug's
	// hash replace its tail when `jobs-<slug>` would not fit.
	passiveWorkerSlugHashLen = 5
)

// PassiveEvaluator evaluates passive checks (heartbeat, email) on the jobs
// node (spec 2026-09-25-04).
//
// Before it, the "is the signal overdue?" evaluation ran as an ordinary
// regional check job, which failed three ways: a dead region silenced every
// dead-man's switch pinned to it (the 2026-09-24 `lauterbourg` outage), an
// agent that won the job could not evaluate it and opened an incident every
// period, and N regions wrote N identical evaluations. A passive check now owns
// exactly one NULL-region check_jobs row, every check worker and agent claim
// excludes passive types, and this loop is the only claimer.
//
// Guarantees, all from the check_jobs lease:
//
//   - it depends on no region and no check worker being alive — only on a node
//     running the jobs role;
//   - a restart loses nothing: claimed-but-unevaluated jobs are released on
//     shutdown, and a crash only waits out the lease (scheduled_at + period +
//     30s) before the next claim picks the job up;
//   - two jobs nodes never evaluate one tick twice: the claim locks rows
//     (FOR UPDATE SKIP LOCKED on Postgres) and leases them in one transaction,
//     and the release moves scheduled_at to the next tick.
//
// Its lease owner is a `workers` row of its own (check_jobs.lease_worker_uid
// is a foreign key). Evaluation rows therefore carry a worker_uid, which is
// half of what keeps them from being read back as signals; the other half is
// the `output.evaluation` filter in GetLastSignalForChecks. Evaluation rows
// carry no region: they did not run anywhere.
type PassiveEvaluator struct {
	cfg     *config.Config
	jobs    checkjobsvc.Service
	backend backend.WorkerBackend
	logger  *slog.Logger

	worker atomic.Pointer[models.Worker]
	wg     sync.WaitGroup

	batch int
	poll  time.Duration
}

// NewPassiveEvaluator builds the jobs node's passive evaluator, writing its
// results through the same in-process pipeline as the check worker
// (DirectBackend: save, ProcessCheckResult, release).
func NewPassiveEvaluator(
	dbService db.Service,
	cfg *config.Config,
	svc *services.Registry,
	checkJobSvc checkjobsvc.Service,
) *PassiveEvaluator {
	incidentSvc, _ := newInProcessIncidentService(dbService, cfg, svc)

	directBackend := backend.NewDirectBackend(
		dbService, checkJobSvc, incidentSvc, svc.EventNotifier, svc.Credentials,
	)

	return newPassiveEvaluator(cfg, checkJobSvc, directBackend)
}

// newPassiveEvaluator is the testable constructor.
func newPassiveEvaluator(
	cfg *config.Config, checkJobSvc checkjobsvc.Service, workerBackend backend.WorkerBackend,
) *PassiveEvaluator {
	return &PassiveEvaluator{
		cfg:     cfg,
		jobs:    checkJobSvc,
		backend: workerBackend,
		logger:  slog.Default().With("component", "passive_evaluator"),
		batch:   passiveEvalBatch,
		poll:    passiveEvalFallbackPoll,
	}
}

// Run registers the evaluator's workers row and evaluates due passive jobs
// until ctx is canceled.
func (e *PassiveEvaluator) Run(ctx context.Context) error {
	if err := e.register(ctx); err != nil {
		return fmt.Errorf("failed to register passive evaluator: %w", err)
	}

	registered := e.worker.Load()
	e.logger.InfoContext(ctx, "Passive evaluator started",
		"worker_uid", registered.UID, "worker_slug", registered.Slug)

	e.wg.Add(1)

	go e.heartbeatLoop(ctx)

	defer e.wg.Wait()

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			e.logger.InfoContext(ctx, "Passive evaluator stopped")

			return ctx.Err()
		case <-timer.C:
		}

		evaluated, nextIn, err := e.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			e.logger.WarnContext(ctx, "Passive evaluation pass failed", "error", err)
		}

		timer.Reset(e.nextWait(evaluated, nextIn, err))
	}
}

// nextWait is how long the loop sleeps after a pass: straight back for a full
// batch, until the next tick when the claim knows it, the fallback poll
// otherwise (and after a failed pass).
func (e *PassiveEvaluator) nextWait(evaluated int, nextIn time.Duration, err error) time.Duration {
	if err != nil {
		return e.poll
	}

	if evaluated >= e.batch {
		return 0
	}

	if nextIn > 0 && nextIn < e.poll {
		return nextIn
	}

	return e.poll
}

// register upserts the evaluator's workers row: region NULL, slug
// `jobs-<node slug>`, name `jobs:<node name>`.
func (e *PassiveEvaluator) register(ctx context.Context) error {
	identity := e.cfg.WorkerIdentity()
	buildVersion := version.Get().Version

	worker := &models.Worker{
		UID:     uuid.New().String(),
		Slug:    passiveWorkerSlug(identity.Slug),
		Name:    passiveWorkerNamePrefix + identity.Name,
		Version: &buildVersion,
	}

	registered, err := e.backend.Register(ctx, worker)
	if err != nil {
		return err
	}

	e.worker.Store(registered)

	return nil
}

// passiveWorkerSlug derives the evaluator's workers.slug from the node slug.
// `jobs-<slug>` when it fits the 21-character column rule; otherwise the slug
// is cut and suffixed with a short hash of the full slug, so two long node
// names sharing a prefix still register two rows.
func passiveWorkerSlug(nodeSlug string) string {
	slug := passiveWorkerSlugPrefix + nodeSlug
	if len(slug) <= passiveWorkerSlugMaxLen {
		return slug
	}

	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(nodeSlug))
	hash := fmt.Sprintf("%08x", hasher.Sum32())[:passiveWorkerSlugHashLen]

	keep := passiveWorkerSlugMaxLen - len(passiveWorkerSlugPrefix) - 1 - passiveWorkerSlugHashLen
	head := strings.TrimRight(nodeSlug[:keep], "-")

	return passiveWorkerSlugPrefix + head + "-" + hash
}

// heartbeatLoop keeps the evaluator's workers row alive, like a check
// worker's.
func (e *PassiveEvaluator) heartbeatLoop(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.backend.Heartbeat(ctx, e.worker.Load().UID, nil, version.Get().Version); err != nil {
				e.logger.WarnContext(ctx, "Failed to update passive evaluator heartbeat", "error", err)
			}
		}
	}
}

// RunOnce claims every due passive job (up to one batch) and evaluates it.
// Returns how many jobs it claimed and the claim's next-eligible hint.
func (e *PassiveEvaluator) RunOnce(ctx context.Context) (int, time.Duration, error) {
	worker := e.worker.Load()
	if worker == nil {
		return 0, 0, errPassiveEvaluatorNotRegistered
	}

	jobs, nextIn, err := e.jobs.ClaimPassiveJobs(ctx, worker.UID, e.batch)
	if err != nil {
		return 0, 0, fmt.Errorf("claim passive jobs: %w", err)
	}

	for i, job := range jobs {
		if ctx.Err() != nil {
			e.releaseUnevaluated(jobs[i:], worker.UID)

			return len(jobs), nextIn, ctx.Err()
		}

		if evalErr := e.evaluate(ctx, job, worker.UID); evalErr != nil {
			e.logger.WarnContext(ctx, "Passive evaluation failed",
				"check_uid", job.CheckUID, "job_uid", job.UID, "error", evalErr)
		}
	}

	return len(jobs), nextIn, nil
}

// errPassiveEvaluatorNotRegistered guards RunOnce before Run registered the
// evaluator's workers row.
var errPassiveEvaluatorNotRegistered = errors.New("passive evaluator is not registered")

// evaluate runs one passive evaluation and writes it through the in-process
// result pipeline, or, inside the first-signal grace, only moves the schedule.
//
// A failure to READ the last signal writes nothing and retries at the next
// tick: it is a fault of this database, not of the monitored sender, and must
// not open the customer's incident. If it persists, the freshness sweep
// reports the check as stale, which is the honest signal.
func (e *PassiveEvaluator) evaluate(ctx context.Context, job *models.CheckJob, workerUID string) error {
	now := time.Now()
	next := nextPassiveScheduledAt(job, now)

	status, output, grace, err := passiveVerdict(ctx, e.backend, job, now)
	if err != nil {
		if relErr := e.jobs.ReleaseLease(ctx, job.UID, workerUID, next); relErr != nil {
			return errors.Join(err, relErr)
		}

		return err
	}

	if grace {
		return e.jobs.ReleaseLease(ctx, job.UID, workerUID, next)
	}

	return e.backend.SubmitResult(ctx, job, workerUID, &backend.SubmitResultRequest{
		Status:          int(status),
		Duration:        0,
		Metrics:         map[string]any{},
		Output:          output,
		Region:          nil,
		NextScheduledAt: next,
	})
}

// releaseUnevaluated hands claimed jobs back at their current schedule, so the
// next pass (on this node after a restart, or on another one) evaluates them
// at once instead of waiting out the lease. Runs on a fresh context: the
// caller's is canceled.
func (e *PassiveEvaluator) releaseUnevaluated(jobs []*models.CheckJob, workerUID string) {
	ctx, cancel := context.WithTimeout(context.Background(), passiveEvalReleaseTimeout)
	defer cancel()

	for _, job := range jobs {
		scheduledAt := time.Now()
		if job.ScheduledAt != nil {
			scheduledAt = *job.ScheduledAt
		}

		if err := e.jobs.ReleaseLease(ctx, job.UID, workerUID, scheduledAt); err != nil {
			e.logger.WarnContext(ctx, "Failed to release an unevaluated passive job",
				"job_uid", job.UID, "error", err)
		}
	}
}

// nextPassiveScheduledAt is the next tick of a passive job: its one
// region-less job's jitter-only phase, the same one reconcile writes
// (scheduling.NextAligned with no region and no spread), so every jobs node
// computes the same instant.
func nextPassiveScheduledAt(job *models.CheckJob, now time.Time) time.Time {
	period := time.Duration(job.Period)
	if period <= 0 {
		return now.Add(time.Minute)
	}

	return scheduling.NextAligned(now, period, period, job.CheckUID, nil, nil, 0)
}
