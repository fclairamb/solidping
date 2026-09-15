//go:build windows

package nettrace

import "syscall"

// setSockoptInt wraps syscall.SetsockoptInt, converting the raw fd handed by
// syscall.RawConn.Control (a uintptr on every platform) to the syscall.Handle
// Windows' SetsockoptInt expects.
func setSockoptInt(handle uintptr, level, opt, value int) error {
	return syscall.SetsockoptInt(syscall.Handle(handle), level, opt, value)
}
