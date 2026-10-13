//go:build unix

package sqlite

import "syscall"

// freeDiskBytes returns the free space available to unprivileged writers on the
// filesystem holding dir.
func freeDiskBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}

	return uint64(st.Bavail) * uint64(st.Bsize), true //nolint:unconvert // Bavail/Bsize types differ per OS
}
