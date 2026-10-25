package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksleep"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/config"
)

// A disabled plugin type is still allowed when every region is a private
// location: the org's own agent runs it (spec 2026-10-17-05 §19).
func TestValidatePluginActivationAllowsPrivateLocations(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const checkType = checkerdef.CheckType("x-checks-gate")

	r.NoError(registry.RegisterPlugin(&checkerdef.CheckTypeMeta{
		Type: checkType, Labels: []string{"unsafe"}, Plugin: &checkerdef.PluginRef{Name: "gate", Version: "1"},
	},
		func() checkerdef.Checker { return &checksleep.SleepChecker{} },
		func() checkerdef.Config { return &checksleep.SleepConfig{} },
		func(*checkerdef.CheckSpec) error { return nil }))
	t.Cleanup(func() { registry.UnregisterPlugin(checkType) })

	svc := &Service{activation: checkerdef.NewActivationResolver(
		&config.CheckersConfig{}, config.DeploymentModeSaaS)}

	r.NoError(svc.validatePluginActivation(string(checkType), []string{"@office"}))
	r.NoError(svc.validatePluginActivation(string(checkType), []string{"@office", "@warehouse"}))
	r.Error(svc.validatePluginActivation(string(checkType), nil))
	r.Error(svc.validatePluginActivation(string(checkType), []string{"@office", "eu"}))
	r.NoError(svc.validatePluginActivation(string(checkerdef.CheckTypeHTTP), []string{"eu"}))

	r.NoError((&Service{}).validatePluginActivation(string(checkType), []string{"eu"}), "unwired: no gate")
}
