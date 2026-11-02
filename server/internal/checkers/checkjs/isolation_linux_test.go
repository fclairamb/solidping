//go:build linux

package checkjs

import "syscall"

// testRlimitAS is this process's RLIMIT_AS.
func testRlimitAS() uint64 {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &limit); err != nil {
		return 0
	}

	return limit.Cur
}
