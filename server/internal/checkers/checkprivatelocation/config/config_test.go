package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation/config"
)

func TestValidateSpec(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Positive control: an org-relative private region validates and names the
	// monitor after the location.
	spec := &checkerdef.CheckSpec{Config: map[string]any{"region": "@office"}}
	r.NoError(config.ValidateSpec(spec))
	r.Equal("Private location: office", spec.Name)
	r.Equal("private-location-office", spec.Slug)
	r.Equal("@office", spec.Config["region"])

	// An explicit name and slug are kept.
	named := &checkerdef.CheckSpec{Name: "Office", Slug: "office-agents", Config: map[string]any{"region": "@office"}}
	r.NoError(config.ValidateSpec(named))
	r.Equal("Office", named.Name)
	r.Equal("office-agents", named.Slug)

	for _, bad := range []map[string]any{
		nil,
		{},
		{"region": ""},
		{"region": "eu-west"},
		{"region": "@"},
		{"region": "@acme/office"},
		{"region": "@Office"},
		{"region": 42},
	} {
		err := config.ValidateSpec(&checkerdef.CheckSpec{Config: bad})
		r.Error(err, "config %v", bad)
		r.NotNil(checkerdef.IsConfigError(err), "config %v", bad)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.PrivateLocationConfig{}
	r.NoError(cfg.FromMap(map[string]any{"region": "@office"}))
	r.Equal("@office", cfg.Region)
	r.Equal(map[string]any{"region": "@office"}, cfg.GetConfig())
	r.Empty(cfg.SecretFields())

	r.Equal("office", config.RegionSlug("@office"))
	r.Empty(config.RegionSlug("office"))
}
