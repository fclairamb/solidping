//go:build windows || darwin || linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// maxTrustedPathLinks bounds how many links walkTrustedPath follows, so a link
// that keeps changing under it cannot make it loop.
const maxTrustedPathLinks = 8

var (
	errUntrustedDataDir = errors.New("data folder is not safe for the service")
	errDataDirReparse   = errors.New("is a link or a junction")
	errDataDirNotFolder = errors.New("is not a folder")
	errDataDirOwner     = errors.New("is owned by")
	errUntrustedBinary  = errors.New("the solidping binary is not safe for the service")
	errUntrustedParent  = errors.New("a parent of the data folder is not safe for the service")
	errTooManyPathLinks = errors.New("too many links")
)

// trustAnchor, when set, is a folder walkTrustedPath trusts without checking
// it or its parents. Production leaves it empty and walks up to the root; the
// tests point it at the temporary folder, which is shared by every user.
var trustAnchor = "" //nolint:gochecknoglobals // seam

// walkTrustedPath calls check on path and on each of its parents, up to the
// root. A part that does not exist yet is skipped: install creates it. check
// examines one part without following it and reports whether it is a link; the
// target of a link is then walked the same way, so both the link (which only
// its folder's writers can replace) and what it points to are checked.
func walkTrustedPath(path string, check func(part string) (link bool, err error)) error {
	return walkTrustedPathDepth(filepath.Clean(path), check, 0)
}

func walkTrustedPathDepth(path string, check func(string) (bool, error), links int) error {
	anchor := ""
	if trustAnchor != "" {
		anchor = filepath.Clean(trustAnchor)
	}

	for part := path; ; {
		if part == anchor {
			return nil
		}

		link, err := check(part)

		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case link:
			if links >= maxTrustedPathLinks {
				return fmt.Errorf("%s: %w", part, errTooManyPathLinks)
			}

			target, evalErr := filepath.EvalSymlinks(part)
			if evalErr != nil {
				return fmt.Errorf("resolve %s: %w", part, evalErr)
			}

			if target != part {
				if err = walkTrustedPathDepth(target, check, links+1); err != nil {
					return err
				}
			}
		}

		parent := filepath.Dir(part)
		if parent == part {
			return nil
		}

		part = parent
	}
}

// untrustedBinary explains how to fix a binary that someone other than an
// administrator can replace.
func untrustedBinary(err error, fix string) error {
	return fmt.Errorf("%w: %w. %s, then install again", errUntrustedBinary, err, fix)
}

// untrustedParent explains how to fix a data folder whose parent someone other
// than an administrator controls.
func untrustedParent(err error) error {
	return fmt.Errorf("%w: %w. Choose another folder with --dir", errUntrustedParent, err)
}

// untrustedServiceDir wraps an error that already names the file at fault.
func untrustedServiceDir(err error) error {
	return fmt.Errorf("%w: %w. Delete the folder, or choose another one with --dir", errUntrustedDataDir, err)
}
