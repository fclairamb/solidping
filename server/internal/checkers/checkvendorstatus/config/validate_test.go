package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/config"
)

func validate(raw map[string]any) (*checkerdef.CheckSpec, error) {
	spec := &checkerdef.CheckSpec{Config: raw}

	return spec, config.ValidateSpec(spec)
}

func TestHTTPIsRejected(t *testing.T) {
	t.Parallel()

	_, err := validate(map[string]any{"url": "http://www.githubstatus.com"})
	require.Error(t, err)
}

func TestURLRequired(t *testing.T) {
	t.Parallel()

	_, err := validate(map[string]any{})
	require.Error(t, err)
}

func TestURLIsNormalized(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://www.githubstatus.com/",
		"https://www.githubstatus.com/api/v2/summary.json",
		"https://www.githubstatus.com/api/v2/components.json",
	} {
		spec, err := validate(map[string]any{"url": raw})
		require.NoError(t, err, raw)
		require.Equal(t, "https://www.githubstatus.com", spec.Config["url"], raw)
	}
}

func TestDefaultNameAndSlug(t *testing.T) {
	t.Parallel()

	spec, err := validate(map[string]any{"url": "https://www.githubstatus.com"})
	require.NoError(t, err)
	require.Equal(t, "www.githubstatus.com status", spec.Name)
	require.Equal(t, "vendor-www-githubstatus-com", spec.Slug)

	spec, err = validate(map[string]any{"url": "https://www.githubstatus.com", "provider": "github"})
	require.NoError(t, err)
	require.Equal(t, "GitHub status", spec.Name)
}

func TestComponentsValidation(t *testing.T) {
	t.Parallel()

	_, err := validate(map[string]any{"url": "https://x.acme.com", "components": []any{"a", ""}})
	require.Error(t, err)

	_, err = validate(map[string]any{"url": "https://x.acme.com", "components": "a"})
	require.Error(t, err)
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := &config.VendorStatusConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{
		"url": "https://x.acme.com", "components": []any{"a", "b"}, "provider": "acme",
	}))

	again := &config.VendorStatusConfig{}
	require.NoError(t, again.FromMap(cfg.GetConfig()))
	require.Equal(t, cfg, again)
}
