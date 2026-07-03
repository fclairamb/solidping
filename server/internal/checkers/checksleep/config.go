package checksleep

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// configKeySleepMs is the JSON/config key for the sleep duration.
const configKeySleepMs = "sleep_ms"

// SleepConfig holds the configuration for the synthetic sleep check.
//
// The sleep check performs no network I/O. It simply sleeps for a configured
// duration (optionally jittered) and returns a configurable status. Its cost is
// exactly SleepMs, which makes it a precise, deterministic load generator for
// exercising the scheduler (cost EWMA, cost-aware timeout, delay EWMA) without
// any real endpoint. It is a synthetic/testing check type and is NOT a
// customer-facing feature.
type SleepConfig struct {
	// SleepMs is how long to sleep, in milliseconds (required, >0, <= cap).
	SleepMs int `json:"sleep_ms,omitempty"` //nolint:tagliatelle // API uses snake_case

	// JitterMs adds a random ± jitter to the sleep, in milliseconds
	// (optional, >=0 and < SleepMs). 0 = deterministic timing.
	JitterMs int `json:"jitter_ms,omitempty"` //nolint:tagliatelle // API uses snake_case

	// Status optionally forces the returned status: ""|up|down|timeout|error
	// (default up). It is returned after sleeping.
	Status string `json:"status,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *SleepConfig) FromMap(configMap map[string]any) error {
	if v, ok := readIntKey(configMap, configKeySleepMs); ok {
		c.SleepMs = v
	} else if configMap[configKeySleepMs] != nil {
		return checkerdef.NewConfigError(configKeySleepMs, "must be a number")
	}

	if v, ok := readIntKey(configMap, "jitter_ms"); ok {
		c.JitterMs = v
	} else if configMap["jitter_ms"] != nil {
		return checkerdef.NewConfigError("jitter_ms", "must be a number")
	}

	if status, ok := configMap["status"].(string); ok {
		c.Status = status
	} else if configMap["status"] != nil {
		return checkerdef.NewConfigError("status", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SleepConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		configKeySleepMs: c.SleepMs,
	}

	if c.JitterMs != 0 {
		cfg["jitter_ms"] = c.JitterMs
	}

	if c.Status != "" {
		cfg["status"] = c.Status
	}

	return cfg
}

// readIntKey reads an integer field, accepting both int and float64 (JSON
// numbers decode to float64).
func readIntKey(configMap map[string]any, key string) (int, bool) {
	switch v := configMap[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
