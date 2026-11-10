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

	require.ErrorIs(t, serviceRun(context.Background(), "io.solidping", t.TempDir(), ""), errNotUnderLaunchd)
}

func TestCreateServiceDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	owners := launchdTrustedUIDs(os.Geteuid())
	dir := filepath.Join(t.TempDir(), "parent", "SolidPing")
	r.NoError(createServiceDir(dir, owners))

	info, err := os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o700), info.Mode().Perm())
	r.DirExists(filepath.Join(dir, serviceLogDir))

	// An existing folder keeps its mode.
	r.NoError(os.Chmod(dir, 0o750))
	r.NoError(createServiceDir(dir, owners))
	info, err = os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o750), info.Mode().Perm())
}

func TestLaunchdTrustedUIDs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A daemon runs as root: a Homebrew binary, owned by its user, is refused.
	r.Equal([]int{0}, launchdTrustedUIDs(0))
	r.Equal([]int{0, 501}, launchdTrustedUIDs(501))
}

func TestCreateServiceDirRefusesExistingFolder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(r *require.Assertions, dir string)
		want    error
	}{
		{
			name: "is a link",
			prepare: func(r *require.Assertions, dir string) {
				target := dir + ".target"
				r.NoError(os.Mkdir(target, 0o700))
				r.NoError(os.Symlink(target, dir))
			},
			want: errDataDirReparse,
		},
		{
			name: "can be written by its group",
			prepare: func(r *require.Assertions, dir string) {
				r.NoError(os.Mkdir(dir, 0o700))
				r.NoError(os.Chmod(dir, 0o770))
			},
			want: errPathWritable,
		},
		{
			name: "can be written by anyone",
			prepare: func(r *require.Assertions, dir string) {
				r.NoError(os.Mkdir(dir, 0o700))
				r.NoError(os.Chmod(dir, 0o777))
			},
			want: errPathWritable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			owners := launchdTrustedUIDs(os.Geteuid())
			dir := filepath.Join(t.TempDir(), "SolidPing")
			tt.prepare(r, dir)

			err := createServiceDir(dir, owners)
			r.ErrorIs(err, errUntrustedDataDir)
			r.ErrorIs(err, tt.want)
			r.NoDirExists(filepath.Join(dir, serviceLogDir))
		})
	}
}

func TestCreateServiceDirRefusesSharedParent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	shared := filepath.Join(t.TempDir(), "shared")
	r.NoError(os.Mkdir(shared, 0o700))
	r.NoError(os.Chmod(shared, 0o777))

	err := createServiceDir(filepath.Join(shared, "SolidPing"), launchdTrustedUIDs(os.Geteuid()))
	r.ErrorIs(err, errUntrustedParent)
	r.ErrorIs(err, errPathWritable)
	r.NoDirExists(filepath.Join(shared, "SolidPing"))
}

func TestServiceRunPathsRefuseDataDirOpenedAfterInstall(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "SolidPing")
	r.NoError(createServiceDir(dir, launchdTrustedUIDs(os.Geteuid())))
	r.NoError(checkServiceRunPaths(dir))

	r.NoError(os.Chmod(dir, 0o777))

	err := checkServiceRunPaths(dir)
	r.ErrorIs(err, errUntrustedDataDir)
	r.ErrorIs(err, errPathWritable)
}

func TestServiceBinaryRefusesWritableFolder(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	shared := filepath.Join(t.TempDir(), "shared")
	r.NoError(os.Mkdir(shared, 0o700))
	r.NoError(os.Chmod(shared, 0o777))

	exe := filepath.Join(shared, "solidping")
	r.NoError(os.WriteFile(exe, nil, 0o755))

	_, err := checkServiceBinary(exe, launchdTrustedUIDs(os.Geteuid()))
	r.ErrorIs(err, errUntrustedBinary)
	r.ErrorIs(err, errPathWritable)

	// Run by sudo, a binary the user owns (Homebrew's) is refused too.
	private := filepath.Join(t.TempDir(), "solidping")
	r.NoError(os.WriteFile(private, nil, 0o755))
	_, err = checkServiceBinary(private, launchdTrustedUIDs(0))
	if os.Geteuid() != 0 {
		r.ErrorIs(err, errDataDirOwner)
	}
}
