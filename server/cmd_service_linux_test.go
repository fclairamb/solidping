//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceRunOutsideSystemd(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")

	require.ErrorIs(t, serviceRun(context.Background(), "solidping", t.TempDir()), errNotUnderSystemd)
}

func TestCreateSystemdServiceDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "parent", "solidping")
	r.NoError(createSystemdServiceDir(dir, -1))

	info, err := os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o700), info.Mode().Perm())
	r.NoDirExists(filepath.Join(dir, "logs"), "logs go to the journal")

	// An existing folder keeps its mode.
	r.NoError(os.Chmod(dir, 0o750))
	r.NoError(createSystemdServiceDir(dir, -1))
	info, err = os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o750), info.Mode().Perm())
}

func TestOthersCanExecute(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(othersCanExecute("/bin/sh"))

	private := filepath.Join(t.TempDir(), "private")
	r.NoError(os.Mkdir(private, 0o750))

	exe := filepath.Join(private, "solidping")
	r.NoError(os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	r.False(othersCanExecute(exe))
}
