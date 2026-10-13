//go:build linux

package memlimit

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// procMeminfo is where Linux reports the host's RAM.
const procMeminfo = "/proc/meminfo"

// kib is the unit /proc/meminfo reports in.
const kib = 1024

// detectHostMemoryOS reads MemTotal from /proc/meminfo. Inside a container with
// no memory limit it reports the host, which is what a native install wants.
// Fails safe: any parse problem returns (0, false).
func detectHostMemoryOS() (int64, bool) {
	data, err := os.ReadFile(procMeminfo)
	if err != nil {
		return 0, false
	}
	return parseMemTotal(data)
}

// parseMemTotal extracts "MemTotal: <n> kB" in bytes.
func parseMemTotal(data []byte) (int64, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n * kib, true
	}
	return 0, false
}
