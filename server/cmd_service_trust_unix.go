//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// unixBinaryFix is how to fix a binary install refuses: put it where only root
// can write.
const unixBinaryFix = "Move it to a folder only root can write, " +
	"for example: sudo install -o root -m 0755 solidping /usr/local/bin/"

var (
	errPathWritable = errors.New("can be written by its group or by others")
	errPathNoOwner  = errors.New("has no readable owner")
)

// superUserIDs are the accounts trusted everywhere: root. A seam, so the tests
// can stand for root without being it.
var superUserIDs = []int{0} //nolint:gochecknoglobals // seam

// trustedUIDs returns superUserIDs plus extra.
func trustedUIDs(extra ...int) []int {
	return append(slices.Clone(superUserIDs), extra...)
}

// checkTrustedPath accepts path only if every part of it, from the root down,
// is owned by one of owners and is not writable by its group or by others.
// Otherwise another account could replace it, or something above it. A link
// is accepted when one of owners made it, and its target is checked the same
// way. The sticky bit does not make a shared folder such as /tmp acceptable.
func checkTrustedPath(path string, owners []int) error {
	return walkTrustedPath(path, func(part string) (bool, error) {
		info, err := os.Lstat(part)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, err
			}

			return false, fmt.Errorf("check %s: %w", part, err)
		}

		if err = checkPathOwner(part, info, owners); err != nil {
			return false, err
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			return true, nil
		}

		if info.Mode().Perm()&0o022 != 0 {
			return false, fmt.Errorf("%s %w (mode %s)", part, errPathWritable, info.Mode().Perm())
		}

		return false, nil
	})
}

func checkPathOwner(path string, info fs.FileInfo, owners []int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s %w", path, errPathNoOwner)
	}

	if !slices.Contains(owners, int(stat.Uid)) {
		return fmt.Errorf("%s %w uid %d", path, errDataDirOwner, stat.Uid)
	}

	return nil
}

// checkServiceBinary resolves exe and refuses it when an account outside
// owners could replace it. It returns the resolved path, the one to register.
func checkServiceBinary(exe string, owners []int) (string, error) {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", exe, err)
	}

	if err = checkTrustedPath(resolved, owners); err != nil {
		return "", untrustedBinary(err, unixBinaryFix)
	}

	return resolved, nil
}

// checkServiceDirParents refuses a data folder whose parents an account outside
// owners could change: it could swap the data folder for its own.
func checkServiceDirParents(dir string, owners []int) error {
	if err := checkTrustedPath(filepath.Dir(dir), owners); err != nil {
		return untrustedParent(err)
	}

	return nil
}

// checkServiceDir accepts a data folder that already exists only if the
// service account controls it alone, as a previous install leaves it: a real
// folder owned by one of owners, not writable by its group or by others, with
// nothing inside that is a link or belongs to someone else. The service reads
// config.yml there, and plugins.dir in it starts executables.
func checkServiceDir(dir string, owners []int) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check data folder %s: %w", dir, err)
	}

	if err = checkServiceDirEntry(dir, info, owners); err != nil {
		return untrustedServiceDir(err)
	}

	if !info.IsDir() {
		return untrustedServiceDir(fmt.Errorf("%s %w", dir, errDataDirNotFolder))
	}

	if info.Mode().Perm()&0o022 != 0 {
		return untrustedServiceDir(fmt.Errorf("%s %w (mode %s)", dir, errPathWritable, info.Mode().Perm()))
	}

	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if path == dir {
			return nil
		}

		// WalkDir does not follow links, and is called before it enters a
		// folder: a link is refused before anything behind it is read.
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("check %s: %w", path, infoErr)
		}

		return checkServiceDirEntry(path, entryInfo, owners)
	})
	if err != nil {
		return untrustedServiceDir(err)
	}

	return nil
}

func checkServiceDirEntry(path string, info fs.FileInfo, owners []int) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s %w", path, errDataDirReparse)
	}

	return checkPathOwner(path, info, owners)
}

// checkServiceRunPaths is what `service run` checks before it enters dir: the
// binary, the data folder and its parents may only be controlled by root and
// by the account the service runs as.
func checkServiceRunPaths(dir string) error {
	owners := trustedUIDs(os.Geteuid())

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate solidping: %w", err)
	}

	if _, err = checkServiceBinary(exe, owners); err != nil {
		return err
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve data folder %s: %w", dir, err)
	}

	if err = checkServiceDirParents(absDir, owners); err != nil {
		return err
	}

	return checkServiceDir(absDir, owners)
}
