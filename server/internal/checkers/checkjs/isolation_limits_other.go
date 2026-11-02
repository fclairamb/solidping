//go:build !linux

package checkjs

import "os/exec"

// setChildRlimit has no hard limit to set here: the platform does not
// enforce RLIMIT_AS. The child's watchdog is the limit.
func setChildRlimit(_ int64) error {
	return nil
}

// configureChildCmd has nothing to configure here.
func configureChildCmd(_ *exec.Cmd, _ IsolationSettings) (func(), error) {
	return func() {}, nil
}
