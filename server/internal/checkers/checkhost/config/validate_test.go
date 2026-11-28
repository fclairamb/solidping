package config_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhost/config"
)

func validate(cfg map[string]any) (*checkerdef.CheckSpec, error) {
	spec := &checkerdef.CheckSpec{Config: cfg}

	return spec, config.ValidateSpec(spec)
}

func TestNoThresholdAtAllIsAccepted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec, err := validate(map[string]any{})
	r.NoError(err)
	r.Equal("host", spec.Slug)
}

func TestDefaultDiskPath(t *testing.T) {
	t.Parallel()

	cfg := &config.HostConfig{}
	if runtime.GOOS == "windows" {
		require.Contains(t, cfg.EffectiveDiskPath(), `:\`)
	} else {
		require.Equal(t, "/", cfg.EffectiveDiskPath())
	}

	cfg.DiskPath = "/data"
	require.Equal(t, "/data", cfg.EffectiveDiskPath())
}

func TestPercentOutOfRangeRejected(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		config.KeyDiskWarningPercent, config.KeyDiskCriticalPercent,
		config.KeyMemWarningPercent, config.KeyMemCriticalPercent,
		config.KeyCPUWarningPercent, config.KeyCPUCriticalPercent,
	} {
		for _, bad := range []float64{0, -1, 100.5, 1000} {
			_, err := validate(map[string]any{key: bad})
			require.Error(t, err, "%s=%v", key, bad)
			require.Equal(t, key, checkerdef.IsConfigError(err).Parameter)
		}

		_, err := validate(map[string]any{key: 100.0})
		require.NoError(t, err, key)
	}

	_, err := validate(map[string]any{config.KeyCPUWarningPercent: "high"})
	require.Error(t, err)
}

func TestWarningMustBeBelowCritical(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, err := validate(map[string]any{config.KeyDiskWarningPercent: 90.0, config.KeyDiskCriticalPercent: 90.0})
	r.Error(err)
	r.Equal(config.KeyDiskWarningPercent, checkerdef.IsConfigError(err).Parameter)

	_, err = validate(map[string]any{config.KeyMemWarningPercent: 95, config.KeyMemCriticalPercent: 90})
	r.Error(err)

	_, err = validate(map[string]any{config.KeyCPUWarningPercent: 80, config.KeyCPUCriticalPercent: 90})
	r.NoError(err)

	// A warning alone, or a critical alone, is valid.
	_, err = validate(map[string]any{config.KeyCPUCriticalPercent: 90})
	r.NoError(err)
}

func TestDiskPathRules(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, bad := range []string{"relative/path", "data", "/a/../b", "/a//b", "/a\x00b"} {
		_, err := validate(map[string]any{config.KeyDiskPath: bad})
		r.Error(err, "%q", bad)
		r.Equal(config.KeyDiskPath, checkerdef.IsConfigError(err).Parameter)
	}

	_, err := validate(map[string]any{config.KeyDiskPath: 3})
	r.Error(err)

	good := filepath.Join(string(filepath.Separator), "var", "lib")
	if runtime.GOOS == "windows" {
		good = `D:\`
	}

	_, err = validate(map[string]any{config.KeyDiskPath: good})
	r.NoError(err)
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	in := map[string]any{config.KeyDiskPath: "/var", config.KeyDiskCriticalPercent: 90.0}
	cfg := &config.HostConfig{}
	r.NoError(cfg.FromMap(in))
	r.Equal(in, cfg.GetConfig())
}
