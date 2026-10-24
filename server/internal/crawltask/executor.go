package crawltask

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/secretref"
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
	allowEnv    secretref.EnvAllowFunc
	logger      *slog.Logger
}

var _ tasks.Executor = (*Executor)(nil)

// NewExecutor builds the executor over the checker registry. allowEnv is the
// process's `${env:}` allowlist (config.Config.EnvRefAllowed): a config's
// env references resolve against the environment of the worker running the
// slice, which for an agent is its own.
func NewExecutor(allowEnv secretref.EnvAllowFunc) *Executor {
	return &Executor{
		getChecker:  registry.GetChecker,
		parseConfig: registry.ParseConfig,
		allowEnv:    allowEnv,
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

	effective, displayURL, err := e.materialize(ctx, &input)
	if err != nil {
		// Never fetch a literal "${param:x}": the run ends on an error result
		// naming the reference, not its value.
		e.logger.WarnContext(ctx, "Crawl config reference unresolved", "task", claim.UID, "error", err)

		return finish(ctx, rep, &checkerdef.StepOutput{Done: true, Result: &checkerdef.Result{
			Status:  checkerdef.StatusError,
			Metrics: map[string]any{},
			Output:  map[string]any{checkerdef.OutputKeyError: err.Error()},
		}}, "")
	}

	if err = config.FromMap(effective); err != nil {
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

	return finish(ctx, rep, &out, displayURL)
}

// materialize returns the config the checker runs with: the stored config,
// the server's `${param:}` overlay over it, and `${env:}` and generated
// values resolved here. displayURL is the url as the result shows it,
// generated values in and secrets still as references, or "" when the url
// holds no secret reference. Anything left unresolved is an error.
func (e *Executor) materialize(ctx context.Context, input *Input) (map[string]any, string, error) {
	if len(input.ParamOverlay) == 0 && !secretref.ContainsInConfig(input.Config) {
		return input.Config, "", nil
	}

	vars := secretref.NewVarResolver(time.Now(), rand.Reader)

	displayURL := ""

	if rawURL, ok := input.Config[checkerdef.OutputKeyURL].(string); ok && secretref.ContainsSecretRef(rawURL) {
		display, _, err := secretref.ResolveString(ctx, rawURL, secretref.VarsOnlyResolver(vars))
		if err != nil {
			return nil, "", fmt.Errorf("%w (config %q)", err, checkerdef.OutputKeyURL)
		}

		displayURL = display
	}

	merged := make(map[string]any, len(input.Config))
	for key, value := range input.Config {
		merged[key] = value
	}

	for key, value := range input.ParamOverlay {
		merged[key] = value
	}

	resolved, _, err := secretref.ResolveConfig(ctx, merged, secretref.ExecutionResolverWith(vars, e.allowEnv))
	if err != nil {
		return nil, "", err
	}

	return resolved, displayURL, nil
}

// finish sends the run's result. A body over the request cap is sent again
// without its report: the result matters more than the full findings list.
// A displayURL replaces the resolved url the checker echoed in its output.
func finish(ctx context.Context, rep tasks.Reporter, out *checkerdef.StepOutput, displayURL string) error {
	body := Result{
		Status:     int(out.Result.Status),
		DurationMs: float64(out.Result.Duration.Milliseconds()),
		Metrics:    out.Result.Metrics,
		Output:     out.Result.Output,
		Units:      out.Units,
	}

	if _, echoed := body.Output[checkerdef.OutputKeyURL]; echoed && displayURL != "" {
		output := make(map[string]any, len(body.Output))
		for key, value := range body.Output {
			output[key] = value
		}

		output[checkerdef.OutputKeyURL] = displayURL
		body.Output = output
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
	ctx context.Context, checker checkerdef.StepChecker, config checkerdef.Config, input checkerdef.StepInput,
) (checkerdef.StepOutput, error) {
	stepCtx, cancel := context.WithTimeout(ctx, input.Budget+stepMargin)
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

		out, err := checker.Step(stepCtx, config, input)
		done <- outcome{out: out, err: err}
	}()

	timer := time.NewTimer(input.Budget + stepMargin + abandonGrace)
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
