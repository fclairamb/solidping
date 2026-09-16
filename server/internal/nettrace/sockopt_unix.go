//go:build !windows

package nettrace

import "syscall"

// setSockoptInt wraps syscall.SetsockoptInt, converting the raw fd handed by
// syscall.RawConn.Control (a uintptr on every platform) to the int file
// descriptor this platform's SetsockoptInt expects.
func setSockoptInt(handle uintptr, level, opt, value int) error {
	return syscall.SetsockoptInt(int(handle), level, opt, value)
}
