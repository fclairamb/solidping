package publicconfig_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/publicconfig"
)

// TestPublicConfigAdvertisesTheDemo covers §7's public-config shape: the login
// page's one-click entry reads it, so a wrong shape here is a demo nobody can
// find.
//
// The password IS in the document, on purpose — see DemoPublicConfig's doc for
// why serving it beats hardcoding it in the bundle.
func TestPublicConfigAdvertisesTheDemo(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Demo = config.DemoConfig{
		Enabled:  true,
		OrgSlug:  "demo",
		Email:    "demo@solidping.io",
		Password: "demo",
	}

	resp := publicconfig.Build(cfg)

	require.True(t, resp.Demo.Enabled)
	require.Equal(t, "demo", resp.Demo.OrgSlug)
	require.Equal(t, "demo@solidping.io", resp.Demo.Email)
	require.Equal(t, "demo", resp.Demo.Password)
}

// TestPublicConfigFallsBackToTheDefaults proves an operator who flips only
// SP_DEMO_ENABLED still gets a usable document — the login button needs a
// triple, and three empty strings would render a button that logs nobody in.
func TestPublicConfigFallsBackToTheDefaults(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Demo = config.DemoConfig{Enabled: true}

	resp := publicconfig.Build(cfg)

	require.True(t, resp.Demo.Enabled)
	require.Equal(t, config.DefaultDemoOrgSlug, resp.Demo.OrgSlug)
	require.Equal(t, config.DefaultDemoEmail, resp.Demo.Email)
	require.Equal(t, config.DefaultDemoPassword, resp.Demo.Password)
}

// TestPublicConfigEmitsNothingWhenTheDemoIsOff is the self-hosted case: an
// install that never turns the demo on must not advertise a credential, not
// even an unusable one.
func TestPublicConfigEmitsNothingWhenTheDemoIsOff(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Demo = config.DemoConfig{
		Enabled:  false,
		OrgSlug:  "demo",
		Email:    "demo@solidping.io",
		Password: "hunter2",
	}

	resp := publicconfig.Build(cfg)

	require.False(t, resp.Demo.Enabled)
	require.Empty(t, resp.Demo.OrgSlug)
	require.Empty(t, resp.Demo.Email)
	require.Empty(t, resp.Demo.Password,
		"a disabled demo must not leak its configured password")
}
