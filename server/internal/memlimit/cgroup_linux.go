//go:build linux

package memlimit

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// cgroupV2MemoryMax is where a cgroup-v2 (unified hierarchy) container sees its
// own memory limit. Inside a container the cgroup namespace roots here, so a
// direct read returns the container's limit.
const cgroupV2MemoryMax = "/sys/fs/cgroup/memory.max"

// cgroupV1MemoryLimit is the legacy cgroup-v1 memory limit path.
const cgroupV1MemoryLimit = "/sys/fs/cgroup/memory/memory.limit_in_bytes"

// cgroupUnlimitedFloor rejects the "effectively unlimited" sentinels both cgroup
// versions use (v1 reports a value near math.MaxInt64 rounded to the page size).
// Any limit at or above this is treated as "no limit".
const cgroupUnlimitedFloor = int64(1) << 62

// detectCgroupMemoryLimitOS reads the container memory limit from cgroup v2,
// then v1. It fails safe: any ambiguity returns (0, false) so no cap is applied.
func detectCgroupMemoryLimitOS() (int64, bool) {
	if v, ok := readCgroupLimit(cgroupV2MemoryMax); ok {
		return v, true
	}
	if v, ok := readCgroupLimit(cgroupV1MemoryLimit); ok {
		return v, true
	}
	return 0, false
}

// readCgroupLimit parses a single-value cgroup memory-limit file, treating the
// v2 "max" sentinel and v1's near-MaxInt64 sentinel as "no limit".
func readCgroupLimit(path string) (int64, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // fixed, well-known cgroup path
	if err != nil {
		return 0, false
	}

	s := strings.TrimSpace(string(data))
	if s == "" || s == "max" {
		return 0, false
	}

	limit, err := strconv.ParseInt(s, 10, 64)
	if err != nil || limit <= 0 || limit >= cgroupUnlimitedFloor || limit == math.MaxInt64 {
		return 0, false
	}

	return limit, true
}
