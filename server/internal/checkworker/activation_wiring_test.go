package checkworker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/config"
)

// resetJSGate clears the package-level gate before the test and restores the
// previous value afterwards, so "was it installed?" is a real question rather
// than a leftover from another test.
func resetJSGate(t *testing.T) {
	t.Helper()

	previous := checkjs.TypeEnabled

	t.Cleanup(func() { checkjs.TypeEnabled = previous })

	checkjs.TypeEnabled = nil
}

// TestAgentCheckWorkerInstallsJSActivationGate is the wiring test the spec asks
// for: it goes through the REAL construction path used by a standalone agent
// (NewAgentCheckWorker → newCheckWorker) rather than poking checkjs.TypeEnabled
// directly. A test that only sets the global proves nothing about production —
// which is exactly how the bypass survived: the only ActivationResolver in the
// tree was built in HTTP route setup, code an agent process never runs.
//
//nolint:paralleltest // mutates the package-level checkjs.TypeEnabled global
func TestAgentCheckWorkerInstallsJSActivationGate(t *testing.T) {
	r := require.New(t)

	resetJSGate(t)

	cfg := &config.Config{}
	cfg.Checkers.Disabled = []string{string(checkerdef.CheckTypeBrowser)}

	worker := NewAgentCheckWorker(cfg, nil)
	r.NotNil(worker)

	r.NotNil(checkjs.TypeEnabled, "agent construction must install the JS sub-check activation gate")
	r.False(checkjs.TypeEnabled(checkerdef.CheckTypeBrowser), "disabled type must be refused")
	r.True(checkjs.TypeEnabled(checkerdef.CheckTypeHTTP), "untouched type must stay enabled")
}

// TestCheckWorkerInstallsJSActivationGate covers the other public constructor,
// the in-process (embedded) worker, so both callers of newCheckWorker are
// represented. Here the config uses the explicit allowlist form, which is a
// different branch of resolveServerEnabled than the blocklist above.
//
//nolint:paralleltest // mutates the package-level checkjs.TypeEnabled global
func TestCheckWorkerInstallsJSActivationGate(t *testing.T) {
	r := require.New(t)

	resetJSGate(t)

	cfg := &config.Config{}
	cfg.Checkers.Enabled = []string{string(checkerdef.CheckTypeHTTP)}

	worker := NewCheckWorker(nil, cfg, services.NewRegistry(), nil)
	r.NotNil(worker)

	r.NotNil(checkjs.TypeEnabled, "in-process construction must install the JS sub-check activation gate")
	r.True(checkjs.TypeEnabled(checkerdef.CheckTypeHTTP), "allowlisted type must be enabled")
	r.False(checkjs.TypeEnabled(checkerdef.CheckTypeICMP), "type outside the allowlist must be refused")
	r.False(checkjs.TypeEnabled(checkerdef.CheckTypeBrowser))
}

// TestCheckWorkerJSGateDefaultsToEverythingEnabled pins the zero-config
// behavior: an operator (or an agent) that configured nothing keeps every
// compiled-in type available, so installing the gate is not itself a
// behavior change. It is also the limitation recorded in the plan — an agent
// left at defaults enables everything.
//
//nolint:paralleltest // mutates the package-level checkjs.TypeEnabled global
func TestCheckWorkerJSGateDefaultsToEverythingEnabled(t *testing.T) {
	r := require.New(t)

	resetJSGate(t)

	worker := NewAgentCheckWorker(&config.Config{}, nil)
	r.NotNil(worker)

	r.NotNil(checkjs.TypeEnabled)

	for _, meta := range checkerdef.ListCheckTypeMetas() {
		r.True(checkjs.TypeEnabled(meta.Type), "default config must enable %s", meta.Type)
	}
}
