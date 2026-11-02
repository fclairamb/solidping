//go:build !linux

package checkjs

// testRlimitAS is 0: no RLIMIT_AS is set here.
func testRlimitAS() uint64 {
	return 0
}
