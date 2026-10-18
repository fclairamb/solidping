package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// A plugin type the activation gate disables (an unsafe type on a SaaS
// deployment) cannot be created or updated on a cloud region (spec
// 2026-10-17-05 §19).
func TestDisabledPluginTypeIsRefusedOnCloudRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-checks-unsafe"

	loadTestPluginLabelled(t, checkType, []string{"unsafe"})

	env := newWarnEnv(t)
	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})

	// Positive control: self-hosted enables the type.
	env.svc.SetActivationResolver(checkerdef.NewActivationResolver(
		&config.CheckersConfig{}, config.DeploymentModeSelfHosted))

	_, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name: "Allowed", Slug: "allowed", Type: checkType,
		Config: map[string]any{"host": "gopher.acme.com"}, Regions: []string{"eu"},
	})
	r.NoError(err)

	env.svc.SetActivationResolver(checkerdef.NewActivationResolver(
		&config.CheckersConfig{}, config.DeploymentModeSaaS))

	_, err = env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name: "Refused", Slug: "refused", Type: checkType,
		Config: map[string]any{"host": "gopher.acme.com"}, Regions: []string{"eu"},
	})
	assertPluginDisabled(r, err)

	_, err = env.dbSvc.GetCheckByUidOrSlug(ctx, env.org.UID, "refused")
	r.Error(err, "a refused check must not be stored")

	// Updating the check made before the gate applied is refused too.
	_, err = env.svc.UpdateCheck(ctx, env.org.Slug, "allowed", &checks.UpdateCheckRequest{
		Config: &map[string]any{"host": "other.acme.com"},
	})
	assertPluginDisabled(r, err)
}

func assertPluginDisabled(r *require.Assertions, err error) {
	r.Error(err)

	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "got %v", err)
	r.Equal("type", configErr.Parameter)
	r.Equal(checks.ErrPluginTypeDisabled.Error(), configErr.Message)
}
