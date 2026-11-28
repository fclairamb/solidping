package checkhost

import (
	"context"
	"errors"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	cpuSampleInterval = time.Second
	percentFactor     = 100
)

var (
	errNoCPUSample = errors.New("no cpu sample returned")
	errNoMemory    = errors.New("total memory is 0")
)

// gopsutilSampler reads the real host on Linux, macOS and Windows.
type gopsutilSampler struct{}

func (gopsutilSampler) Disk(ctx context.Context, path string) (float64, uint64, error) {
	usage, err := disk.UsageWithContext(ctx, path)
	if err != nil {
		return 0, 0, err //nolint:wrapcheck // wrapped by the caller with the source
	}

	return usage.UsedPercent, usage.Free, nil
}

func (gopsutilSampler) Memory(ctx context.Context) (float64, uint64, error) {
	virtual, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, 0, err //nolint:wrapcheck // wrapped by the caller with the source
	}

	if virtual.Total == 0 {
		return 0, 0, errNoMemory
	}

	used := float64(virtual.Total-min(virtual.Available, virtual.Total)) / float64(virtual.Total) * percentFactor

	return used, virtual.Available, nil
}

func (gopsutilSampler) CPU(ctx context.Context) (float64, error) {
	percents, err := cpu.PercentWithContext(ctx, cpuSampleInterval, false)
	if err != nil {
		return 0, err //nolint:wrapcheck // wrapped by the caller with the source
	}

	if len(percents) == 0 {
		return 0, errNoCPUSample
	}

	return percents[0], nil
}
