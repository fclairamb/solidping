package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksystemd/config"
)

func TestValidateUnitRejects(t *testing.T) {
	t.Parallel()

	for _, unit := range []string{
		"-H",
		"--host=x",
		"a b",
		"a;b",
		"a|b",
		"$(id)",
		"a/b",
		"",
		strings.Repeat("a", 300),
	} {
		spec := &checkerdef.CheckSpec{Config: map[string]any{"unit": unit}}
		err := config.ValidateSpec(spec)
		require.Error(t, err, "unit %q must be rejected", unit)

		configErr := checkerdef.IsConfigError(err)
		require.NotNil(t, configErr, "unit %q", unit)
		require.Equal(t, config.KeyUnit, configErr.Parameter, "unit %q", unit)
	}
}

func TestValidateUnitAccepts(t *testing.T) {
	t.Parallel()

	for _, unit := range []string{"nginx", "app@1.service", "foo.timer", "sys-kernel-debug.mount", "a:b_c.socket"} {
		spec := &checkerdef.CheckSpec{Config: map[string]any{"unit": unit}}
		require.NoError(t, config.ValidateSpec(spec), "unit %q", unit)
		require.True(t, strings.HasPrefix(spec.Slug, "systemd-"), "slug %q", spec.Slug)
	}
}

func TestValidateExpectedStateAndTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, state := range []string{"", "active", "inactive"} {
		r.NoError(config.ValidateSpec(&checkerdef.CheckSpec{Config: map[string]any{
			"unit": "nginx", "expected_state": state,
		}}), state)
	}

	err := config.ValidateSpec(&checkerdef.CheckSpec{Config: map[string]any{
		"unit": "nginx", "expected_state": "failed",
	}})
	r.Equal(config.KeyExpectedState, checkerdef.IsConfigError(err).Parameter)

	err = config.ValidateSpec(&checkerdef.CheckSpec{Config: map[string]any{"unit": "nginx", "timeout": "1m"}})
	r.Equal(config.KeyTimeout, checkerdef.IsConfigError(err).Parameter)

	err = config.ValidateSpec(&checkerdef.CheckSpec{Config: map[string]any{"unit": 3}})
	r.Equal(config.KeyUnit, checkerdef.IsConfigError(err).Parameter)
}

func TestUnitNameAndRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for in, want := range map[string]string{
		"nginx":         "nginx.service",
		"app@1.service": "app@1.service",
		"foo.timer":     "foo.timer",
		"my.app":        "my.app.service",
		"tmp.mount":     "tmp.mount",
	} {
		r.Equal(want, (&config.SystemdConfig{Unit: in}).UnitName(), in)
	}

	cfg := &config.SystemdConfig{}
	r.NoError(cfg.FromMap(map[string]any{"unit": "nginx", "expected_state": "inactive", "timeout": "3s"}))
	r.False(cfg.WantActive())
	r.Equal(map[string]any{"unit": "nginx", "expected_state": "inactive", "timeout": "3s"}, cfg.GetConfig())

	r.True((&config.SystemdConfig{Unit: "nginx"}).WantActive())
}
