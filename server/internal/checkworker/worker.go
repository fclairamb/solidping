// Package checkworker implements distributed check job execution.
package checkworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/stats"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Errors returned by the check runner.
var (
	ErrUnknownCheckType   = errors.New("unknown check type")
	ErrCheckerNotFound    = errors.New("checker not found for type")
	ErrFailedToParseConf  = errors.New("failed to parse config")
	ErrFailedToFetchCheck = errors.New("failed to fetch check from database")
	ErrNoCheckType        = errors.New("check job has no type set")

	// ErrCheckerPanic wraps a recovered panic from inside a checker's
	// Execute call (spec 2026-07-05-05 D2). The runner survives; the panic
	// is converted into a StatusError result instead of crashing the
	// process.
	ErrCheckerPanic = errors.New("checker panicked")

	// ErrCheckerAbandoned marks a checker execution the watchdog gave up
	// on because the checker never observed its context deadline within
	// execTimeout + abandonGrace (spec 2026-07-05-05 D1).
	ErrCheckerAbandoned = errors.New("check execution abandoned: checker did not honor its context")
)

const (
	// heartbeatInterval is how often the worker updates its last_active_at.
	heartbeatInterval = 50 * time.Second

	periodTypeRaw = "raw"

	outputKeyMessage = "message"

	// abandonGrace is how much extra time a checker gets past its
	// execTimeout before the watchdog abandons it (spec 2026-07-05-05 D1).
	// This gives well-behaved checkers room to observe ctx.Done() and
	// return their own StatusTimeout first — the watchdog only fires for
	// checkers that ignore the context entirely.
	abandonGrace = 5 * time.Second
)

// abandonGraceOverride lets tests shrink abandonGrace so hang/panic watchdog
// tests don't have to wait out the real 5s grace. Zero (the default) means
// "use abandonGrace". Package-private; production code never sets it.
//
//nolint:gochecknoglobals // test-only override, never mutated outside _test.go files
var abandonGraceOverride time.Duration

// effectiveAbandonGrace returns abandonGraceOverride when a test has set one,
// else the real abandonGrace constant.
func effectiveAbandonGrace() time.Duration {
	if abandonGraceOverride > 0 {
		return abandonGraceOverride
	}

	return abandonGrace
}

// CheckWorker executes check jobs from the queue.
type CheckWorker struct {
	// worker holds the registered worker identity. It is written once by
	// registerWorker (from Run's goroutine) and then read from many goroutines
	// (runners, fetcher, heartbeat, the Prometheus channel collector, and tests),
	// so access goes through atomic.Pointer to avoid a data race. Use
	// setWorker/getWorker rather than touching the field directly.
	worker      atomic.Pointer[models.Worker]
	dbService   db.Service
	checkJobSvc checkjobsvc.Service
	incidentSvc *incidents.Service
	config      *config.Config
	services    *services.Registry
	logger      *slog.Logger
	wg          sync.WaitGroup
	stats       stats.ProcessingStats

	// getChecker/parseConfig default to registry.GetChecker/registry.ParseConfig
	// (set by NewCheckWorker) and are only ever overridden in tests, to drive
	// executeJob/runnerLoop end-to-end with a stub checkerdef.Checker that
	// deliberately hangs or panics — registry.GetChecker itself is a hardcoded
	// switch with no such seam. Production behavior is unchanged: the default
	// wiring calls the exact same registry functions executeJob called before
	// this field existed.
	getChecker  func(checkerdef.CheckType) (checkerdef.Checker, bool)
	parseConfig func(checkerdef.CheckType) (checkerdef.Config, bool)

	// Channel-based architecture fields
	poolSize         int                   // Number of runner goroutines
	availableRunners atomic.Int32          // Runners waiting for jobs
	jobsChan         chan *models.CheckJob // Fetcher → Runners
	completionChan   chan struct{}         // Runners → Fetcher (wake-up signal)

	// Fast/slow lane reservation (spec 2026-07-01-03 D3/D4): slow-lane jobs in
	// flight never exceed poolSize − fastLaneReserved on this worker, so a
	// burst of slow probes can never occupy the whole pool and starve due fast
	// checks. busySlow counts in-flight slow-lane jobs: incremented by the
	// fetcher right after a successful claim (accounting at claim time — not
	// at runner start — closes the window where a re-fetch could read a stale
	// count and over-claim slow) and decremented by the runner when the job
	// finishes. The express path bypasses both the pool and this accounting (a
	// freshly created check is lane-fast by definition; its goroutine is
	// additive).
	busySlow         atomic.Int32
	fastLaneReserved int // reserved fast-only slots, clamped to [0, poolSize−1]

	// Cost-aware, plan-weighted scheduling (spec 2026-06-30-09): schedParams is
	// the pure math config that deprioritizes slow checks (and credits paid
	// tiers) by pushing each job's effective scheduled_at on reschedule. It only
	// reorders fetch order — it never sheds or defers a claimed job.
	schedParams scheduling.Params

	// Self-stats reporting fields
	internalCheckUID string // UID of the internal check for this worker
	defaultOrgUID    string // UID of the default organization
}

// NewCheckWorker creates a new check runner.
func NewCheckWorker(
	dbService db.Service,
	cfg *config.Config,
	svc *services.Registry,
	checkJobSvc checkjobsvc.Service,
) *CheckWorker {
	logger := slog.Default().With("component", "check_worker")

	poolSize := cfg.Server.CheckWorker.Nb
	if poolSize <= 0 {
		// I/O-bound work tolerates far more concurrency than a CPU-bound pool;
		// a goroutine parked on a slow socket costs ~KB and zero CPU. Slow
		// checks are deprioritized in fetch order (spec 2026-06-30-09) and
		// capacity-capped by the fast-lane reservation below (spec
		// 2026-07-01-03), not by this count.
		poolSize = 25
	}

	// Clamp the fast-lane floor to [0, poolSize−1] (spec 2026-07-01-03 D3): a
	// floor at or above the pool size would leave the slow lane zero slots
	// forever, silently killing every slow check.
	fastLaneReserved := cfg.Server.Scheduling.FastLaneReserved
	switch {
	case fastLaneReserved < 0:
		logger.Warn("scheduling.fast_lane_reserved is negative; clamping to 0 (no reservation)",
			"configured", cfg.Server.Scheduling.FastLaneReserved)
		fastLaneReserved = 0
	case fastLaneReserved >= poolSize:
		logger.Warn("scheduling.fast_lane_reserved >= pool size would starve the slow lane; clamping to pool−1",
			"configured", cfg.Server.Scheduling.FastLaneReserved,
			"pool_size", poolSize,
			"clamped", poolSize-1)
		fastLaneReserved = poolSize - 1
	}

	schedParams := schedulingParamsFromConfig(cfg.Server.Scheduling)

	return &CheckWorker{
		dbService:   dbService,
		config:      cfg,
		services:    svc,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidents.NewService(dbService, svc.Jobs, clock.Real{}, svc.Realtime),
		logger:      logger,
		stats:       stats.NewProcessingStats(time.Minute, time.Minute, logger),
		getChecker:  registry.GetChecker,
		parseConfig: registry.ParseConfig,
		// Channel-based architecture
		poolSize:         poolSize,
		fastLaneReserved: fastLaneReserved,
		jobsChan:         make(chan *models.CheckJob),
		completionChan:   make(chan struct{}, 1),
		schedParams:      schedParams,
	}
}

// laneLimits computes the per-lane claim limits (fast, slow) for one fetch
// (spec 2026-07-01-03 D3). Fast jobs may occupy any free slot (the fast limit
// is the full free capacity); slow jobs may only occupy slots above the
// reserved fast floor:
//
//	slowBudget = max(0, (poolSize − fastReserved) − busySlow)
//
// clamped to the free slots. An idle slow lane donates everything to fast
// (trivially); an idle fast stream lets slow borrow up to poolSize −
// fastReserved; a fast burst never finds fewer than fastReserved slots
// occupied by nothing slower than another fast check. Per-worker enforcement
// is fleet-correct: every worker independently guarantees its own floor.
func laneLimits(free, poolSize, fastReserved, busySlow int) (int, int) {
	slowLimit := (poolSize - fastReserved) - busySlow
	if slowLimit < 0 {
		slowLimit = 0
	}
	if slowLimit > free {
		slowLimit = free
	}

	return free, slowLimit
}

// schedulingParamsFromConfig converts the koanf SchedulingConfig (seconds / ms
// scalars) into the worker's scheduling.Params (typed durations). Every zero
// value maps to "feature off", so the default config reproduces pure-FIFO,
// flat-30s behavior.
func schedulingParamsFromConfig(cfg config.SchedulingConfig) scheduling.Params {
	secs := func(seconds float64) time.Duration { return time.Duration(seconds * float64(time.Second)) }
	millis := func(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

	return scheduling.Params{
		SlowThresholdMs:     cfg.SlowThresholdMs,
		TierCreditPerWeight: secs(cfg.TierCreditSeconds),
		TierCreditMax:       secs(cfg.TierCreditMaxSeconds),
		CostTimeoutFactor:   cfg.CostTimeoutFactor,
		CostTimeoutFloor:    millis(cfg.CostTimeoutFloorMs),
		LaneSlowThresholdMs: cfg.LaneSlowThresholdMs,
		LaneFastThresholdMs: cfg.LaneFastThresholdMs,
	}
}

// Run starts the runner loop (blocking).
func (r *CheckWorker) Run(ctx context.Context) error {
	r.logger.InfoContext(ctx, "Starting check worker")

	// 1. Register worker in database
	if err := r.registerWorker(ctx); err != nil {
		return fmt.Errorf("failed to register worker: %w", err)
	}

	registered := r.getWorker()
	r.logger.InfoContext(ctx, "Worker registered",
		"worker_uid", registered.UID,
		"worker_slug", registered.Slug,
		"pool_size", r.poolSize)

	// 1b. Register per-worker Prometheus channel-depth collector
	prometheus.DefaultRegisterer.MustRegister(newWorkerChannelCollector(r))

	// 2. Setup self-stats reporting
	if err := r.setupSelfStats(ctx); err != nil {
		r.logger.WarnContext(ctx, "Failed to setup self-stats, continuing without it", "error", err)
	}

	// 3. Start heartbeat goroutine
	r.wg.Add(1)
	go r.heartbeatLoop(ctx)

	// 4. Start runner pool
	for i := 0; i < r.poolSize; i++ {
		r.wg.Add(1)
		go r.runnerLoop(ctx, i)
	}

	// 5. Start fetcher (owns jobsChan, closes it on exit)
	r.wg.Add(1)
	go r.fetcherLoop(ctx)

	// 6. Start express runner (handles check.created events directly)
	r.wg.Add(1)
	go r.expressLoop(ctx)

	// 7. Wait for shutdown signal
	<-ctx.Done()
	r.logger.InfoContext(ctx, "Check worker stopping, waiting for goroutines")

	// 7. Wait for all goroutines to finish
	// The fetcherLoop closes jobsChan on exit, which signals runners to stop
	r.wg.Wait()
	r.logger.InfoContext(ctx, "Check worker stopped")

	return ctx.Err()
}

// registerWorker registers or updates the worker in the database.
func (r *CheckWorker) registerWorker(ctx context.Context) error {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	// Limit hostname to ensure slug doesn't exceed 20 characters (slug = hostname-cr-X)
	// Reserve 5 chars for "-cr-X", leaving 15 for hostname
	if len(hostname) > 15 {
		hostname = hostname[:15]
	}

	slug := strings.ToLower(hostname)
	name := hostname

	region := "default"
	if r.config.Server.CheckWorker.Region != "" {
		region = r.config.Server.CheckWorker.Region
	}

	worker := &models.Worker{
		UID:    uuid.New().String(),
		Slug:   slug,
		Name:   name,
		Region: &region,
	}

	registeredWorker, err := r.dbService.RegisterOrUpdateWorker(ctx, worker)
	if err != nil {
		return err
	}

	r.setWorker(registeredWorker)
	return nil
}

// setWorker publishes the registered worker identity. Safe to call from any
// goroutine.
func (r *CheckWorker) setWorker(worker *models.Worker) {
	r.worker.Store(worker)
}

// getWorker returns the registered worker identity, or nil before registration.
// Safe to call from any goroutine.
func (r *CheckWorker) getWorker() *models.Worker {
	return r.worker.Load()
}

// heartbeatLoop periodically updates the worker's last_active_at.
func (r *CheckWorker) heartbeatLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.updateHeartbeat(ctx)
		}
	}
}

// updateHeartbeat updates the worker's last_active_at timestamp.
func (r *CheckWorker) updateHeartbeat(ctx context.Context) {
	if err := r.dbService.UpdateWorkerHeartbeat(ctx, r.getWorker().UID); err != nil {
		r.logger.ErrorContext(ctx, "Failed to update heartbeat", "error", err)
	}
}

// fetcherLoop fetches jobs from the database and distributes them to runners.
func (r *CheckWorker) fetcherLoop(ctx context.Context) {
	defer r.wg.Done()
	defer close(r.jobsChan) // Signal runners to exit when fetcher stops

	logger := r.logger.With("role", "fetcher")
	logger.InfoContext(ctx, "Fetcher started")
	defer logger.InfoContext(ctx, "Fetcher stopped")

	checkCreatedChan := r.services.EventNotifier.Listen("check.created")

	for {
		// Check for shutdown
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Fetch and distribute jobs if runners are available
		if err := r.fetchAndDistributeJobs(ctx, logger); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// Wait briefly before retry on error
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second * 5):
			}
			continue
		}

		// Wait for next trigger
		select {
		case <-ctx.Done():
			return
		case <-r.completionChan:
			// A runner completed a job, capacity available
		case <-checkCreatedChan:
			// New check created, might be ready to execute
		case <-time.After(time.Minute):
			// Periodic check for newly-scheduled jobs
		}
	}
}

// fetchAndDistributeJobs claims jobs from the database and sends them to runners.
// Returns nil if no runners available or no jobs to distribute.
func (r *CheckWorker) fetchAndDistributeJobs(ctx context.Context, logger *slog.Logger) error {
	available := int(r.availableRunners.Load())

	if available == 0 {
		logger.DebugContext(ctx, "All runners busy, waiting for completion")
		return nil
	}

	// Per-lane reservation (spec 2026-07-01-03 D3): fast may fill every free
	// slot; slow is bounded by the budget left under the fast floor.
	fastLimit, slowLimit := laneLimits(
		available, r.poolSize, r.fastLaneReserved, int(r.busySlow.Load()),
	)

	cfg := r.config.Server.CheckWorker
	fetchStart := time.Now()
	worker := r.getWorker()
	jobs, err := r.checkJobSvc.ClaimJobs(
		ctx,
		worker.UID,
		worker.Region,
		fastLimit,
		slowLimit,
		cfg.FetchMaxAhead,
	)
	prommetrics.RecordCheckStage("fetch", time.Since(fetchStart).Seconds())
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		// no-op: shutdown path
	case err != nil:
		prommetrics.RecordClaimJobsOutcome("error")
	case len(jobs) == 0:
		prommetrics.RecordClaimJobsOutcome("empty")
	default:
		prommetrics.RecordClaimJobsOutcome("jobs")
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "Failed to claim jobs", "error", err)
		}
		return err
	}

	// Account in-flight slow jobs at claim time (D4): the count must be
	// visible before any subsequent fetch computes its slow budget, and a
	// runner-side increment would leave a window between channel handoff and
	// runner start where a re-fetch could over-claim slow. The runner
	// decrements when the job finishes.
	slowClaimed := 0
	for _, job := range jobs {
		if job.Lane == scheduling.LaneSlow {
			r.busySlow.Add(1)
			slowClaimed++
		}
	}
	prommetrics.RecordLaneClaims(prommetrics.LaneLabelFast, len(jobs)-slowClaimed)
	prommetrics.RecordLaneClaims(prommetrics.LaneLabelSlow, slowClaimed)

	// Distribute jobs to runners
	for _, job := range jobs {
		select {
		case r.jobsChan <- job:
			// Job delivered to a runner
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if len(jobs) > 0 {
		logger.DebugContext(ctx, "Distributed jobs",
			"count", len(jobs),
			"available_runners", available)
	}

	return nil
}

// expressLoop subscribes to check.created and runs the freshly-created
// check on its own goroutine, bypassing the regular runner pool. This
// keeps first-run latency bounded by execution time rather than by
// however long the busiest pool runner is mid-check. Jobs claimed here
// are atomic via the shared lease mechanism, so a parallel
// fetcherLoop pickup of the same row cannot double-execute it.
func (r *CheckWorker) expressLoop(ctx context.Context) {
	defer r.wg.Done()

	logger := r.logger.With("role", "express")
	logger.InfoContext(ctx, "Express runner started")
	defer logger.InfoContext(ctx, "Express runner stopped")

	events := r.services.EventNotifier.Listen("check.created")

	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-events:
			if !ok {
				return
			}
			r.handleExpressEvent(ctx, logger, payload)
		}
	}
}

// handleExpressEvent decodes one check.created payload and runs the
// matching job, if it can claim it. Old senders publish "{}" (no
// check_uid), in which case the express path silently no-ops and the
// regular fetcher still picks the new check up on its next poll.
func (r *CheckWorker) handleExpressEvent(ctx context.Context, logger *slog.Logger, payload string) {
	// The wire format uses snake_case to stay aligned with the check.* event
	// payloads stored in the events table; switching only the notifier
	// payload to camelCase would split the convention across two surfaces
	// for the same check_uid concept.
	var msg struct {
		CheckUID string `json:"check_uid"` //nolint:tagliatelle // intentional: matches event payload convention
	}

	if err := json.Unmarshal([]byte(payload), &msg); err != nil || msg.CheckUID == "" {
		return
	}

	worker := r.getWorker()
	jobs, err := r.checkJobSvc.ClaimJobsForCheck(ctx, worker.UID, worker.Region, msg.CheckUID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.WarnContext(ctx, "express claim failed",
				"error", err,
				"check_uid", msg.CheckUID)
		}

		return
	}

	for _, job := range jobs {
		if err := r.executeJob(ctx, logger, job); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "express execution failed",
				"error", err,
				"check_uid", job.CheckUID)
		}
	}
}

// runnerLoop is the main loop for a runner goroutine.
func (r *CheckWorker) runnerLoop(ctx context.Context, id int) {
	defer r.wg.Done()

	logger := r.logger.With("runner_id", id)
	logger.InfoContext(ctx, "Runner started")
	defer logger.InfoContext(ctx, "Runner stopped")

	for {
		// Signal: "I'm available for work"
		r.availableRunners.Add(1)

		// Wait for a job
		var job *models.CheckJob
		var ok bool

		select {
		case job, ok = <-r.jobsChan:
			// Got a job or channel was closed
		case <-ctx.Done():
			r.availableRunners.Add(-1)
			return
		}

		// Signal: "I'm now busy"
		r.availableRunners.Add(-1)

		// Channel closed = shutdown
		if !ok {
			return
		}

		// Execute the job
		if err := r.executeJob(ctx, logger, job); err != nil {
			logger.ErrorContext(ctx, "Error executing job",
				"error", err,
				"check_uid", job.CheckUID)
		}

		// A finished slow-lane job frees its reserved-budget slot (the
		// matching increment happened in the fetcher at claim time).
		if job.Lane == scheduling.LaneSlow {
			r.busySlow.Add(-1)
		}

		// Signal completion to wake fetcher (non-blocking)
		select {
		case r.completionChan <- struct{}{}:
		default:
			// Channel already has a signal, that's fine
		}
	}
}

// executeJob executes a single check job.
//
//nolint:funlen,cyclop // Slightly over limits due to OTel tracing
func (r *CheckWorker) executeJob(
	ctx context.Context,
	logger *slog.Logger,
	checkJob *models.CheckJob,
) error {
	ctx, span := otel.Tracer("solidping.check").Start(
		ctx, "check.execute",
		trace.WithAttributes(
			attribute.String("check.uid", checkJob.CheckUID),
			attribute.String("check.type", checkJob.Type),
			attribute.String(
				"organization.uid",
				checkJob.OrganizationUID,
			),
		),
	)
	defer span.End()

	logger.InfoContext(
		ctx,
		"Executing check job",
		"check_type", checkJob.Type,
		"check_config", checkJob.Config,
	)

	startTime := time.Now()

	// 1. Get check type from check_jobs.type
	if checkJob.Type == "" {
		return r.saveErrorResult(ctx, checkJob, ErrNoCheckType)
	}
	checkType := checkJob.Type

	// Passive checks (heartbeat, email) don't make outbound requests — the
	// worker just inspects whether a recent inbound signal arrived in time.
	if isPassiveCheckType(checkerdef.CheckType(checkType)) {
		return r.executePassiveJob(ctx, logger, checkJob)
	}

	// 2. Parse check configuration
	var checkConfig checkerdef.Config

	// Parse config from check_jobs.config
	config, ok := r.parseConfig(checkerdef.CheckType(checkType))
	if !ok {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("%w: %s", ErrUnknownCheckType, checkType))
	}

	if err := config.FromMap(checkJob.Config); err != nil {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("%w: %w", ErrFailedToParseConf, err))
	}

	checkConfig = config

	// 3. Get checker from registry
	checker, ok := r.getChecker(checkerdef.CheckType(checkType))
	if !ok {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("%w: %s", ErrCheckerNotFound, checkType))
	}

	sleepTime := checkJob.ScheduledAt.Sub(startTime)

	// wait is the pre-schedule sleep actually observed (claimed ahead of
	// scheduled_at, e.g. by D3's bounded claim-ahead window); delay is how
	// far past-due the job was at dispatch (claimed late). Exactly one of
	// the two is ever non-zero. Reported as separate fields on the
	// completion log (D4) instead of being folded into duration_ms, which
	// previously conflated wait + exec + save/release overhead into one
	// number — a healthy sub-second check claimed minutes ahead of schedule
	// logged as if it had taken minutes to execute.
	wait, delay := waitAndDelay(sleepTime)

	// Wait for the check time to come
	if sleepTime > 0 {
		timer := time.NewTimer(sleepTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Server is shutting down — don't save a result, let the lease expire
			// so the job is picked up again on restart
			logger.InfoContext(ctx, "Server shutting down during sleep, leaving job for next startup")

			return nil
		case <-timer.C:
			// Scheduled time reached, continue with check execution
		}
	}

	// Per-org MaxChecksPerMinute gate. Drained buckets reschedule the
	// job for next period without writing a result, so the user sees
	// missed executions in their history rather than a hard error. The
	// entitlements service treats nil caps as unlimited (no-op).
	if entSvc := r.services.Entitlements; entSvc != nil {
		if rateErr := entSvc.ReserveCheckExecution(ctx, checkJob.OrganizationUID); rateErr != nil {
			var quotaErr *entitlements.QuotaError
			if errors.As(rateErr, &quotaErr) {
				prommetrics.ChecksRateLimited.WithLabelValues(checkJob.OrganizationUID).Inc()
				logger.InfoContext(ctx, "Check execution rate-limited; deferring to next period",
					"check_uid", checkJob.CheckUID, "limit", quotaErr.Limit)
				// Release the lease (which reschedules next_run_at) and skip the
				// outbound probe.
				return r.releaseLease(ctx, checkJob)
			}
			// Anything else is an unexpected resolve failure — log and
			// fall through so the check still runs (fail-open: better
			// to occasionally over-execute than to silently halt).
			logger.WarnContext(ctx, "ReserveCheckExecution failed; running check anyway",
				"error", rateErr)
		}
	}

	// 4. Execute check with a cost-aware timeout.
	// Use background context so the check can complete even during shutdown —
	// only the timeout should cancel the check, not the runner shutdown. The
	// ceiling is clamp(factor × cost_ewma, floor, 30s): a 200ms-p95 check no
	// longer reserves 30s of worst-case occupancy, while chronic 30s offenders
	// still hit the ceiling. With the cost-aware timeout disabled this is the
	// historical flat 30s.
	execTimeout := r.schedParams.ExecutionTimeout(checkJob.CostEWMAMs)
	execCtx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	execStart := time.Now()
	result, err := r.runCheckerGuarded(execCtx, logger, checker, checkConfig, checkJob, execTimeout, startTime)
	prommetrics.RecordCheckStage("execute", time.Since(execStart).Seconds())
	if err != nil {
		duration := time.Since(startTime)

		// Distinguish timeout (check took too long) from other errors
		if errors.Is(err, context.DeadlineExceeded) {
			logger.WarnContext(ctx, "Check execution timed out", "duration_ms", duration.Milliseconds())
			result = &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output: map[string]any{
					checkerdef.OutputKeyError: "check execution timed out",
				},
			}
		} else {
			logger.ErrorContext(ctx, "Check execution failed", "error", err)
			result = &checkerdef.Result{
				Status:   checkerdef.StatusError,
				Duration: duration,
				Output: map[string]any{
					checkerdef.OutputKeyError: err.Error(),
				},
			}
		}
	}

	// 5. Save result
	// Use a fallback context for cleanup operations if the main context is canceled
	saveCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		saveCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	r.stats.AddMetric(result.Status == checkerdef.StatusUp, result.Duration, delay)

	// Wire up the long-standing Prometheus counter + duration histogram.
	// They were defined in prommetrics but never observed in production
	// before this; without these two lines the existing
	// solidping_check_executions_total / _duration_seconds emit only
	// from tests.
	region := ""
	if checkJob.Region != nil {
		region = *checkJob.Region
	}
	prommetrics.RecordExecution(
		checkJob.Type,
		result.Status.String(),
		region,
		checkJob.OrganizationUID,
		float64(result.Duration.Milliseconds()),
	)
	prommetrics.RecordSchedulingDelay(region, delay.Seconds())

	saveStart := time.Now()
	if err := r.saveResult(saveCtx, checkJob, *result); err != nil {
		logger.ErrorContext(ctx, "Failed to save result", "error", err)
		// Continue to release lease even if save failed
	}
	prommetrics.RecordCheckStage("save_result", time.Since(saveStart).Seconds())
	r.logger.InfoContext(ctx, "Check result saved")

	// 6. Release lease and reschedule
	// Use a fallback context for cleanup operations if the main context is canceled
	releaseCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	releaseStart := time.Now()
	// Fold the just-measured cost into the same post-exec write (no extra
	// query on the hot path): update the EWMA and recompute the effective
	// deadline used for cost-aware claim ordering. Timeouts are pinned to the
	// ceiling so a chronic offender's cost reflects its worst-case occupancy.
	releaseErr := r.releaseLeaseWithCost(releaseCtx, checkJob, *result, execStart)
	prommetrics.RecordCheckStage("release_lease", time.Since(releaseStart).Seconds())
	if releaseErr != nil {
		return fmt.Errorf("failed to release lease: %w", releaseErr)
	}

	// duration_ms is the actual exec duration (D4) — previously this logged
	// time.Since(startTime), which conflated the pre-schedule sleep (wait_ms)
	// and the past-due dispatch delay (delay_ms) into the same number as the
	// probe's real cost. That conflation is what made
	// "duration_ms≈179000" for a genuinely sub-second check read as a hung
	// checker during live incident triage (see spec 2026-07-05-08 context).
	execDuration := time.Since(execStart)
	logger.InfoContext(ctx, "Check job completed",
		"status", result.Status,
		"duration_ms", execDuration.Milliseconds(),
		"wait_ms", wait.Milliseconds(),
		"delay_ms", delay.Milliseconds())

	return nil
}

// execOutcome carries a checker's Execute result (or panic) from the child
// goroutine to runCheckerGuarded's select (spec 2026-07-05-05 D1).
type execOutcome struct {
	result *checkerdef.Result
	err    error
}

// runCheckerGuarded runs checker.Execute in a child goroutine and abandons it
// if it does not return within execTimeout + abandonGrace (spec
// 2026-07-05-05 D1). This bounds the blast radius of a checker that ignores
// its context (or panics) to one goroutine instead of the runner that would
// otherwise be lost for the process lifetime.
//
// On the happy path (checker returns, or honors ctx and returns its own
// DeadlineExceeded/error within the grace window) this returns exactly what
// checker.Execute returned, so executeJob's existing
// errors.Is(err, context.DeadlineExceeded) / generic-error branches are
// unchanged (D1: additive, not a replacement).
//
// On a recovered panic (D2), it returns (nil, error) wrapping
// ErrCheckerPanic and the panic value/stack; executeJob's generic-error
// branch turns that into a StatusError result exactly like any other
// checker error.
//
// On abandonment (D3), it returns a fully-formed StatusTimeout result (nil
// error) so executeJob's save+release-lease steps run unchanged — the check
// surfaces as "timeout" instead of eternal "created". The child's eventual
// send lands in the cap-1 buffer nobody reads and is garbage-collected with
// the goroutine (D4): no double result, no double lease release.
func (r *CheckWorker) runCheckerGuarded(
	ctx context.Context,
	logger *slog.Logger,
	checker checkerdef.Checker,
	checkConfig checkerdef.Config,
	checkJob *models.CheckJob,
	execTimeout time.Duration,
	startTime time.Time,
) (*checkerdef.Result, error) {
	outcomeCh := make(chan execOutcome, 1) // cap 1: a late send from an abandoned child never blocks

	// abandoned is read by the child goroutine's deferred cleanup and written
	// by the select below — two different goroutines, no channel or lock
	// between the write and that read, so a plain bool would be a data race.
	// atomic.Bool makes the write/read pair safe without adding a mutex.
	var abandoned atomic.Bool

	go func() {
		defer func() {
			if p := recover(); p != nil {
				outcomeCh <- execOutcome{err: fmt.Errorf("%w: %v\n%s", ErrCheckerPanic, p, debug.Stack())}
			}

			if abandoned.Load() {
				prommetrics.DecCheckRunnerAbandonedActive()
			}
		}()

		res, execErr := checker.Execute(ctx, checkConfig)
		outcomeCh <- execOutcome{result: res, err: execErr}
	}()

	select {
	case out := <-outcomeCh:
		return out.result, out.err
	case <-time.After(execTimeout + effectiveAbandonGrace()):
		abandoned.Store(true)

		return r.abandonCheckerExecution(ctx, logger, checkJob, time.Since(startTime))
	}
}

// abandonCheckerExecution implements the D3 abandon branch: loud logging,
// the two watchdog metrics, and a saved StatusTimeout result shaped exactly
// like the spec's D3 output.
func (r *CheckWorker) abandonCheckerExecution(
	ctx context.Context,
	logger *slog.Logger,
	checkJob *models.CheckJob,
	duration time.Duration,
) (*checkerdef.Result, error) {
	region := ""
	if checkJob.Region != nil {
		region = *checkJob.Region
	}

	prommetrics.RecordCheckRunnerAbandoned(checkJob.Type)
	prommetrics.IncCheckRunnerAbandonedActive()

	logger.ErrorContext(ctx, "Checker abandoned: did not honor context",
		"check_uid", checkJob.CheckUID,
		"check_type", checkJob.Type,
		"org", checkJob.OrganizationUID,
		"region", region,
		"abandon_count", 1)

	return &checkerdef.Result{
		Status:   checkerdef.StatusTimeout,
		Duration: duration,
		Output: map[string]any{
			checkerdef.OutputKeyError: ErrCheckerAbandoned.Error(),
		},
	}, nil
}

// costSampleMs derives the cost sample (ms) to fold into the EWMA from a result.
// A timeout is pinned to the execution ceiling so a perpetually-timing-out check
// converges to the maximum cost (and stays slow-classified) rather than being
// scored by the partial duration measured before the deadline fired.
func (r *CheckWorker) costSampleMs(result checkerdef.Result) float64 {
	if result.Status == checkerdef.StatusTimeout {
		return float64(scheduling.DefaultExecutionTimeout.Milliseconds())
	}

	return float64(result.Duration.Milliseconds())
}

// releaseLeaseWithCost releases the lease for an actively-probed check, folding
// the new cost and delay EWMAs, the recomputed effective_scheduled_at, and the
// hysteresis-classified lane into the same write. The effective deadline and
// the lane are computed from the cost EWMA only (specs 2026-07-01-02 /
// 2026-07-01-03): the delay EWMA is persisted as telemetry but never steers
// the claim order nor the lane — delay is a victim signal, and classifying on
// it would send starved fast checks into the slow lane. execStart is when the
// outbound probe actually began.
func (r *CheckWorker) releaseLeaseWithCost(
	ctx context.Context,
	checkJob *models.CheckJob,
	result checkerdef.Result,
	execStart time.Time,
) error {
	nextScheduledAt := r.calculateNextScheduledAt(checkJob)
	newCost := scheduling.UpdateEWMA(checkJob.CostEWMAMs, r.costSampleMs(result))
	newDelay := scheduling.UpdateEWMA(checkJob.DelayEWMAMs, r.delaySampleMs(checkJob, execStart))
	effective := r.schedParams.EffectiveScheduledAt(nextScheduledAt, newCost, checkJob.PlanWeight)
	newLane := scheduling.ClassifyLane(checkJob.Lane, newCost, r.schedParams)

	return r.checkJobSvc.ReleaseLeaseWithSchedulingState(
		ctx, checkJob.UID, r.getWorker().UID, nextScheduledAt, newCost, newDelay, effective, newLane,
	)
}

// delaySampleMs is the scheduling-delay sample (ms) folded into the delay EWMA:
// how far past the job's real scheduled_at the probe actually started, floored
// at 0 — true start lateness vs the schedule the user configured (spec
// 2026-07-01-02 D4). The runner sleeps until scheduled_at, so under spare
// capacity the probe starts on time and this is 0; it only goes positive when
// contention pushes the start past the schedule. Pure telemetry: it feeds the
// cost-distribution endpoint and the lane-split go/no-go, never the claim
// order, so there is no feedback loop. Falls back to effective_scheduled_at
// when the row has no scheduled_at (should not happen in practice).
func (r *CheckWorker) delaySampleMs(checkJob *models.CheckJob, execStart time.Time) float64 {
	ref := checkJob.ScheduledAt
	if ref == nil {
		ref = checkJob.EffectiveScheduledAt
	}
	if ref == nil {
		return 0
	}

	lateness := execStart.Sub(*ref)
	if lateness < 0 {
		return 0
	}

	return float64(lateness.Milliseconds())
}

// saveResult saves a check result to the database and processes incidents.
func (r *CheckWorker) saveResult(ctx context.Context, checkJob *models.CheckJob, checkResult checkerdef.Result) error {
	resultUID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("failed to generate result UID: %w", err)
	}

	status := int(checkResult.Status)
	durationMs := float32(checkResult.Duration.Seconds() * 1000)
	lastForStatus := true
	worker := r.getWorker()

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: checkJob.OrganizationUID,
		CheckUID:        checkJob.CheckUID,
		PeriodType:      periodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &worker.UID,
		Region:          checkJob.Region,
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         models.JSONMap(checkResult.Metrics),
		Output:          models.JSONMap(checkResult.Output),
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	if err := r.dbService.SaveResultWithStatusTracking(ctx, result); err != nil {
		return err
	}

	// Process incidents
	incStart := time.Now()
	r.processIncidents(ctx, checkJob, result)
	prommetrics.RecordCheckStage("process_incident", time.Since(incStart).Seconds())

	return nil
}

// processIncidents handles incident creation/resolution based on check results.
func (r *CheckWorker) processIncidents(ctx context.Context, checkJob *models.CheckJob, result *models.Result) {
	// The check is normally attached at claim time (see checkjobsvc.attachChecks)
	// so the incident hot path avoids a per-result GetCheck round-trip. Fall back
	// to a fetch only when it is missing (e.g. claimed before this change, or a
	// batch-fetch scan failure left it nil).
	check := checkJob.Check
	if check == nil {
		var err error
		check, err = r.dbService.GetCheck(ctx, checkJob.OrganizationUID, checkJob.CheckUID)
		if err != nil {
			r.logger.WarnContext(ctx, "Failed to fetch check for incident processing", "error", err)

			return
		}
	}

	if err := r.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		r.logger.WarnContext(ctx, "Failed to process check result for incidents", "error", err)
	}
}

// saveErrorResult saves an error result when check execution fails.
func (r *CheckWorker) saveErrorResult(ctx context.Context, checkJob *models.CheckJob, err error) error {
	resultUID, uidErr := uuid.NewV7()
	if uidErr != nil {
		return fmt.Errorf("failed to generate result UID: %w", uidErr)
	}

	status := int(checkerdef.StatusError)
	durationMs := float32(0)
	lastForStatus := true
	worker := r.getWorker()

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: checkJob.OrganizationUID,
		CheckUID:        checkJob.CheckUID,
		PeriodType:      periodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &worker.UID,
		Region:          checkJob.Region,
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         make(models.JSONMap),
		Output:          models.JSONMap{checkerdef.OutputKeyError: err.Error()},
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	// Use a fallback context for cleanup operations if the main context is canceled
	saveCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		saveCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	insertErr := r.dbService.SaveResultWithStatusTracking(saveCtx, result)

	// Process incidents for error results
	r.processIncidents(saveCtx, checkJob, result)

	// Also release the lease using fallback context
	releaseCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	_ = r.releaseLease(releaseCtx, checkJob)

	return insertErr
}

// releaseLease releases the job lease and reschedules for next execution.
func (r *CheckWorker) releaseLease(ctx context.Context, checkJob *models.CheckJob) error {
	// Parse period and calculate next scheduled time
	nextScheduledAt := r.calculateNextScheduledAt(checkJob)

	return r.checkJobSvc.ReleaseLease(ctx, checkJob.UID, r.getWorker().UID, nextScheduledAt)
}

// isPassiveCheckType reports whether a check type is passive — driven by
// inbound signals (HTTP heartbeats, incoming emails) rather than outbound
// probes. Passive checks share the same overdue/grace-period logic.
func isPassiveCheckType(t checkerdef.CheckType) bool {
	return t == checkerdef.CheckTypeHeartbeat || t == checkerdef.CheckTypeEmail
}

// passiveSignalNoun returns the human-readable noun used in result messages
// for the given passive check type ("heartbeat" / "email").
func passiveSignalNoun(t checkerdef.CheckType) string {
	if t == checkerdef.CheckTypeEmail {
		return "Email"
	}

	return "Heartbeat"
}

// executePassiveJob handles passive check jobs (heartbeat, email).
// Instead of making a network request, it inspects whether a recent inbound
// signal landed within the check's period.
func (r *CheckWorker) executePassiveJob(ctx context.Context, logger *slog.Logger, checkJob *models.CheckJob) error {
	period := time.Duration(checkJob.Period)
	noun := passiveSignalNoun(checkerdef.CheckType(checkJob.Type))

	// Get the latest result for this check
	lastResults, err := r.dbService.GetLastResultForChecks(ctx, []string{checkJob.CheckUID})
	if err != nil {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("failed to get last result: %w", err))
	}

	// Determine status based on recency of last passive signal
	status := checkerdef.StatusDown
	output := map[string]any{outputKeyMessage: "No " + strings.ToLower(noun) + " received"}

	if lastResult, ok := lastResults[checkJob.CheckUID]; ok && lastResult.Status != nil {
		elapsed := time.Since(lastResult.PeriodStart)

		switch {
		// Last result was UP and recent enough
		case *lastResult.Status == int(checkerdef.StatusUp) && elapsed <= period:
			status = checkerdef.StatusUp
			output = map[string]any{
				outputKeyMessage: noun + " received",
				"lastSignalAt":   lastResult.PeriodStart.Format(time.RFC3339),
			}

		// Last result was UP but overdue
		case *lastResult.Status == int(checkerdef.StatusUp):
			output = map[string]any{
				outputKeyMessage: noun + " overdue",
				"lastSignalAt":   lastResult.PeriodStart.Format(time.RFC3339),
				"overdueBy":      (elapsed - period).String(),
			}

		// Last result was RUNNING and still within grace period (2x period)
		case *lastResult.Status == int(checkerdef.StatusRunning) && elapsed <= period*2:
			status = checkerdef.StatusRunning
			output = map[string]any{
				outputKeyMessage: "Run in progress",
				"runStarted":     lastResult.PeriodStart.Format(time.RFC3339),
			}

		// Last result was RUNNING but exceeded grace period — stale run
		case *lastResult.Status == int(checkerdef.StatusRunning):
			status = checkerdef.StatusTimeout
			output = map[string]any{
				outputKeyMessage: "Run started but never completed",
				"runStarted":     lastResult.PeriodStart.Format(time.RFC3339),
				"overdueBy":      (elapsed - period*2).String(),
			}
		}
	}

	result := checkerdef.Result{
		Status:   status,
		Duration: 0,
		Metrics:  make(map[string]any),
		Output:   output,
	}

	r.stats.AddMetric(result.Status == checkerdef.StatusUp, result.Duration, 0)

	if err := r.saveResult(ctx, checkJob, result); err != nil {
		logger.ErrorContext(ctx, "Failed to save passive check result", "error", err)
	}

	if err := r.releaseLease(ctx, checkJob); err != nil {
		return fmt.Errorf("failed to release lease: %w", err)
	}

	logger.InfoContext(ctx, "Passive check completed",
		"type", checkJob.Type,
		"status", result.Status,
		"check_uid", checkJob.CheckUID)

	return nil
}

// waitAndDelay splits a job's sleepTime (scheduled_at − dispatch time) into
// the pre-schedule wait (claimed ahead of schedule, sleepTime > 0) and the
// past-due delay (claimed late, sleepTime < 0). Exactly one return value is
// ever non-zero; sleepTime == 0 returns (0, 0) (fires exactly on schedule).
// Pure function so the D4 wait/delay separation is unit-testable without
// driving executeJob end-to-end.
func waitAndDelay(sleepTime time.Duration) (wait, delay time.Duration) {
	switch {
	case sleepTime > 0:
		return sleepTime, 0
	case sleepTime < 0:
		return 0, -sleepTime
	default:
		return 0, 0
	}
}

// calculateNextScheduledAt calculates the next scheduled time for a check job.
//
// Phase-locked rescheduling (spec 2026-07-05-08 D1): when the job's check is
// attached (populated at claim time by ClaimJobs/ClaimJobsForCheck — always
// true for jobs flowing through the regular runner pool), the next tick is
// aligned to the job's deterministic phase (scheduling.NextAligned) rather
// than computed as "scheduled_at + period" or "now + period". This makes
// rescheduling immune to lateness: a late or missed run resumes at the next
// phase-aligned tick instead of re-anchoring to the claim-batch timestamp,
// which is what previously made every job in a cohort lock-step onto the
// same tick after a restart (F2).
//
// Falls back to the old anchor-preserving/catch-up logic when there is no
// attached check (defensive only — the regular claim path always attaches
// one) or when either period is non-positive.
func (r *CheckWorker) calculateNextScheduledAt(checkJob *models.CheckJob) time.Time {
	now := time.Now()
	jobPeriod := time.Duration(checkJob.Period)

	if checkJob.Check != nil {
		basePeriod := time.Duration(checkJob.Check.Period)
		if basePeriod > 0 && jobPeriod > 0 {
			return scheduling.NextAligned(
				now, basePeriod, jobPeriod, checkJob.CheckUID, checkJob.Region, checkJob.Check.Regions,
			)
		}
	}

	if checkJob.ScheduledAt == nil {
		// No scheduled_at, schedule for now + period
		return now.Add(jobPeriod)
	}

	nextScheduled := checkJob.ScheduledAt.Add(jobPeriod)

	if nextScheduled.After(now) {
		// We're on schedule
		return nextScheduled
	}

	// We're behind schedule, catch up
	return now.Add(jobPeriod)
}

// setupSelfStats configures self-stats reporting for the worker.
func (r *CheckWorker) setupSelfStats(ctx context.Context) error {
	// Get the default organization
	org, err := r.dbService.GetOrganizationBySlug(ctx, "default")
	if err != nil {
		return fmt.Errorf("failed to get default organization: %w", err)
	}
	r.defaultOrgUID = org.UID

	// Create or get the internal check
	if err := r.createInternalCheck(ctx); err != nil {
		return fmt.Errorf("failed to create internal check: %w", err)
	}

	// Wire up the stats reporter
	r.stats.SetReporter(r.reportStats)
	r.stats.SetFreeRunnersFunc(func() float64 {
		return float64(r.availableRunners.Load())
	})

	r.logger.InfoContext(ctx, "Self-stats reporting configured",
		"internal_check_uid", r.internalCheckUID)

	return nil
}

// createInternalCheck creates or retrieves the internal check for this worker.
func (r *CheckWorker) createInternalCheck(ctx context.Context) error {
	worker := r.getWorker()
	slug := "int-checks-" + worker.Slug

	// Check if already exists
	existing, err := r.dbService.GetCheckByUidOrSlug(ctx, r.defaultOrgUID, slug)
	if err == nil && existing != nil {
		r.internalCheckUID = existing.UID

		// Fix legacy checks that had type "internal:checkworker" and internal=false
		if !existing.Internal || existing.Type != "checkworker" {
			internalTrue := true
			newType := "checkworker"
			_ = r.dbService.UpdateCheck(ctx, existing.UID, &models.CheckUpdate{
				Internal: &internalTrue,
				Type:     &newType,
			})
		}

		return nil
	}

	// Create new internal check
	check := models.NewCheck(r.defaultOrgUID, slug, "checkworker")
	name := "Check Worker: " + worker.Name
	check.Name = &name
	check.Enabled = false // Don't schedule it as a regular check
	check.Internal = true

	if err := r.dbService.CreateCheck(ctx, check); err != nil {
		return err
	}

	r.internalCheckUID = check.UID
	return nil
}

// reportStats saves worker stats as a result to the database.
func (r *CheckWorker) reportStats(reported stats.ReportedStats) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Determine status: UP if at least one check succeeded
	status := int(models.ResultStatusUp)
	if reported.TotalChecks == 0 || reported.TotalChecks == reported.FailedChecks {
		status = int(models.ResultStatusDown)
	}

	resultUID, err := uuid.NewV7()
	if err != nil {
		r.logger.Error("Failed to generate result UID for self-stats", "error", err)
		return
	}

	worker := r.getWorker()
	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: r.defaultOrgUID,
		CheckUID:        r.internalCheckUID,
		PeriodType:      periodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &worker.UID,
		Status:          &status,
		Metrics: models.JSONMap{
			"job_runs":         reported.TotalChecks,
			"free_runners":     reported.FreeRunners,
			"average_duration": reported.AverageDuration,
			"average_delay":    reported.AverageDelay,
		},
		CreatedAt: time.Now(),
	}

	if err := r.dbService.CreateResult(ctx, result); err != nil {
		r.logger.Error("Failed to save self-stats result", "error", err)
	}
}
