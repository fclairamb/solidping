package checkjs

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func runStepScript(t *testing.T, script string) *checkerdef.Result {
	t.Helper()

	res, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{Script: script, Timeout: 5 * time.Second})
	require.NoError(t, err)

	return res
}

func stepsOf(t *testing.T, res *checkerdef.Result) []map[string]any {
	t.Helper()

	raw, ok := res.Output[OutputKeySteps].([]any)
	require.True(t, ok, "no steps in output: %v", res.Output)

	out := make([]map[string]any, 0, len(raw))

	for _, item := range raw {
		entry, isMap := item.(map[string]any)
		require.True(t, isMap)

		out = append(out, entry)
	}

	return out
}

func TestStepFailureReportsOkFailedSkipped(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `
		step("Open", function () { sleep(20); });
		try { step("Click Se connecter", function () { throw new Error("boom"); }); } catch (e) {}
		step("Check", function () { return 1; });
		return { status: "up" };`)

	steps := stepsOf(t, res)
	require.Len(t, steps, 3)
	assert.Equal(t, "ok", steps[0]["status"])
	assert.GreaterOrEqual(t, steps[0]["durationMs"], int64(15))
	assert.Equal(t, "failed", steps[1]["status"])
	assert.Equal(t, "boom", steps[1]["error"])
	assert.Equal(t, "skipped", steps[2]["status"])
}

func TestStepFailureSummaryNamesTheStep(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `
		step("Open", function () {});
		step("Click Se connecter", function () { throw new Error("no such element"); });
		step("Never", function () {});
		return { status: "up" };`)

	assert.Equal(t, checkerdef.StatusError, res.Status)
	assert.Equal(t, `step 2/2 "Click Se connecter" failed: no such element`, res.Output["error"])
	assert.Equal(t, "failed", stepsOf(t, res)[1]["status"])
}

func TestStepNestedIsAnError(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `
		step("outer", function () { step("inner", function () {}); });
		return { status: "up" };`)

	assert.Equal(t, checkerdef.StatusError, res.Status)
	assert.Contains(t, res.Output["error"], "cannot be nested")
}

func TestNoStepLeavesOutputUnchanged(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `return { status: "up", output: { a: 1 } };`)

	assert.Equal(t, checkerdef.StatusUp, res.Status)
	assert.NotContains(t, res.Output, OutputKeySteps)
}

//nolint:paralleltest // swaps the process-wide OpenBrowser seam
func TestStepScreenshotOnlyOnFailureByDefault(t *testing.T) {
	fake := &fakeSession{shot: []byte("img")}
	installFakeBrowser(t, fake)

	res := runStepScript(t, `
		var page = browser.open();
		step("fine", function () { page.goto("https://acme.dev"); });
		step("broken", function () { throw new Error("x"); });
		return { status: "up" };`)

	steps := stepsOf(t, res)
	assert.NotContains(t, steps[0], "screenshot")
	assert.Equal(t, true, steps[1]["screenshot"])
	assert.Equal(t, int32(1), fake.shots.Load())
	require.NotNil(t, res.Diagnostics)
	require.NotNil(t, res.Diagnostics.Screenshot)
}

//nolint:paralleltest // swaps the process-wide OpenBrowser seam
func TestStepScreenshotOptionIsCapped(t *testing.T) {
	fake := &fakeSession{shot: []byte("img")}
	installFakeBrowser(t, fake)

	res := runStepScript(t, `
		var page = browser.open();
		for (var i = 0; i < 8; i++) { step("s" + i, function () {}, { screenshot: true }); }
		return { status: "up" };`)

	assert.Equal(t, int32(maxStepScreenshots), fake.shots.Load())
	assert.Equal(t, checkerdef.StatusUp, res.Status)
}

var errRendererGone = errors.New("renderer gone")

func TestStepRequiresAFunction(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `step("no body", 42); return { status: "up" };`)

	assert.Equal(t, checkerdef.StatusError, res.Status)
	assert.Contains(t, res.Output["error"], "fn must be a function")
}

func TestStepFailureSurvivesTheStepCap(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `
		for (var i = 0; i < 100; i++) { step("s" + i, function () {}); }
		step("late failure", function () { throw new Error("too late for the list"); });
		return { status: "up" };`)

	steps := stepsOf(t, res)
	require.Len(t, steps, maxSteps)
	assert.Equal(t, "late failure", steps[maxSteps-1]["name"], "the failure replaces the last recorded step")
	assert.Equal(t, "failed", steps[maxSteps-1]["status"])
	assert.Equal(t, checkerdef.StatusError, res.Status)
}

func TestStepInterruptedByTheRunDeadline(t *testing.T) {
	t.Parallel()

	res, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
		Script:  `step("spin", function () { while (true) {} }); return { status: "up" };`,
		Timeout: 300 * time.Millisecond,
	})
	require.NoError(t, err)

	assert.NotEqual(t, checkerdef.StatusUp, res.Status)
	assert.Equal(t, "failed", stepsOf(t, res)[0]["status"])
}

func TestStepErrorTextForNonErrorThrows(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `step("plain", function () { throw "just a string"; }); return { status: "up" };`)

	assert.Equal(t, "just a string", stepsOf(t, res)[0]["error"])
}

func TestStepStringsAreTruncatedOnRuneBoundaries(t *testing.T) {
	t.Parallel()

	res := runStepScript(t, `
		var name = "a" + new Array(301).join("é");
		step(name, function () { throw new Error(new Array(1500).join("é")); });
		return { status: "up" };`)

	step := stepsOf(t, res)[0]
	name, _ := step["name"].(string)
	message, _ := step["error"].(string)

	assert.LessOrEqual(t, len(name), maxStepNameLen)
	assert.LessOrEqual(t, len(message), maxStepErrorLen)
	assert.True(t, utf8.ValidString(name), "a rune is never cut in half")
	assert.True(t, utf8.ValidString(message), "a rune is never cut in half")
}

//nolint:paralleltest // swaps the process-wide OpenBrowser seam
func TestStepCaptureFailureIsReportedNotRaised(t *testing.T) {
	fake := &fakeSession{shotErr: errRendererGone}
	installFakeBrowser(t, fake)

	res := runStepScript(t, `
		var page = browser.open();
		step("broken", function () { throw new Error("x"); });
		return { status: "up" };`)

	steps := stepsOf(t, res)
	assert.NotContains(t, steps[0], "screenshot", "no capture was kept")
	assert.Equal(t, "failed", steps[0]["status"])
	require.NotNil(t, res.Diagnostics)
	assert.Nil(t, res.Diagnostics.Screenshot)
	assert.NotEmpty(t, res.Diagnostics.ScreenshotError, "the capture failure is reported")
}

// The capture hook is a global a script can overwrite: a broken one must cost
// the step its screenshot, never the run.
func TestStepCaptureHookTamperedWith(t *testing.T) {
	t.Parallel()

	for name, hook := range map[string]string{
		"not callable": `__stepshot = 1;`,
		"throws":       `__stepshot = function () { throw new Error("nope"); };`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res := runStepScript(t, hook+`
				step("fine", function () {}, { screenshot: true });
				step("broken", function () { throw new Error("x"); });
				return { status: "up" };`)

			steps := stepsOf(t, res)
			assert.NotContains(t, steps[0], "screenshot")
			assert.NotContains(t, steps[1], "screenshot")
			assert.Equal(t, "failed", steps[1]["status"])
		})
	}
}
