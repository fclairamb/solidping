package checkjs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// stubConfig is a no-op checkerdef.Config: the gate tests care about whether
// Execute is reached, not about parsing.
type stubConfig struct{}

func (c *stubConfig) FromMap(_ map[string]any) error { return nil }

func (c *stubConfig) GetConfig() map[string]any { return map[string]any{} }

// stubChecker records every Execute call so a test can assert the sub-checker
// was NEVER entered. Asserting only on the returned error string would pass
// even if the check had really run and failed afterwards — which is the whole
// point of the negative test.
type stubChecker struct {
	checkType checkerdef.CheckType
	calls     atomic.Int32
}

func (c *stubChecker) Type() checkerdef.CheckType { return c.checkType }

func (c *stubChecker) Validate(_ *checkerdef.CheckSpec) error { return nil }

func (c *stubChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	c.calls.Add(1)

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Millisecond,
		Output:   map[string]any{"stub": true},
	}, nil
}

// installStubResolver points the package-level ResolveChecker at a set of stub
// checkers and restores the previous value when the test ends.
func installStubResolver(t *testing.T, stubs map[checkerdef.CheckType]*stubChecker) {
	t.Helper()

	previous := ResolveChecker

	t.Cleanup(func() { ResolveChecker = previous })

	ResolveChecker = func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool) {
		stub, ok := stubs[checkType]
		if !ok {
			return nil, nil, false
		}

		return stub, &stubConfig{}, true
	}
}

// installGate installs a server-level activation gate that disables exactly the
// given types, restoring the previous gate when the test ends.
func installGate(t *testing.T, disabled ...checkerdef.CheckType) {
	t.Helper()

	previous := TypeEnabled

	t.Cleanup(func() { TypeEnabled = previous })

	disabledSet := make(map[checkerdef.CheckType]bool, len(disabled))
	for _, checkType := range disabled {
		disabledSet[checkType] = true
	}

	TypeEnabled = func(checkType checkerdef.CheckType) bool {
		return !disabledSet[checkType]
	}
}

// clearGate pins TypeEnabled to nil (the "no gate configured" default) for the
// duration of a test.
func clearGate(t *testing.T) {
	t.Helper()

	previous := TypeEnabled

	t.Cleanup(func() { TypeEnabled = previous })

	TypeEnabled = nil
}

// runScript executes a JS check with the given script and returns its result.
func runScript(t *testing.T, script string) *checkerdef.Result {
	t.Helper()

	checker := &JSChecker{}

	result, err := checker.Execute(context.Background(), &JSConfig{
		Script:  script,
		Timeout: 10 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// subCheckError digs the sub-check error message out of the result the script
// returned verbatim.
func subCheckError(t *testing.T, result *checkerdef.Result) string {
	t.Helper()

	// buildOutputFromObj merges the script's `output` field into the top-level
	// result output, so the sub-check result map lands under "output".
	nested, ok := result.Output[jsKeyOutput].(map[string]any)
	require.True(t, ok, "sub-check output is not a map: %#v", result.Output)

	msg, ok := nested[checkerdef.OutputKeyError].(string)
	require.True(t, ok, "sub-check output has no error string: %#v", nested)

	return msg
}

// subCheckStatus reads the status of the sub-check result the script returned.
func subCheckStatus(t *testing.T, result *checkerdef.Result) string {
	t.Helper()

	status, ok := result.Output[jsKeyStatus].(string)
	require.True(t, ok, "sub-check result has no status: %#v", result.Output)

	return status
}

// The gate and the resolver are package-level globals (the import-cycle break),
// so these tests must not run concurrently with one another.

// TestSubCheckGateRefusesDisabledType is the core negative test: a script
// asking for a server-disabled type gets an error result that names the type
// and says it is DISABLED, and the sub-checker is never entered.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckGateRefusesDisabledType(t *testing.T) {
	r := require.New(t)

	browser := &stubChecker{checkType: checkerdef.CheckTypeBrowser}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeBrowser: browser,
	})
	installGate(t, checkerdef.CheckTypeBrowser)

	result := runScript(t, `
		var res = solidping.check("browser", { url: "https://acme.com" });
		return { status: "up", output: res };
	`)

	r.Equal(logLevelError, subCheckStatus(t, result))

	msg := subCheckError(t, result)
	r.Contains(msg, "browser")
	r.Contains(msg, "disabled")
	r.NotContains(msg, "unknown")

	// The single most important assertion: the disabled checker never ran.
	r.Zero(browser.calls.Load(), "disabled sub-checker must never be executed")
}

// TestSubCheckGateAllowsEnabledType is the positive control: with the very same
// gate installed, an enabled type still resolves and executes normally.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckGateAllowsEnabledType(t *testing.T) {
	r := require.New(t)

	browser := &stubChecker{checkType: checkerdef.CheckTypeBrowser}
	httpStub := &stubChecker{checkType: checkerdef.CheckTypeHTTP}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeBrowser: browser,
		checkerdef.CheckTypeHTTP:    httpStub,
	})
	installGate(t, checkerdef.CheckTypeBrowser)

	result := runScript(t, `
		var res = solidping.check("http", { url: "https://acme.com" });
		return { status: "up", output: res };
	`)

	r.Equal(checkerdef.StatusUp.String(), subCheckStatus(t, result))
	r.EqualValues(1, httpStub.calls.Load(), "enabled sub-checker must still execute")
	r.Zero(browser.calls.Load())
}

// TestSubCheckGateTypedWrapperRefusesDisabledType pins that the fifteen typed
// wrappers (solidping.http(), solidping.icmp(), …) go through the same gate —
// they are convenience aliases over check(), not a second entry point.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckGateTypedWrapperRefusesDisabledType(t *testing.T) {
	r := require.New(t)

	icmp := &stubChecker{checkType: checkerdef.CheckTypeICMP}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeICMP: icmp,
	})
	installGate(t, checkerdef.CheckTypeICMP)

	result := runScript(t, `
		var res = solidping.icmp({ host: "example.invalid" });
		return { status: "up", output: res };
	`)

	r.Contains(subCheckError(t, result), "disabled")
	r.Zero(icmp.calls.Load(), "disabled sub-checker must never be executed")
}

// TestSubCheckNilGateAllowsEverything is the nil-gate control: with no gate
// configured the package behaves exactly as before, so it stays usable
// standalone and in unit tests.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckNilGateAllowsEverything(t *testing.T) {
	r := require.New(t)

	browser := &stubChecker{checkType: checkerdef.CheckTypeBrowser}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeBrowser: browser,
	})
	clearGate(t)

	result := runScript(t, `
		var res = solidping.check("browser", { url: "https://acme.com" });
		return { status: "up", output: res };
	`)

	r.Equal(checkerdef.StatusUp.String(), subCheckStatus(t, result))
	r.EqualValues(1, browser.calls.Load(), "with no gate every type must resolve as before")
}

// TestSubCheckUnknownTypeStaysUnknown pins that the refusal reason is
// distinguishable from "unknown check type": a type absent from the registry
// must still report unknown, not disabled.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckUnknownTypeStaysUnknown(t *testing.T) {
	r := require.New(t)

	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{})
	installGate(t) // nothing disabled

	result := runScript(t, `
		var res = solidping.check("not-a-real-type", {});
		return { status: "up", output: res };
	`)

	msg := subCheckError(t, result)
	r.Contains(msg, "unknown check type")
	r.NotContains(msg, "disabled")
}

// TestSubCheckSandboxBlockSurvivesGate pins that the pre-existing js/heartbeat
// sandbox rule is untouched: those two stay refused even when the gate enables
// everything.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckSandboxBlockSurvivesGate(t *testing.T) {
	r := require.New(t)

	jsStub := &stubChecker{checkType: checkerdef.CheckTypeJS}
	heartbeat := &stubChecker{checkType: checkerdef.CheckTypeHeartbeat}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeJS:        jsStub,
		checkerdef.CheckTypeHeartbeat: heartbeat,
	})
	installGate(t) // everything enabled

	for _, typeName := range []string{"js", "heartbeat"} {
		result := runScript(t, `
			var res = solidping.check("`+typeName+`", {});
			return { status: "up", output: res };
		`)

		r.Contains(subCheckError(t, result), "not allowed in JS scripts")
	}

	r.Zero(jsStub.calls.Load())
	r.Zero(heartbeat.calls.Load())
}

// TestSubCheckGateCountsAgainstBudget pins decision D4: a refused sub-check
// still consumes one unit of the maxSubChecks budget, because the gate runs
// after the counter. If that ordering is ever reshuffled this test fails and
// the decision gets re-made deliberately rather than by accident.
//
//nolint:paralleltest // mutates the package-level TypeEnabled/ResolveChecker globals
func TestSubCheckGateCountsAgainstBudget(t *testing.T) {
	r := require.New(t)

	browser := &stubChecker{checkType: checkerdef.CheckTypeBrowser}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeBrowser: browser,
	})
	installGate(t, checkerdef.CheckTypeBrowser)

	// maxSubChecks refusals exhaust the budget; the next call reports the
	// limit rather than the refusal.
	result := runScript(t, `
		var last = null;
		for (var i = 0; i < `+"21"+`; i++) {
			last = solidping.check("browser", {});
		}
		return { status: "up", output: last };
	`)

	r.Contains(subCheckError(t, result), "sub-check limit")
	r.Zero(browser.calls.Load())
}
