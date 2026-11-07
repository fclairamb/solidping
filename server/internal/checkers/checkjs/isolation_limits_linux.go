//go:build linux

package checkjs

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

const (
	// rlimitSlack is the address space a child may map past its current
	// size and twice its limit: Go maps its heap in 64 MiB arenas, and
	// threads need stacks.
	rlimitSlack = 256 << 20
	// cgroupSlack is what a child's cgroup allows past its heap limit: the
	// binary, the runtime and the goja VM itself.
	cgroupSlack = 128 << 20
	// kibibyte converts /proc/self/status sizes.
	kibibyte = 1024
)

//nolint:gochecknoglobals // Names each child's cgroup.
var childCgroupSeq atomic.Int64

// setChildRlimit caps the child's address space (RLIMIT_AS) and data
// (RLIMIT_DATA), measured from what the Go runtime already mapped: an
// allocation past the cap fails, and the runtime dies with "out of memory"
// instead of the host.
func setChildRlimit(limit int64) error {
	sizes, err := procStatusSizes("VmSize", "VmData")
	if err != nil {
		return err
	}

	for resource, current := range map[int]int64{
		syscall.RLIMIT_AS:   sizes["VmSize"],
		syscall.RLIMIT_DATA: sizes["VmData"],
	} {
		capBytes := uint64(current + 2*limit + rlimitSlack)

		if err := syscall.Setrlimit(resource, &syscall.Rlimit{Cur: capBytes, Max: capBytes}); err != nil {
			return fmt.Errorf("setrlimit %d: %w", resource, err)
		}
	}

	return nil
}

// procStatusSizes reads sizes (in bytes) from /proc/self/status.
func procStatusSizes(names ...string) (map[string]int64, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return nil, fmt.Errorf("reading /proc/self/status: %w", err)
	}
	defer func() { _ = file.Close() }()

	sizes := map[string]int64{}
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}

		for _, name := range names {
			if key != name {
				continue
			}

			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}

			if kib, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				sizes[name] = kib * kibibyte
			}
		}
	}

	return sizes, nil
}

// configureChildCmd kills the child with its parent, and puts it in its own
// cgroup under settings.CgroupDir, memory.max set, when one is configured.
func configureChildCmd(cmd *exec.Cmd, settings IsolationSettings) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	if settings.CgroupDir == "" {
		return func() {}, nil
	}

	dir := filepath.Join(settings.CgroupDir,
		fmt.Sprintf("sp-js-%d-%d", os.Getpid(), childCgroupSeq.Add(1)))

	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating the script cgroup: %w", err)
	}

	removeDir := func() { _ = os.Remove(dir) }

	maxBytes := strconv.FormatInt(settings.MemoryLimit+cgroupSlack, 10)
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(maxBytes), 0o600); err != nil {
		removeDir()

		return nil, fmt.Errorf("setting the script cgroup memory.max: %w", err)
	}

	_ = os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0o600)

	dirFD, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		removeDir()

		return nil, fmt.Errorf("opening the script cgroup: %w", err)
	}

	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = dirFD

	return func() {
		_ = syscall.Close(dirFD)
		removeDir()
	}, nil
}
