//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The temporary folder is shared by every user (/tmp is world-writable), so
// the tests trust it and check what they create in it.
func init() { //nolint:gochecknoinits // test seam
	trustAnchor = os.TempDir()
}

func TestServiceTrustedPathRootChain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// /bin/sh resolves to a root-owned binary in root-owned folders.
	exe, err := checkServiceBinary("/bin/sh", []int{0})
	r.NoError(err)
	r.NotEmpty(exe)

	// A folder that does not exist yet is skipped: install creates it.
	r.NoError(checkTrustedPath("/usr/solidping-absent/data", []int{0}))
}

func TestServiceTrustedPathRefusesSharedFolders(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	me := trustedUIDs(os.Geteuid())

	for _, mode := range []os.FileMode{0o777, 0o775, 0o757, 0o1777} {
		shared := filepath.Join(t.TempDir(), "shared")
		r.NoError(os.Mkdir(shared, 0o700))
		r.NoError(os.Chmod(shared, mode))

		// The sticky bit (/tmp) does not make a parent acceptable.
		r.ErrorIs(checkTrustedPath(filepath.Join(shared, "solidping"), me), errPathWritable, mode.String())
	}

	private := filepath.Join(t.TempDir(), "private")
	r.NoError(os.Mkdir(private, 0o755))
	r.NoError(checkTrustedPath(filepath.Join(private, "solidping"), me))

	// The test's files stand for another user's.
	r.ErrorIs(checkTrustedPath(private, []int{os.Geteuid() + 1}), errDataDirOwner)
}

func TestServiceTrustedPathFollowsLinks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	me := trustedUIDs(os.Geteuid())
	base := t.TempDir()

	safe := filepath.Join(base, "safe")
	r.NoError(os.Mkdir(safe, 0o755))
	r.NoError(os.Symlink(safe, filepath.Join(base, "to-safe")))
	r.NoError(checkTrustedPath(filepath.Join(base, "to-safe", "solidping"), me))

	// The link is fine, its target is not.
	shared := filepath.Join(base, "shared")
	r.NoError(os.Mkdir(shared, 0o700))
	r.NoError(os.Chmod(shared, 0o777))
	r.NoError(os.Symlink(shared, filepath.Join(base, "to-shared")))
	r.ErrorIs(checkTrustedPath(filepath.Join(base, "to-shared", "solidping"), me), errPathWritable)

	// A link made by someone else (the test's own stands for it).
	r.ErrorIs(checkTrustedPath(filepath.Join(base, "to-safe"), []int{os.Geteuid() + 1}), errDataDirOwner)
}

func TestCheckServiceDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	me := trustedUIDs(os.Geteuid())

	// What an install leaves: accepted again.
	dir := filepath.Join(t.TempDir(), "data")
	r.NoError(os.Mkdir(dir, 0o700))
	r.NoError(os.MkdirAll(filepath.Join(dir, "logs"), 0o750))
	r.NoError(os.WriteFile(filepath.Join(dir, "config.yml"), nil, 0o600))
	r.NoError(checkServiceDir(dir, me))

	// Owned by another account.
	err := checkServiceDir(dir, []int{os.Geteuid() + 1})
	r.ErrorIs(err, errUntrustedDataDir)
	r.ErrorIs(err, errDataDirOwner)

	// A link inside, which could lead the service anywhere.
	r.NoError(os.Symlink("/etc", filepath.Join(dir, "plugins")))
	err = checkServiceDir(dir, me)
	r.ErrorIs(err, errUntrustedDataDir)
	r.ErrorIs(err, errDataDirReparse)

	// A file in place of the folder.
	file := filepath.Join(t.TempDir(), "file")
	r.NoError(os.WriteFile(file, nil, 0o600))
	r.ErrorIs(checkServiceDir(file, me), errDataDirNotFolder)

	r.ErrorContains(checkServiceDir(filepath.Join(t.TempDir(), "absent"), me), "check data folder")
}
