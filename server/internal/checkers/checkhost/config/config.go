// Package config holds the host resource check's configuration: the struct,
// its map parsing and serialization, and the whole offline rule set
// (ValidateSpec).
//
// It is free of the execution code (gopsutil) the parent checkhost package
// links, so `sp checks validate` can run the server's own validators against
// a config-as-code manifest. The parent keeps a type alias.
package config

import (
	"fmt"
	"os"
	"runtime"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config keys.
const (
	KeyDiskPath            = "diskPath"
	KeyDiskWarningPercent  = "diskWarningPercent"
	KeyDiskCriticalPercent = "diskCriticalPercent"
	KeyMemWarningPercent   = "memoryWarningPercent"
	KeyMemCriticalPercent  = "memoryCriticalPercent"
	KeyCPUWarningPercent   = "cpuWarningPercent"
	KeyCPUCriticalPercent  = "cpuCriticalPercent"
)

// HostConfig holds the configuration of a host resource check. Thresholds are
// pointers: 0 is never valid here, but "unset" must stay distinguishable from
// any value, as in the prometheus check.
type HostConfig struct {
	DiskPath            string   `json:"diskPath,omitempty"`
	DiskWarningPercent  *float64 `json:"diskWarningPercent,omitempty"`
	DiskCriticalPercent *float64 `json:"diskCriticalPercent,omitempty"`
	MemWarningPercent   *float64 `json:"memoryWarningPercent,omitempty"`
	MemCriticalPercent  *float64 `json:"memoryCriticalPercent,omitempty"`
	CPUWarningPercent   *float64 `json:"cpuWarningPercent,omitempty"`
	CPUCriticalPercent  *float64 `json:"cpuCriticalPercent,omitempty"`
}

// EffectiveDiskPath returns the path whose filesystem is measured: `/`, or
// the system drive on Windows.
func (c *HostConfig) EffectiveDiskPath() string {
	if c.DiskPath != "" {
		return c.DiskPath
	}

	if runtime.GOOS == "windows" {
		drive := os.Getenv("SystemDrive")
		if drive == "" {
			drive = "C:"
		}

		return drive + `\`
	}

	return "/"
}

// FromMap populates the configuration from a map.
func (c *HostConfig) FromMap(configMap map[string]any) error {
	switch value := configMap[KeyDiskPath].(type) {
	case nil:
	case string:
		c.DiskPath = value
	default:
		return checkerdef.NewConfigError(KeyDiskPath, "must be a string")
	}

	targets := []struct {
		key  string
		dest **float64
	}{
		{KeyDiskWarningPercent, &c.DiskWarningPercent},
		{KeyDiskCriticalPercent, &c.DiskCriticalPercent},
		{KeyMemWarningPercent, &c.MemWarningPercent},
		{KeyMemCriticalPercent, &c.MemCriticalPercent},
		{KeyCPUWarningPercent, &c.CPUWarningPercent},
		{KeyCPUCriticalPercent, &c.CPUCriticalPercent},
	}

	for _, target := range targets {
		value, err := readFloat(configMap, target.key)
		if err != nil {
			return err
		}

		*target.dest = value
	}

	return nil
}

func readFloat(configMap map[string]any, key string) (*float64, error) {
	var out float64

	switch value := configMap[key].(type) {
	case nil:
		return nil, nil //nolint:nilnil // absence is the documented outcome
	case float64:
		out = value
	case float32:
		out = float64(value)
	case int:
		out = float64(value)
	case int64:
		out = float64(value)
	default:
		return nil, checkerdef.NewConfigError(key, "must be a number")
	}

	return &out, nil
}

// GetConfig returns the configuration as a map.
func (c *HostConfig) GetConfig() map[string]any {
	cfg := map[string]any{}

	if c.DiskPath != "" {
		cfg[KeyDiskPath] = c.DiskPath
	}

	for key, value := range map[string]*float64{
		KeyDiskWarningPercent:  c.DiskWarningPercent,
		KeyDiskCriticalPercent: c.DiskCriticalPercent,
		KeyMemWarningPercent:   c.MemWarningPercent,
		KeyMemCriticalPercent:  c.MemCriticalPercent,
		KeyCPUWarningPercent:   c.CPUWarningPercent,
		KeyCPUCriticalPercent:  c.CPUCriticalPercent,
	} {
		if value != nil {
			cfg[key] = *value
		}
	}

	return cfg
}

// SchemaNotes implements checkerdef.SchemaNoter: the rules Validate() enforces
// that a schema reflected from this struct cannot state.
func (c *HostConfig) SchemaNotes() []string {
	return []string{
		"Every field is optional. Percent thresholds must be in (0, 100], and a warning threshold " +
			"must be below the critical one when both are set. A threshold is breached at `>=`: " +
			"warning gives a warning status, critical gives down.",
		"`diskPath` must be an absolute, clean path of the host that runs the check " +
			"(default `/`, the system drive on Windows, e.g. `D:\\`).",
		"The CPU figure is the whole-host busy percent sampled over one second, so the check takes " +
			"at least one second.",
		"A SaaS shared region never runs this type: it must be pinned to a private location.",
	}
}

// String is used in error messages.
func (c *HostConfig) String() string {
	return fmt.Sprintf("host(disk=%s)", c.EffectiveDiskPath())
}
