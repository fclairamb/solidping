package checkjs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Step statuses reported in the result output's "steps" list.
const (
	StepStatusOK      = "ok"
	StepStatusFailed  = "failed"
	StepStatusSkipped = "skipped"
)

// OutputKeySteps is the result output key holding the named steps of a script
// that used step(): a list of {name, status, durationMs, error?, screenshot?}.
const OutputKeySteps = "steps"

// stepKeyName is the key of a step's name in the output.
const stepKeyName = "name"

const (
	// maxSteps bounds the recorded steps of one run (the output is stored).
	maxSteps = 100
	// maxStepNameLen and maxStepErrorLen bound a step's strings.
	maxStepNameLen  = 200
	maxStepErrorLen = 1024
	// maxStepScreenshots bounds the optional `{screenshot: true}` captures of
	// a run. A failing step's capture is not counted: there is at most one.
	maxStepScreenshots = 5

	// stepShotGlobal is the host-side global the step() wrapper calls to take
	// a capture: the browser lives in the host process, the step() wrapper may
	// run in the isolated child.
	stepShotGlobal = "__stepshot"
)

var errNestedStep = errors.New("step() cannot be nested: call it at the top level of the script")

// stepRecord is one entry of the steps list.
type stepRecord struct {
	name       string
	status     string
	durationMs int64
	err        string
	screenshot bool
}

// registerStep exposes the `step(name, fn[, opts])` global.
func (r *jsRuntime) registerStep() {
	_ = r.vm.Set("step", func(call goja.FunctionCall) goja.Value {
		return r.runStep(call)
	})
}

func (r *jsRuntime) runStep(call goja.FunctionCall) goja.Value {
	if r.inStep {
		panic(r.vm.NewGoError(errNestedStep))
	}

	name := truncateRunes(call.Argument(0).String(), maxStepNameLen)

	body, ok := goja.AssertFunction(call.Argument(1))
	if !ok {
		panic(r.vm.NewTypeError("step(name, fn): fn must be a function"))
	}

	wantShot := false

	if opts, isMap := call.Argument(2).Export().(map[string]any); isMap {
		wantShot, _ = opts["screenshot"].(bool)
	}

	if r.stepFailed {
		r.addStep(stepRecord{name: name, status: StepStatusSkipped})

		return goja.Undefined()
	}

	r.inStep = true
	start := time.Now()
	val, err := body(goja.Undefined())
	r.inStep = false

	rec := stepRecord{name: name, status: StepStatusOK, durationMs: time.Since(start).Milliseconds()}

	if err == nil {
		rec.screenshot = wantShot && r.stepCapture(false)
		r.addStep(rec)

		return val
	}

	rec.status = StepStatusFailed
	rec.err = truncateRunes(stepErrorText(err), maxStepErrorLen)
	rec.screenshot = r.stepCapture(true)
	r.stepFailed = true
	r.addStep(rec)

	// A failure is never lost to the cap: it replaces the last entry.
	if len(r.steps) == maxSteps && r.steps[maxSteps-1] != rec {
		r.steps[maxSteps-1] = rec
	}

	r.failedStep = len(r.steps)

	var exc *goja.Exception
	if errors.As(err, &exc) {
		panic(exc.Value())
	}

	// An interrupt (the run's deadline): keep it pending for the VM.
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		r.vm.Interrupt(interrupted.Value())

		return goja.Undefined()
	}

	panic(r.vm.NewGoError(err))
}

// addStep records a step, within the cap.
func (r *jsRuntime) addStep(rec stepRecord) {
	if len(r.steps) < maxSteps {
		r.steps = append(r.steps, rec)
	}
}

// stepCapture asks the host for a capture of the page; false when there is no
// open page, the cap is reached (unless forced) or the capture failed.
func (r *jsRuntime) stepCapture(force bool) bool {
	fn, ok := goja.AssertFunction(r.vm.Get(stepShotGlobal))
	if !ok {
		return false
	}

	val, err := fn(goja.Undefined(), r.vm.ToValue(force))
	if err != nil || val == nil {
		return false
	}

	return val.ToBoolean()
}

// registerStepShot exposes the host-side capture function step() calls.
func (r *jsRuntime) registerStepShot() {
	_ = r.vm.Set(stepShotGlobal, func(call goja.FunctionCall) goja.Value {
		force := call.Argument(0).ToBoolean()

		return r.vm.ToValue(r.captureForStep(force))
	})
}

// captureForStep takes a time-boxed capture of the open page and records it
// like a page.screenshot() (last capture wins).
func (r *jsRuntime) captureForStep(force bool) bool {
	if r.browser == nil || r.execCtx == nil {
		return false
	}

	if !force && r.stepShots >= maxStepScreenshots {
		return false
	}

	ctx, cancel := context.WithTimeout(r.execCtx, config.ScreenshotTimeout)
	defer cancel()

	shot, err := r.browser.Screenshot(ctx)
	if err != nil {
		r.screenshotErr = checkbrowser.CaptureErrorMessage(err, 0)

		return false
	}

	if !force {
		r.stepShots++
	}

	r.recordScreenshot(shot)

	return r.screenshotErr == ""
}

// stepErrorText is the message of a step's failure, without the engine's
// exception decoration when there is a plain message.
func stepErrorText(err error) string {
	var exc *goja.Exception
	if errors.As(err, &exc) {
		if obj, ok := exc.Value().(*goja.Object); ok {
			if msg := obj.Get("message"); msg != nil && !goja.IsUndefined(msg) {
				return msg.String()
			}
		}

		return exc.Value().String()
	}

	return err.Error()
}

func truncateRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	return text[:cut]
}

// addSteps adds the steps list to a result output, when the script used step().
func (r *jsRuntime) addSteps(out map[string]any) {
	if len(r.steps) == 0 {
		return
	}

	list := make([]any, 0, len(r.steps))

	for index := range r.steps {
		rec := &r.steps[index]
		entry := map[string]any{
			stepKeyName:  rec.name,
			"status":     rec.status,
			"durationMs": rec.durationMs,
		}

		if rec.err != "" {
			entry["error"] = rec.err
		}

		if rec.screenshot {
			entry["screenshot"] = true
		}

		list = append(list, entry)
	}

	out[OutputKeySteps] = list
}

// stepFailureMessage is the summary of a run a step failed: `step 4/7 "Click"
// failed: …`. Empty when no step failed.
func (r *jsRuntime) stepFailureMessage() string {
	if !r.stepFailed || r.failedStep < 1 || r.failedStep > len(r.steps) {
		return ""
	}

	rec := r.steps[r.failedStep-1]

	return fmt.Sprintf("step %d/%d %q failed: %s", r.failedStep, len(r.steps), rec.name, strings.TrimSpace(rec.err))
}

// hasFailedStep reports a result whose steps list holds a failed step: the
// capture taken at that step is kept even though the status is `error` (a
// step threw), like any other failed verdict.
func hasFailedStep(result *checkerdef.Result) bool {
	list, _ := result.Output[OutputKeySteps].([]any)

	for _, item := range list {
		if entry, ok := item.(map[string]any); ok && entry["status"] == StepStatusFailed {
			return true
		}
	}

	return false
}
