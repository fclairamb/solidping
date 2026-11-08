//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceRunOutsideLaunchd(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, serviceRun(context.Background(), "io.solidping", t.TempDir()), errNotUnderLaunchd)
}

func TestCreateServiceDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "parent", "SolidPing")
	r.NoError(createServiceDir(dir))

	info, err := os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o700), info.Mode().Perm())
	r.DirExists(filepath.Join(dir, serviceLogDir))

	// An existing folder keeps its mode.
	r.NoError(os.Chmod(dir, 0o750))
	r.NoError(createServiceDir(dir))
	info, err = os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o750), info.Mode().Perm())
}
