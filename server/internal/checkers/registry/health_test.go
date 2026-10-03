package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

func TestHealthIsRegistered(t *testing.T) {
	t.Parallel()

	checker, ok := GetChecker(checkerdef.CheckTypeHealth)
	require.True(t, ok)
	require.Equal(t, checkerdef.CheckTypeHealth, checker.Type())

	_, hasSamples := checker.(checkerdef.CheckerSamplesProvider)
	require.True(t, hasSamples)

	cfg, ok := ParseConfig(checkerdef.CheckTypeHealth)
	require.True(t, ok)
	require.NoError(t, cfg.FromMap(map[string]any{"url": "https://app.acme.com/health", "format": "aspnet"}))
	require.True(t, configregistry.IsKnownType(checkerdef.CheckTypeHealth))

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeHealth)
	require.NotNil(t, meta)
	require.Equal(t, time.Minute, meta.DefaultPeriod)
	require.True(t, meta.SupportsTunnel)
	require.True(t, meta.SupportsIPVersion)
	require.Contains(t, checkerdef.ListCheckTypes(), checkerdef.CheckTypeHealth)

	require.ErrorContains(t,
		configregistry.ValidateSpec(checkerdef.CheckTypeHealth, &checkerdef.CheckSpec{
			Config: map[string]any{"url": "https://app.acme.com/health", "body_expect": "ok"},
		}), "body_expect")
}
