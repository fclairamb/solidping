package crawltask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Slice timings of the executor.
const (
	// stepMargin is the context margin over the slice budget, the margin
	// rule of the global timeout (spec 2026-07-10-11).
	stepMargin = time.Second
	// abandonGrace is how long a checker that ignores its context is waited
	// for before the slice is given up.
	abandonGrace = 5 * time.Second
)

var (
	errAbandoned = errors.New("crawl slice did not honor its context")
	errNoResult  = errors.New("a done crawl slice carried no result")
)

// Executor is the worker side of the crawl kind: it runs one StepChecker
// slice per claim. The loop puts the worker's egress guard on its context.
type Executor struct {
	getChecker  func(checkerdef.CheckType) (checkerdef.Checker, bool)
	parseConfig func(checkerdef.CheckType) (checkerdef.Config, bool)
	logger      *slog.Logger
}

var _ tasks.Executor = (*Executor)(nil)

// NewExecutor builds the executor over the checker registry.
func NewExecutor() *Executor {
	return &Executor{
		getChecker:  registry.GetChecker,
		parseConfig: registry.ParseConfig,
		logger:      slog.Default().With("component", "crawl_task"),
	}
}

// Kind implements tasks.Executor.
func (*Executor) Kind() string { return Kind }

// Capability implements tasks.Executor.
func (*Executor) Capability() string { return Capability }

// Run implements tasks.Executor: decode the input, run one slice, then report
// the next state, the run's result, or the failure.
func (e *Executor) Run(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error {
	var input Input
	if err := json.Unmarshal(claim.Input, &input); err != nil {
		return rep.Fail(ctx, codeInvalidInput)
	}

	checkType := checkerdef.CheckType(input.Type)

	checker, ok := e.getChecker(checkType)
	stepChecker, isStep := checker.(checkerdef.StepChecker)

	if !ok || !isStep {
		return rep.Fail(ctx, codeUnsupported)
	}

	config, ok := e.parseConfig(checkType)
	if !ok {
		return rep.Fail(ctx, codeUnsupported)
	}

	if err := config.FromMap(input.Config); err != nil {
		e.logger.WarnContext(ctx, "Crawl config rejected", "task", claim.UID, "error", err)

		return rep.Fail(ctx, codeInvalidConfig)
	}

	budget := time.Duration(claim.SliceBudgetMs) * time.Millisecond
	if budget <= 0 {
		budget = scheduling.DefaultBulkSliceBudget
	}

	in := checkerdef.StepInput{State: claim.State, Budget: budget, MaxUnits: claim.Units, Final: claim.Final}

	out, err := runStep(ctx, stepChecker, config, in)
	if err != nil {
		e.logger.WarnContext(ctx, "Crawl slice failed", "task", claim.UID, "error", err)

		return rep.Fail(ctx, codeStepFailed)
	}

	if !out.Done {
		stepErr := rep.Step(ctx, workertasks.StepRequest{State: out.State, Progress: out.Progress, Units: out.Units})
		if !errors.Is(stepErr, workertasks.ErrTooLarge) {
			return stepErr
		}

		// The state is over the cap and was not saved: finish the run now
		// with what this slice has, without fetching anything more.
		e.logger.WarnContext(ctx, "Crawl state over the cap; finishing the run", "task", claim.UID,
			"bytes", len(out.State))

		used := out.Units

		out, err = runStep(ctx, stepChecker, config, checkerdef.StepInput{State: out.State, Budget: budget, Final: true})
		if err != nil {
			return rep.Fail(ctx, codeStepFailed)
		}

		out.Units += used
	}

	return finish(ctx, rep, &out)
}

// finish sends the run's result. A body over the request cap is sent again
// without its report: the result matters more than the full findings list.
func finish(ctx context.Context, rep tasks.Reporter, out *checkerdef.StepOutput) error {
	body := Result{
		Status:     int(out.Result.Status),
		DurationMs: float64(out.Result.Duration.Milliseconds()),
		Metrics:    out.Result.Metrics,
		Output:     out.Result.Output,
		Units:      out.Units,
	}

	if len(out.Report) > 0 && json.Valid(out.Report) {
		body.Report = out.Report
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode crawl result: %w", err)
	}

	err = rep.Finish(ctx, raw)
	if !errors.Is(err, workertasks.ErrTooLarge) || body.Report == nil {
		return err
	}

	body.Report = nil

	if raw, err = json.Marshal(body); err != nil {
		return fmt.Errorf("encode crawl result: %w", err)
	}

	return rep.Finish(ctx, raw)
}

// runStep calls Step under the slice budget plus margin, and gives up on a
// checker that ignores its context. A panic is the loop's to recover.
func runStep(
	ctx context.Context, checker checkerdef.StepChecker, config checkerdef.Config, in checkerdef.StepInput,
) (checkerdef.StepOutput, error) {
	stepCtx, cancel := context.WithTimeout(ctx, in.Budget+stepMargin)
	defer cancel()

	type outcome struct {
		out checkerdef.StepOutput
		err error
		pan any
	}

	done := make(chan outcome, 1)

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- outcome{pan: recovered}
			}
		}()

		out, err := checker.Step(stepCtx, config, in)
		done <- outcome{out: out, err: err}
	}()

	timer := time.NewTimer(in.Budget + stepMargin + abandonGrace)
	defer timer.Stop()

	select {
	case res := <-done:
		if res.pan != nil {
			panic(res.pan)
		}

		if res.err == nil && res.out.Done && res.out.Result == nil {
			return res.out, errNoResult
		}

		return res.out, res.err
	case <-timer.C:
		return checkerdef.StepOutput{}, errAbandoned
	}
}
