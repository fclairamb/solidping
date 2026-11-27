// Package checkhost reports the disk, memory and CPU usage of the host that
// runs the check, natively (through gopsutil, no exporter or agent needed).
//
// Thresholds follow the shared two-tier model of the prometheus check:
// critical breached gives StatusDown, warning breached gives StatusWarning,
// both compared with `>=`. Every metric is stored even without a threshold.
//
// The figures only mean something on the host that runs the check, so the
// type never runs through a tunnel, and an enforcing egress guard (a SaaS
// shared worker) refuses it before anything is read.
package checkhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhost/config"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const (
	defaultTimeout = 10 * time.Second

	// OutputKeyDiskPath is the path whose filesystem was measured.
	OutputKeyDiskPath = "disk_path"
	// OutputKeyBreaches lists the metrics that breached a threshold.
	OutputKeyBreaches = "breaches"

	// MetricDiskUsedPercent is the used percent of the measured filesystem.
	MetricDiskUsedPercent = "disk_used_percent"
	// MetricDiskFreeBytes is the free space of the measured filesystem.
	MetricDiskFreeBytes = "disk_free_bytes"
	// MetricMemoryUsedPercent is (total - available) / total of the memory.
	MetricMemoryUsedPercent = "memory_used_percent"
	// MetricMemoryAvailableBytes is the memory available to new workloads.
	MetricMemoryAvailableBytes = "memory_available_bytes"
	// MetricCPUPercent is the whole-host busy percent over a one second sample.
	MetricCPUPercent = "cpu_percent"
)

// ErrTunnelNotSupported is returned when a host check runs through a tunnel:
// the resources of a host mean nothing on the far side of a bastion.
var ErrTunnelNotSupported = errors.New("host checks cannot run through a tunnel")

// Sampler reads the host resources. It is injectable so tests never depend on
// the machine they run on.
type Sampler interface {
	// Disk returns the used percent and the free bytes of the filesystem at path.
	Disk(ctx context.Context, path string) (usedPercent float64, freeBytes uint64, err error)
	// Memory returns the used percent (total minus available) and the available bytes.
	Memory(ctx context.Context) (usedPercent float64, availableBytes uint64, err error)
	// CPU returns the whole-host busy percent over a one second sample.
	CPU(ctx context.Context) (percent float64, err error)
}

// HostChecker implements the Checker interface for host resources.
type HostChecker struct {
	// Sampler reads the resources; nil means the real host (gopsutil).
	Sampler Sampler
}

// Type returns the check type identifier.
func (c *HostChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeHost
}

// Validate checks the configuration.
func (c *HostChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute reads the three sources and grades them against the thresholds.
func (c *HostChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*HostConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	path := cfg.EffectiveDiskPath()

	if refusal := refuse(ctx, path); refusal != nil {
		return finish(errorResult(refusal.Error()), path, start), nil
	}

	if validateErr := cfg.Validate(); validateErr != nil {
		return finish(errorResult(validateErr.Error()), path, start), nil
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	sampler := c.Sampler
	if sampler == nil {
		sampler = gopsutilSampler{}
	}

	reading, err := read(ctx, sampler, path)
	if err != nil {
		if ctx.Err() != nil {
			return finish(&checkerdef.Result{
				Status: checkerdef.StatusTimeout,
				Output: map[string]any{checkerdef.OutputKeyError: err.Error()},
			}, path, start), nil
		}

		return finish(errorResult(err.Error()), path, start), nil
	}

	return finish(Grade(cfg, reading), path, start), nil
}

func refuse(ctx context.Context, path string) error {
	if checkerdef.TunnelDialerFrom(ctx) != nil {
		return ErrTunnelNotSupported
	}

	if egress.FromContext(ctx).Enforcing() {
		denied := &egress.DeniedError{Host: "host:" + path}
		egress.RecordDenial(ctx, denied)

		return denied
	}

	return nil
}

// Reading is one sample of the host resources.
type Reading struct {
	DiskUsedPercent      float64
	DiskFreeBytes        uint64
	MemoryUsedPercent    float64
	MemoryAvailableBytes uint64
	CPUPercent           float64
}

func read(ctx context.Context, sampler Sampler, path string) (*Reading, error) {
	reading := &Reading{}

	var err error

	if reading.DiskUsedPercent, reading.DiskFreeBytes, err = sampler.Disk(ctx, path); err != nil {
		return nil, fmt.Errorf("disk %s: %w", path, err)
	}

	if reading.MemoryUsedPercent, reading.MemoryAvailableBytes, err = sampler.Memory(ctx); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}

	if reading.CPUPercent, err = sampler.CPU(ctx); err != nil {
		return nil, fmt.Errorf("cpu: %w", err)
	}

	return reading, nil
}

// Grade maps a reading to a result: the worst status of the configured
// thresholds. Critical breached is down, warning breached is a warning.
func Grade(cfg *HostConfig, reading *Reading) *checkerdef.Result {
	metrics := map[string]any{
		MetricDiskUsedPercent:      reading.DiskUsedPercent,
		MetricDiskFreeBytes:        float64(reading.DiskFreeBytes),
		MetricMemoryUsedPercent:    reading.MemoryUsedPercent,
		MetricMemoryAvailableBytes: float64(reading.MemoryAvailableBytes),
		MetricCPUPercent:           reading.CPUPercent,
	}

	status := checkerdef.StatusUp

	var breaches []string

	metricList := []struct {
		name       string
		value      float64
		warn, crit *float64
	}{
		{"disk", reading.DiskUsedPercent, cfg.DiskWarningPercent, cfg.DiskCriticalPercent},
		{"memory", reading.MemoryUsedPercent, cfg.MemWarningPercent, cfg.MemCriticalPercent},
		{"cpu", reading.CPUPercent, cfg.CPUWarningPercent, cfg.CPUCriticalPercent},
	}

	for idx := range metricList {
		metric := &metricList[idx]

		switch {
		case metric.crit != nil && metric.value >= *metric.crit:
			status = checkerdef.StatusDown
			breaches = append(breaches,
				fmt.Sprintf("%s at %.1f%% >= critical %g%%", metric.name, metric.value, *metric.crit))
		case metric.warn != nil && metric.value >= *metric.warn:
			if status == checkerdef.StatusUp {
				status = checkerdef.StatusWarning
			}

			breaches = append(breaches,
				fmt.Sprintf("%s at %.1f%% >= warning %g%%", metric.name, metric.value, *metric.warn))
		}
	}

	output := map[string]any{}

	if len(breaches) > 0 {
		output[OutputKeyBreaches] = breaches
		output[checkerdef.OutputKeyMessage] = strings.Join(breaches, "; ")

		if status == checkerdef.StatusDown {
			output[checkerdef.OutputKeyError] = strings.Join(breaches, "; ")
		}
	}

	return &checkerdef.Result{Status: status, Metrics: metrics, Output: output}
}

func errorResult(msg string) *checkerdef.Result {
	return &checkerdef.Result{
		Status: checkerdef.StatusError,
		Output: map[string]any{checkerdef.OutputKeyError: msg},
	}
}

func finish(result *checkerdef.Result, path string, start time.Time) *checkerdef.Result {
	result.Duration = time.Since(start)

	if result.Output == nil {
		result.Output = map[string]any{}
	}

	result.Output[OutputKeyDiskPath] = path

	return result
}
