package checkworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Multi-step run limits (spec 2026-10-03-03).
const (
	// defaultMaxRunDuration is how long a run may last before its next slice
	// is Final, when the config does not say.
	defaultMaxRunDuration = 30 * time.Minute
	// maxMaxRunDuration caps any configured run duration.
	maxMaxRunDuration = 2 * time.Hour
	// maxStepFailures consecutive failed slices end the run with an error.
	maxStepFailures = 3
	// stepRateDeferral is how far a run in progress is pushed back when the
	// org's bucket granted no unit: the bucket refills per minute, so the
	// next slice waits one minute rather than the check's whole period.
	stepRateDeferral = time.Minute
	// stepMarginBudget is the context margin over the slice budget, the same
	// margin rule as the global timeout (spec 2026-07-10-11).
	stepMarginBudget = time.Second
	// stepSaveTimeout bounds the bookkeeping of a slice during shutdown.
	stepSaveTimeout = 5 * time.Second
	// OutputKeyReportFileUID names the stored report of a finished run.
	OutputKeyReportFileUID = "reportFileUid"
)

// Errors of the multi-step path.
var (
	// ErrStepNotSupported means this worker's backend cannot persist step
	// state (an agent): multi-step checks run on the server's workers only.
	ErrStepNotSupported = errors.New("multi-step checks are not supported on this worker")
	// ErrStepFailedTooOften ends a run after maxStepFailures failed slices.
	ErrStepFailedTooOften = errors.New("multi-step run failed 3 slices in a row")
)

// stepRun is the identity of the run a slice belongs to.
type stepRun struct {
	uid       string
	startedAt time.Time
	step      int // slices completed so far
	state     []byte
}

// stepCheckerFor returns the step checker of a multi-step type.
func (r *CheckWorker) stepCheckerFor(checkType checkerdef.CheckType) (checkerdef.StepChecker, bool) {
	if !checkType.IsMultiStep() {
		return nil, false
	}

	checker, ok := r.getChecker(checkType)
	if !ok {
		return nil, false
	}

	stepChecker, ok := checker.(checkerdef.StepChecker)

	return stepChecker, ok
}

// executeStepJob runs ONE slice of a multi-step run (spec 2026-10-03-03 §1.5):
// start or resume the run, reserve its units, run Step under the slice budget,
// then either save the state and release (no result row) or, when the run is
// done, store the report and submit the run's single result.
func (r *CheckWorker) executeStepJob(
	ctx context.Context, logger *slog.Logger, checkJob *models.CheckJob, checker checkerdef.StepChecker,
) error {
	steps, ok := r.backend.(backend.StepBackend)
	if !ok {
		return r.saveErrorResult(ctx, checkJob, ErrStepNotSupported)
	}

	config, ok := r.parseConfig(checkerdef.CheckType(checkJob.Type))
	if !ok {
		return r.saveStepErrorResult(ctx, checkJob, nil, fmt.Errorf("%w: %s", ErrUnknownCheckType, checkJob.Type))
	}

	if err := config.FromMap(checkJob.Config); err != nil {
		return r.saveStepErrorResult(ctx, checkJob, nil, fmt.Errorf("%w: %w", ErrFailedToParseConf, err))
	}

	run := r.resolveStepRun(ctx, logger, checkJob, steps)

	granted, deferred, rateErr := r.reserveStepUnits(ctx, logger, checkJob, checker.UnitsPerSlice())
	if deferred {
		return rateErr
	}

	input := checkerdef.StepInput{
		State:    run.state,
		Budget:   r.schedParams.EffectiveBulkSliceBudget(),
		MaxUnits: granted,
		Final:    time.Since(run.startedAt) >= maxRunDurationOf(config),
	}

	out, stepErr := r.runStep(ctx, logger, checkJob, checker, config, input)
	if stepErr != nil {
		return r.failStep(ctx, logger, checkJob, run, stepErr)
	}

	r.refundStepUnits(ctx, checkJob, granted-out.Units)

	if !out.Done {
		saved, err := r.saveStep(ctx, logger, checkJob, steps, run, &out)
		if err != nil {
			return r.failStep(ctx, logger, checkJob, run, err)
		}

		if !saved {
			// The state was over the cap: the same slice re-ran as Final.
			out, stepErr = r.runStep(ctx, logger, checkJob, checker, config, checkerdef.StepInput{
				State: out.State, Budget: input.Budget, Final: true,
			})
			if stepErr != nil {
				return r.failStep(ctx, logger, checkJob, run, stepErr)
			}
		}
	}

	if !out.Done {
		return nil
	}

	return r.finishStepRun(ctx, logger, checkJob, steps, checker, run, &out)
}

// resolveStepRun starts a run (no run in progress) or resumes the current one.
// A state file that cannot be read restarts the run from scratch.
func (r *CheckWorker) resolveStepRun(
	ctx context.Context, logger *slog.Logger, checkJob *models.CheckJob, steps backend.StepBackend,
) stepRun {
	fresh := stepRun{uid: uuid.NewString(), startedAt: time.Now()}

	if checkJob.StepRunUID == nil {
		return fresh
	}

	run := stepRun{uid: *checkJob.StepRunUID, startedAt: time.Now(), step: checkJob.StepCount}
	if checkJob.StepRunStartedAt != nil {
		run.startedAt = *checkJob.StepRunStartedAt
	}

	if checkJob.StepStateFileUID == nil {
		// Only failed slices so far: resume the run with no state.
		return run
	}

	state, err := steps.LoadStepState(ctx, checkJob)
	if err != nil {
		logger.WarnContext(ctx, "Multi-step state unreadable; restarting the run",
			"check_uid", checkJob.CheckUID, "run_uid", run.uid, "error", err)

		return fresh
	}

	run.state = state

	return run
}

// maxRunDurationOf reads the config's run deadline (default 30 min, max 2 h).
func maxRunDurationOf(config checkerdef.Config) time.Duration {
	hint, ok := config.(checkerdef.MaxRunDurationHint)
	if !ok || hint.MaxRunDuration() <= 0 {
		return defaultMaxRunDuration
	}

	return min(hint.MaxRunDuration(), maxMaxRunDuration)
}

// reserveStepUnits draws up to n units from the org's checks-per-minute
// bucket. A drained bucket defers the job without running a slice: a run in
// progress comes back in a minute, a run not yet started at its next tick.
func (r *CheckWorker) reserveStepUnits(
	ctx context.Context, logger *slog.Logger, checkJob *models.CheckJob, n int,
) (int, bool, error) {
	entSvc := r.entitlementsService()
	if entSvc == nil || checkJob.IsInternal() {
		return n, false, nil
	}

	granted, err := entSvc.ReserveCheckExecutionsUpTo(ctx, checkJob.OrganizationUID, n)
	if err != nil {
		logger.WarnContext(ctx, "ReserveCheckExecutionsUpTo failed; running the slice anyway", "error", err)

		return n, false, nil
	}

	if granted > 0 {
		return granted, false, nil
	}

	prommetrics.ChecksRateLimited.WithLabelValues(checkJob.OrganizationUID).Inc()
	entSvc.RecordRateLimitedSkip(ctx, checkJob.OrganizationUID)

	logger.InfoContext(ctx, "Multi-step slice rate-limited; deferring", "check_uid", checkJob.CheckUID)

	next := r.calculateNextScheduledAt(checkJob)
	if checkJob.StepRunUID != nil {
		next = time.Now().Add(stepRateDeferral)
	}

	return 0, true, r.backend.DeferRateLimited(ctx, checkJob, r.getWorker().UID, next)
}

func (r *CheckWorker) refundStepUnits(ctx context.Context, checkJob *models.CheckJob, unused int) {
	if unused <= 0 || checkJob.IsInternal() {
		return
	}

	if entSvc := r.entitlementsService(); entSvc != nil {
		entSvc.RefundCheckExecutions(ctx, checkJob.OrganizationUID, unused)
	}
}

// runStep calls Step under the slice budget + margin, with the worker's egress
// policy, recovering a panic and abandoning a checker that ignores its
// context.
func (r *CheckWorker) runStep( //nolint:contextcheck // the slice context is detached on purpose
	ctx context.Context,
	logger *slog.Logger,
	checkJob *models.CheckJob,
	checker checkerdef.StepChecker,
	config checkerdef.Config,
	input checkerdef.StepInput,
) (checkerdef.StepOutput, error) {
	// Detached on purpose, like executeJob: only the slice budget cancels a
	// slice, never the runner's shutdown.
	execCtx, cancel := context.WithTimeout(context.Background(), input.Budget+stepMarginBudget)
	defer cancel()

	execCtx, _ = r.withEgress(execCtx)

	type outcome struct {
		out checkerdef.StepOutput
		err error
	}

	done := make(chan outcome, 1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- outcome{err: fmt.Errorf("%w: %v\n%s", ErrCheckerPanic, p, debug.Stack())}
			}
		}()

		out, err := checker.Step(execCtx, config, input)
		done <- outcome{out: out, err: err}
	}()

	select {
	case res := <-done:
		if res.err == nil && res.out.Done && res.out.Result == nil {
			return res.out, fmt.Errorf("%w: a Done slice carried no result", ErrCheckerAbandoned)
		}

		return res.out, res.err
	case <-time.After(input.Budget + stepMarginBudget + effectiveAbandonGrace()):
		logger.ErrorContext(ctx, "Multi-step slice abandoned: did not honor context",
			"check_uid", checkJob.CheckUID)

		return checkerdef.StepOutput{}, ErrCheckerAbandoned
	}
}

// saveStep writes the next state and releases the lease (no result row, no
// incident evaluation). It reports false, without writing anything, when the
// state is over the cap: the caller re-runs the slice as Final.
func (r *CheckWorker) saveStep(
	ctx context.Context,
	logger *slog.Logger,
	checkJob *models.CheckJob,
	steps backend.StepBackend,
	run stepRun,
	out *checkerdef.StepOutput,
) (bool, error) {
	fileUID, err := steps.SaveStepState(ctx, checkJob, &backend.SaveStepStateRequest{
		RunUID: run.uid, Step: run.step + 1, Payload: out.State, Progress: out.Progress,
	})
	if errors.Is(err, backend.ErrStepStateTooLarge) {
		logger.WarnContext(ctx, "Multi-step state over the cap; finishing the run now",
			"check_uid", checkJob.CheckUID, "run_uid", run.uid, "bytes", len(out.State))

		return false, nil
	}

	if err != nil {
		return true, fmt.Errorf("save step state: %w", err)
	}

	saveCtx, cancel := saveContext(ctx)
	defer cancel()

	if submitErr := steps.SubmitStep(saveCtx, checkJob, r.getWorker().UID, &backend.SubmitStepRequest{
		RunUID: run.uid, RunStartedAt: run.startedAt, StateFileUID: &fileUID, NextAt: time.Now(),
	}); submitErr != nil {
		// Fenced out: our lease expired and another worker owns the run. Our
		// state file is an orphan the keep-2 prune retires.
		logger.WarnContext(ctx, "Multi-step slice not recorded", "check_uid", checkJob.CheckUID, "error", submitErr)
	}

	return true, nil
}

// failStep counts a failed slice: the previous state is kept and the slice is
// requeued; the third consecutive failure ends the run with an error result.
func (r *CheckWorker) failStep(
	ctx context.Context, logger *slog.Logger, checkJob *models.CheckJob, run stepRun, cause error,
) error {
	logger.WarnContext(ctx, "Multi-step slice failed",
		"check_uid", checkJob.CheckUID, "run_uid", run.uid, "failures", checkJob.StepFailures+1, "error", cause)

	if checkJob.StepFailures+1 >= maxStepFailures {
		return r.saveStepErrorResult(ctx, checkJob, &run, fmt.Errorf("%w: %w", ErrStepFailedTooOften, cause))
	}

	steps, _ := r.backend.(backend.StepBackend)

	saveCtx, cancel := saveContext(ctx)
	defer cancel()

	return steps.SubmitStep(saveCtx, checkJob, r.getWorker().UID, &backend.SubmitStepRequest{
		RunUID: run.uid, RunStartedAt: run.startedAt, Failed: true, NextAt: time.Now(),
	})
}

// finishStepRun stores the report, diffs it against the previous run's, and
// submits the run's one result through the normal path, clearing the run.
func (r *CheckWorker) finishStepRun(
	ctx context.Context,
	logger *slog.Logger,
	checkJob *models.CheckJob,
	steps backend.StepBackend,
	checker checkerdef.StepChecker,
	run stepRun,
	out *checkerdef.StepOutput,
) error {
	saveCtx, cancel := saveContext(ctx)
	defer cancel()

	result := out.Result

	if result.Output == nil {
		result.Output = map[string]any{}
	}

	if differ, ok := checker.(checkerdef.ReportDiffer); ok {
		previous, err := steps.PreviousStepReport(saveCtx, checkJob)
		if err != nil {
			logger.WarnContext(ctx, "Previous multi-step report unreadable", "check_uid", checkJob.CheckUID, "error", err)
		}

		differ.DiffAgainst(previous, out)
	}

	if len(out.Report) > 0 {
		fileUID, err := steps.SaveStepReport(saveCtx, checkJob, run.uid, out.Report)
		if err != nil {
			logger.WarnContext(ctx, "Failed to store the multi-step report", "check_uid", checkJob.CheckUID, "error", err)
		} else {
			result.Output[OutputKeyReportFileUID] = fileUID
		}
	}

	r.stats.AddMetric(result.Status == checkerdef.StatusUp, result.Duration, 0)

	runUID := run.uid
	req := &backend.SubmitResultRequest{
		Status:          int(result.Status),
		Duration:        float32(result.Duration.Seconds() * 1000),
		Metrics:         result.Metrics,
		Output:          result.Output,
		Region:          r.resolveResultRegion(checkJob),
		NextScheduledAt: r.calculateNextScheduledAtFrom(checkJob, run.startedAt),
		StepRunUID:      &runUID,
	}

	if err := r.backend.SubmitResult(saveCtx, checkJob, r.getWorker().UID, req); err != nil {
		return fmt.Errorf("failed to submit multi-step result: %w", err)
	}

	logger.InfoContext(ctx, "Multi-step run completed",
		"check_uid", checkJob.CheckUID, "run_uid", run.uid, "status", result.Status, "slices", run.step+1)

	return nil
}

// saveStepErrorResult ends a run (or a run that could not start) with an
// error result, clearing the step_* columns.
func (r *CheckWorker) saveStepErrorResult(
	ctx context.Context, checkJob *models.CheckJob, run *stepRun, cause error,
) error {
	runUID := ""
	anchor := time.Now()

	if run != nil {
		runUID = run.uid
		anchor = run.startedAt
	} else if checkJob.StepRunUID != nil {
		runUID = *checkJob.StepRunUID
	}

	req := &backend.SubmitResultRequest{
		Status:          int(checkerdef.StatusError),
		Metrics:         map[string]any{},
		Output:          map[string]any{checkerdef.OutputKeyError: cause.Error()},
		Region:          r.resolveResultRegion(checkJob),
		NextScheduledAt: r.calculateNextScheduledAtFrom(checkJob, anchor),
		StepRunUID:      &runUID,
	}

	saveCtx, cancel := saveContext(ctx)
	defer cancel()

	return r.backend.SubmitResult(saveCtx, checkJob, r.getWorker().UID, req)
}

// saveContext is ctx, or a short background context when ctx is canceled
// (shutdown), so a slice's bookkeeping still lands.
func saveContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}

	return context.WithTimeout(context.Background(), stepSaveTimeout)
}
