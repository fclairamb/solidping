//go:build !linux

package memlimit

// detectCgroupMemoryLimitOS is a no-op on non-Linux platforms (macOS dev,
// Windows): there is no cgroup memory limit to read, so auto-derivation is
// skipped and the runtime default stands.
func detectCgroupMemoryLimitOS() (int64, bool) {
	return 0, false
}

// detectHostMemoryOS is a no-op on non-Linux platforms: the host-derived cap is
// meant for small Linux hosts (Raspberry Pi, small VPS).
func detectHostMemoryOS() (int64, bool) {
	return 0, false
}
