package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	DefaultPort           = 5672
	DefaultTimeout        = 10 * time.Second
	maxTimeout            = 30 * time.Second
	defaultVhost          = "/"
	DefaultMode           = ModeAMQP
	DefaultManagementPort = 15672
)

// Mode constants for RabbitMQ check modes.
const (
	ModeAMQP       = "amqp"
	ModeManagement = "management"
)

// Config map keys for the memory/disk thresholds — constants so FromMap /
// GetConfig / Validate cannot drift.
const (
	KeyMemoryUsedWarning  = "memoryUsedWarning"
	KeyMemoryUsedCritical = "memoryUsedCritical"
	KeyDiskFreeWarning    = "diskFreeWarning"
	KeyDiskFreeCritical   = "diskFreeCritical"
)

// minPercent/maxPercent bound the `NN%` form of a threshold.
const (
	minPercent = 1
	maxPercent = 100
)

// RabbitMQConfig holds the configuration for RabbitMQ health checks.
type RabbitMQConfig struct {
	Host           string        `json:"host"`
	Port           int           `json:"port,omitempty"`
	Username       string        `json:"username"`
	Password       string        `json:"password,omitempty"`
	Vhost          string        `json:"vhost,omitempty"`
	TLS            bool          `json:"tls,omitempty"`
	Mode           string        `json:"mode,omitempty"`
	ManagementPort int           `json:"managementPort,omitempty"`
	Queue          string        `json:"queue,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`

	// MemoryUsedWarning/Critical accept a percentage of RabbitMQ's high
	// watermark ("80%") or an absolute byte size ("1.5GiB"). Stored as the
	// string the user typed — see Threshold.Raw.
	MemoryUsedWarning  string `json:"memoryUsedWarning,omitempty"`
	MemoryUsedCritical string `json:"memoryUsedCritical,omitempty"`

	// DiskFreeWarning/Critical accept a byte size only: the management API
	// exposes no total disk size, so "percent free" cannot be computed.
	DiskFreeWarning  string `json:"diskFreeWarning,omitempty"`
	DiskFreeCritical string `json:"diskFreeCritical,omitempty"`
}

// Threshold is a parsed memory/disk threshold: either a percentage (memory
// only, relative to mem_limit) or an absolute byte size.
type Threshold struct {
	Raw       string
	IsPercent bool
	Percent   int
	Bytes     uint64
}

// ParseThreshold parses a threshold string. A `%` suffix yields a percentage
// (1-100); anything else is parsed as a byte size via humanize.ParseBytes.
// allowPercent is false for the disk keys, which have no total-size
// denominator to be a percentage of.
func ParseThreshold(key, raw string, allowPercent bool) (*Threshold, error) {
	if strings.HasSuffix(raw, "%") {
		if !allowPercent {
			return nil, checkerdef.NewConfigErrorf(
				key, "cannot be a percentage: the management API exposes no total disk size "+
					"to be a percentage of; use a byte size (e.g. %q)", "10GiB",
			)
		}

		digits := strings.TrimSuffix(raw, "%")

		pct, err := strconv.Atoi(digits)
		if err != nil || pct < minPercent || pct > maxPercent {
			return nil, checkerdef.NewConfigErrorf(key, "must be a percentage between 1%% and 100%%, got %q", raw)
		}

		return &Threshold{Raw: raw, IsPercent: true, Percent: pct}, nil
	}

	bytesVal, err := humanize.ParseBytes(raw)
	if err != nil {
		return nil, checkerdef.NewConfigErrorf(
			key, "must be a percentage (e.g. %q) or a byte size (e.g. %q), got %q", "80%", "1.5GiB", raw,
		)
	}

	return &Threshold{Raw: raw, Bytes: bytesVal}, nil
}

// compareThresholds returns -1/0/1 for warning </==/> critical. Callers must
// ensure both thresholds share a unit kind (both percent or both bytes)
// first — comparing across kinds is meaningless and is rejected upstream.
func compareThresholds(warning, critical *Threshold) int {
	if warning.IsPercent {
		return compareInts(warning.Percent, critical.Percent)
	}

	return compareUints(warning.Bytes, critical.Bytes)
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareUints(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// FromMap populates the configuration from a map.
func (c *RabbitMQConfig) FromMap(configMap map[string]any) error {
	if err := c.parseStringFields(configMap); err != nil {
		return err
	}

	if err := c.parsePortFields(configMap); err != nil {
		return err
	}

	if err := c.parseBoolAndDurationFields(configMap); err != nil {
		return err
	}

	return nil
}

func (c *RabbitMQConfig) parseStringFields(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	} else if configMap[checkerdef.OutputKeyHost] != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
	}

	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	} else if configMap["username"] != nil {
		return checkerdef.NewConfigError("username", "must be a string")
	}

	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	} else if configMap["password"] != nil {
		return checkerdef.NewConfigError("password", "must be a string")
	}

	if vhost, ok := configMap["vhost"].(string); ok {
		c.Vhost = vhost
	} else if configMap["vhost"] != nil {
		return checkerdef.NewConfigError("vhost", "must be a string")
	}

	if mode, ok := configMap["mode"].(string); ok {
		c.Mode = mode
	} else if configMap["mode"] != nil {
		return checkerdef.NewConfigError("mode", "must be a string")
	}

	if queue, ok := configMap["queue"].(string); ok {
		c.Queue = queue
	} else if configMap["queue"] != nil {
		return checkerdef.NewConfigError("queue", "must be a string")
	}

	return c.parseThresholdFields(configMap)
}

func (c *RabbitMQConfig) parseThresholdFields(configMap map[string]any) error {
	for key, target := range map[string]*string{
		KeyMemoryUsedWarning:  &c.MemoryUsedWarning,
		KeyMemoryUsedCritical: &c.MemoryUsedCritical,
		KeyDiskFreeWarning:    &c.DiskFreeWarning,
		KeyDiskFreeCritical:   &c.DiskFreeCritical,
	} {
		if configMap[key] == nil {
			continue
		}

		v, ok := configMap[key].(string)
		if !ok {
			return checkerdef.NewConfigError(key, "must be a string")
		}

		*target = v
	}

	return nil
}

func (c *RabbitMQConfig) parsePortFields(configMap map[string]any) error {
	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if mp, ok := configMap["managementPort"].(int); ok {
		c.ManagementPort = mp
	} else if mpFloat, ok := configMap["managementPort"].(float64); ok {
		c.ManagementPort = int(mpFloat)
	} else if configMap["managementPort"] != nil {
		return checkerdef.NewConfigError("managementPort", "must be a number")
	}

	return nil
}

func (c *RabbitMQConfig) parseBoolAndDurationFields(configMap map[string]any) error {
	if tlsVal, ok := configMap["tls"].(bool); ok {
		c.TLS = tlsVal
	} else if configMap["tls"] != nil {
		return checkerdef.NewConfigError("tls", "must be a boolean")
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *RabbitMQConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
		"username":               c.Username,
	}

	if c.Port != 0 && c.Port != DefaultPort {
		cfg["port"] = c.Port
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Vhost != "" && c.Vhost != defaultVhost {
		cfg["vhost"] = c.Vhost
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.Mode != "" && c.Mode != DefaultMode {
		cfg["mode"] = c.Mode
	}

	if c.ManagementPort != 0 && c.ManagementPort != DefaultManagementPort {
		cfg["managementPort"] = c.ManagementPort
	}

	if c.Queue != "" {
		cfg["queue"] = c.Queue
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	for key, value := range map[string]string{
		KeyMemoryUsedWarning:  c.MemoryUsedWarning,
		KeyMemoryUsedCritical: c.MemoryUsedCritical,
		KeyDiskFreeWarning:    c.DiskFreeWarning,
		KeyDiskFreeCritical:   c.DiskFreeCritical,
	} {
		if value != "" {
			cfg[key] = value
		}
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *RabbitMQConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "is required")
	}

	if c.Username == "" {
		return checkerdef.NewConfigError("username", "is required")
	}

	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.ManagementPort != 0 && (c.ManagementPort < 1 || c.ManagementPort > 65535) {
		return checkerdef.NewConfigErrorf("managementPort", "must be between 1 and 65535, got %d", c.ManagementPort)
	}

	if c.Mode != "" && c.Mode != ModeAMQP && c.Mode != ModeManagement {
		return checkerdef.NewConfigErrorf("mode", "must be %q or %q, got %q", ModeAMQP, ModeManagement, c.Mode)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String())
	}

	return c.validateThresholds()
}

// validateThresholds parses and cross-checks the four threshold keys:
//   - a threshold requires management mode (AMQP has no view of node
//     resources);
//   - disk keys reject a `%` value (enforced by ParseThreshold);
//   - when both tiers of the same resource are set AND share a unit kind
//     (both percentages or both byte sizes), enforce the tier ordering.
//     Mixed units (e.g. "70%" vs "1.8GiB") are accepted without
//     cross-checking, since there's no common scale to compare them on.
func (c *RabbitMQConfig) validateThresholds() error {
	fields := []struct {
		key          string
		raw          string
		allowPercent bool
	}{
		{KeyMemoryUsedWarning, c.MemoryUsedWarning, true},
		{KeyMemoryUsedCritical, c.MemoryUsedCritical, true},
		{KeyDiskFreeWarning, c.DiskFreeWarning, false},
		{KeyDiskFreeCritical, c.DiskFreeCritical, false},
	}

	effectiveMode := c.Mode
	if effectiveMode == "" {
		effectiveMode = DefaultMode
	}

	for i := range fields {
		if fields[i].raw == "" {
			continue
		}

		if effectiveMode != ModeManagement {
			return checkerdef.NewConfigErrorf(fields[i].key, "requires mode %q, got %q", ModeManagement, effectiveMode)
		}

		if _, err := ParseThreshold(fields[i].key, fields[i].raw, fields[i].allowPercent); err != nil {
			return err
		}
	}

	if err := c.validateTierOrder(
		KeyMemoryUsedWarning, c.MemoryUsedWarning, KeyMemoryUsedCritical, c.MemoryUsedCritical, true, true,
	); err != nil {
		return err
	}

	return c.validateTierOrder(
		KeyDiskFreeWarning, c.DiskFreeWarning, KeyDiskFreeCritical, c.DiskFreeCritical, false, false,
	)
}

// validateTierOrder enforces the ordering between a warning and a critical
// tier of the same resource, when both are set and share a unit kind.
// wantWarningLower requires warning < critical (memory: a ceiling, so the
// critical breach point is the bigger number); false requires
// warning > critical (disk: a floor, so the critical breach point is the
// smaller number).
func (c *RabbitMQConfig) validateTierOrder(
	warningKey, warningRaw, criticalKey, criticalRaw string, allowPercent, wantWarningLower bool,
) error {
	if warningRaw == "" || criticalRaw == "" {
		return nil
	}

	warning, err := ParseThreshold(warningKey, warningRaw, allowPercent)
	if err != nil {
		return err
	}

	critical, err := ParseThreshold(criticalKey, criticalRaw, allowPercent)
	if err != nil {
		return err
	}

	if warning.IsPercent != critical.IsPercent {
		return nil
	}

	// cmp < 0 means warning < critical, cmp == 0 means equal, cmp > 0 means
	// warning > critical. Both directions require a STRICT inequality, so
	// equal tiers are always rejected.
	cmp := compareThresholds(warning, critical)

	if wantWarningLower && cmp >= 0 {
		return checkerdef.NewConfigErrorf(
			criticalKey, "must be greater than %s (%s), got %s", warningKey, warning.Raw, critical.Raw,
		)
	}

	if !wantWarningLower && cmp <= 0 {
		return checkerdef.NewConfigErrorf(
			criticalKey, "must be less than %s (%s), got %s", warningKey, warning.Raw, critical.Raw,
		)
	}

	return nil
}

// BuildAMQPURI builds an AMQP connection URI from the configuration.
func (c *RabbitMQConfig) BuildAMQPURI() string {
	scheme := "amqp"
	if c.TLS {
		scheme = "amqps"
	}

	port := c.Port
	if port == 0 {
		port = DefaultPort
	}

	vhost := c.Vhost
	if vhost == "" {
		vhost = defaultVhost
	}

	amqpURL := &url.URL{
		Scheme: scheme,
		User:   url.UserPassword(c.Username, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, port),
		Path:   vhost,
	}

	return amqpURL.String()
}
