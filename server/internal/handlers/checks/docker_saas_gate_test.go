package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
)

// TestValidateDockerDeploymentConfig is the pure-function half of spec
// 2026-09-25-22: it exercises validateHostLocalDeploymentConfig directly (no DB,
// no placement resolution) across every combination of deployment mode, check
// type and resolved region shape.
func TestValidateDockerDeploymentConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	saas := &Service{deploymentMode: config.DeploymentModeSaaS}
	selfHosted := &Service{deploymentMode: config.DeploymentModeSelfHosted}
	unset := &Service{}

	// Non-docker types are never gated, whatever the mode or regions.
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeHTTP), nil, nil))
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeHTTP), nil, []string{"eu-west"}))

	// Self-hosted (and the unset zero value, which behaves like self-hosted)
	// never gates docker — running against the local daemon is the feature.
	r.NoError(selfHosted.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, nil))
	r.NoError(selfHosted.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, []string{"eu-west"}))
	r.NoError(unset.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, []string{"eu-west"}))

	// SaaS + no regions (auto placement never resolves onto a private region,
	// so an empty/shared resolved list must always be rejected).
	err := saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, nil)
	assertDockerRejected(r, err)

	// SaaS + a shared region.
	err = saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, []string{"eu-west"})
	assertDockerRejected(r, err)

	// SaaS + a mix of private and shared: still rejected — EVERY resolved
	// region must be private, not just one of them.
	err = saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, []string{"@office", "eu-west"})
	assertDockerRejected(r, err)

	// SaaS + private-only regions: allowed. This is the "an agent in the
	// customer's own network" path the spec carves out.
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, []string{"@office"}))
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil, []string{"@office", "@warehouse"}))
}

// assertDockerRejected asserts the gate's error shape: a *checkerdef.ConfigError
// (so the handler maps it to 400 VALIDATION_ERROR, same as every other config
// rule) carrying ErrDockerNotAvailableInSaaS's exact message.
func assertDockerRejected(r *require.Assertions, err error) {
	r.Error(err)

	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "docker-in-SaaS rejection must be a *checkerdef.ConfigError")
	r.Equal(ErrDockerNotAvailableInSaaS.Error(), configErr.Message)
}
