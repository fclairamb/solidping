package checkjs

import (
	"context"
	"testing"
	"time"

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
