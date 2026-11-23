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
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeHTTP), nil))
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeHTTP), []string{"eu-west"}))

	// Self-hosted (and the unset zero value, which behaves like self-hosted)
	// never gates docker — running against the local daemon is the feature.
	r.NoError(selfHosted.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil))
	r.NoError(selfHosted.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), []string{"eu-west"}))
	r.NoError(unset.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), []string{"eu-west"}))

	// SaaS + no regions (auto placement never resolves onto a private region,
	// so an empty/shared resolved list must always be rejected).
	err := saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), nil)
	assertDockerRejected(r, err)

	// SaaS + a shared region.
	err = saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), []string{"eu-west"})
	assertDockerRejected(r, err)

	// SaaS + a mix of private and shared: still rejected — EVERY resolved
	// region must be private, not just one of them.
	err = saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), []string{"@office", "eu-west"})
	assertDockerRejected(r, err)

	// SaaS + private-only regions: allowed. This is the "an agent in the
	// customer's own network" path the spec carves out.
	r.NoError(saas.validateHostLocalDeploymentConfig(string(checkerdef.CheckTypeDocker), []string{"@office"}))
	r.NoError(saas.validateHostLocalDeploymentConfig(
		string(checkerdef.CheckTypeDocker), []string{"@office", "@warehouse"},
	))
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

// A unix_socket check reads a socket file of the executing host, exactly like
// docker reads its daemon socket (issue #1000): the same SaaS rule applies,
// with its own message, on its `path` field.
func TestValidateHostLocalDeploymentConfig_UnixSocket(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	saas := &Service{deploymentMode: config.DeploymentModeSaaS}
	selfHosted := &Service{deploymentMode: config.DeploymentModeSelfHosted}
	unixSocket := string(checkerdef.CheckTypeUnixSocket)

	r.NoError(selfHosted.validateHostLocalDeploymentConfig(unixSocket, nil))
	r.NoError(selfHosted.validateHostLocalDeploymentConfig(unixSocket, []string{"eu-west"}))
	r.NoError(saas.validateHostLocalDeploymentConfig(unixSocket, []string{"@office"}))

	for _, placement := range [][]string{nil, {"eu-west"}, {"@office", "eu-west"}} {
		err := saas.validateHostLocalDeploymentConfig(unixSocket, placement)
		configErr := checkerdef.IsConfigError(err)
		r.NotNil(configErr, "placement %v must be rejected", placement)
		r.Equal("path", configErr.Parameter)
		r.Equal(ErrUnixSocketNotAvailableInSaaS.Error(), configErr.Message)
	}
}

// A systemd check reads a unit of the executing host (issue #1000): the same
// SaaS rule applies, with its own message, on its `unit` field.
func TestValidateHostLocalDeploymentConfig_Systemd(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	saas := &Service{deploymentMode: config.DeploymentModeSaaS}
	selfHosted := &Service{deploymentMode: config.DeploymentModeSelfHosted}
	systemd := string(checkerdef.CheckTypeSystemd)

	r.NoError(selfHosted.validateHostLocalDeploymentConfig(systemd, nil))
	r.NoError(selfHosted.validateHostLocalDeploymentConfig(systemd, []string{"eu-west"}))
	r.NoError(saas.validateHostLocalDeploymentConfig(systemd, []string{"@office"}))

	for _, placement := range [][]string{nil, {"eu-west"}, {"@office", "eu-west"}} {
		err := saas.validateHostLocalDeploymentConfig(systemd, placement)
		configErr := checkerdef.IsConfigError(err)
		r.NotNil(configErr, "placement %v must be rejected", placement)
		r.Equal("unit", configErr.Parameter)
		r.Equal(ErrSystemdNotAvailableInSaaS.Error(), configErr.Message)
	}
}
