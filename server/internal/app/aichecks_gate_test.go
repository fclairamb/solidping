package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/config"
)

// The script-authoring tools run js in the API process, which may construct no
// check worker, so buildAIChecks installs the sub-check deployment gate itself
// (spec 2026-10-16-09): on SaaS a script cannot run a docker sub-check there.
//
//nolint:paralleltest // mutates the package-level checkjs.DeploymentGate global
func TestBuildAIChecksInstallsTheDeploymentGate(t *testing.T) {
	previous := checkjs.DeploymentGate

	t.Cleanup(func() { checkjs.DeploymentGate = previous })

	for _, tc := range []struct {
		mode    string
		blocked bool
	}{
		{config.DeploymentModeSaaS, true},
		{config.DeploymentModeSelfHosted, false},
	} {
		checkjs.DeploymentGate = nil

		cfg := &config.Config{Deployment: config.DeploymentConfig{Mode: tc.mode}}
		buildAIChecks(cfg, nil, nil, nil, nil, services.NewRegistry())

		require.NotNil(t, checkjs.DeploymentGate, "mode %s", tc.mode)
		require.Equal(t, tc.blocked, checkjs.DeploymentGate(checkerdef.CheckTypeDocker) != "", "mode %s", tc.mode)
		require.Empty(t, checkjs.DeploymentGate(checkerdef.CheckTypeHTTP), "mode %s", tc.mode)
	}
}
