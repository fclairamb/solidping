package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
)

// ParamPlatformWatchdog is the system-parameter key the watchdog is
// configured through. It rides the existing super-admin
// /api/v1/system/parameters CRUD, so there is no new settings surface and no
// redeploy needed to add an operator.
const ParamPlatformWatchdog = "platform_watchdog"

// Watchdog defaults. Every one of them is overridable through the system
// parameter; these are the values an operator gets by writing nothing but
// `{"enabled": true, "recipients": ["<uid>"]}`.
const (
	// DefaultInterval is how often the job wakes up.
	DefaultInterval = time.Hour
	// DefaultRenotifyAfter bounds re-notification of an ONGOING anomaly.
	DefaultRenotifyAfter = 24 * time.Hour

	// DefaultDarkRegionMinOverdueJobs is the blast-radius floor: 419 stranded
	// jobs is page-worthy, one job 90 seconds late is not.
	DefaultDarkRegionMinOverdueJobs = 5
	// DefaultDarkRegionMinOverdueAge is how late the oldest overdue job must
	// be before the region counts as an anomaly.
	DefaultDarkRegionMinOverdueAge = 10 * time.Minute
	// DefaultDarkRegionCriticalJobs escalates a dark region to critical.
	DefaultDarkRegionCriticalJobs = 50
	// DefaultDarkRegionCriticalAge escalates a dark region to critical on
	// duration alone — a handful of jobs stranded for half an hour is still
	// a region nobody is serving.
	DefaultDarkRegionCriticalAge = 30 * time.Minute

	// DefaultFleetDropPercent is the drop, against the same hour a day
	// earlier, that counts as a collapse.
	DefaultFleetDropPercent = 50.0
	// DefaultFleetMinBaseline keeps a quiet instance (or a brand-new one)
	// from reporting a collapse: without a floor, 4 results vs. 10 is a 60%
	// "collapse".
	DefaultFleetMinBaseline = 100
	// DefaultFleetCriticalDropPercent escalates a collapse to critical.
	DefaultFleetCriticalDropPercent = 80.0

	// DefaultStaleIncidentMinAge is the floor of max(N × period, this).
	DefaultStaleIncidentMinAge = 15 * time.Minute
	// DefaultStaleIncidentPeriodMultiplier is the N of max(N × period, floor).
	DefaultStaleIncidentPeriodMultiplier = 3
	// DefaultStaleIncidentCriticalCount escalates the stale-incident anomaly
	// to critical — 61 frozen incidents was the 2026-08-24 number.
	DefaultStaleIncidentCriticalCount = 10
	// DefaultStaleIncidentScanLimit bounds the active-incident scan so the
	// run stays cheap on an instance with a genuinely huge outage.
	DefaultStaleIncidentScanLimit = 2000
)

// Config is the decoded `platform_watchdog` system parameter.
//
// Every threshold lives here rather than in koanf config for one reason: the
// people who tune a watchdog are the people running the instance, and they
// can already edit system parameters live. A threshold that needs a redeploy
// is a threshold nobody adjusts.
type Config struct {
	// Enabled gates the whole job. False means the run has NO side effects at
	// all: no queries, no state writes, no metrics, no delivery.
	Enabled bool `json:"enabled"`
	// Recipients are user UIDs. Delivery goes through each user's own enabled
	// notification routes — no new medium, no hardcoded webhook.
	Recipients []string `json:"recipients"`
	// MinSeverity filters DELIVERY only.
	MinSeverity string `json:"minSeverity"`

	IntervalMinutes      int `json:"intervalMinutes,omitempty"`
	RenotifyAfterMinutes int `json:"renotifyAfterMinutes,omitempty"`

	DarkRegionMinOverdueJobs    int `json:"darkRegionMinOverdueJobs,omitempty"`
	DarkRegionMinOverdueMinutes int `json:"darkRegionMinOverdueMinutes,omitempty"`
	DarkRegionCriticalJobs      int `json:"darkRegionCriticalJobs,omitempty"`
	DarkRegionCriticalMinutes   int `json:"darkRegionCriticalMinutes,omitempty"`

	FleetDropPercent         float64 `json:"fleetDropPercent,omitempty"`
	FleetMinBaseline         int     `json:"fleetMinBaseline,omitempty"`
	FleetCriticalDropPercent float64 `json:"fleetCriticalDropPercent,omitempty"`

	StaleIncidentMinMinutes       int `json:"staleIncidentMinMinutes,omitempty"`
	StaleIncidentPeriodMultiplier int `json:"staleIncidentPeriodMultiplier,omitempty"`
	StaleIncidentCriticalCount    int `json:"staleIncidentCriticalCount,omitempty"`
	StaleIncidentScanLimit        int `json:"staleIncidentScanLimit,omitempty"`
}

// DefaultConfig is the watchdog with everything at its documented default and
// the feature OFF — an instance that never writes the parameter must not
// suddenly start mailing whoever happens to be user #1.
func DefaultConfig() *Config {
	cfg := &Config{Enabled: false, MinSeverity: SeverityWarning.String()}
	cfg.applyDefaults()

	return cfg
}

// applyDefaults fills every unset (zero) threshold with its default. Zero is
// treated as "unset" throughout: a watchdog with a 0-minute re-notify window
// or a 0-job blast-radius floor is a flood machine, never a deliberate
// configuration.
func (c *Config) applyDefaults() {
	if c.MinSeverity == "" {
		c.MinSeverity = SeverityWarning.String()
	}

	if c.IntervalMinutes <= 0 {
		c.IntervalMinutes = int(DefaultInterval / time.Minute)
	}

	if c.RenotifyAfterMinutes <= 0 {
		c.RenotifyAfterMinutes = int(DefaultRenotifyAfter / time.Minute)
	}

	if c.DarkRegionMinOverdueJobs <= 0 {
		c.DarkRegionMinOverdueJobs = DefaultDarkRegionMinOverdueJobs
	}

	if c.DarkRegionMinOverdueMinutes <= 0 {
		c.DarkRegionMinOverdueMinutes = int(DefaultDarkRegionMinOverdueAge / time.Minute)
	}

	if c.DarkRegionCriticalJobs <= 0 {
		c.DarkRegionCriticalJobs = DefaultDarkRegionCriticalJobs
	}

	if c.DarkRegionCriticalMinutes <= 0 {
		c.DarkRegionCriticalMinutes = int(DefaultDarkRegionCriticalAge / time.Minute)
	}

	if c.FleetDropPercent <= 0 {
		c.FleetDropPercent = DefaultFleetDropPercent
	}

	if c.FleetMinBaseline <= 0 {
		c.FleetMinBaseline = DefaultFleetMinBaseline
	}

	if c.FleetCriticalDropPercent <= 0 {
		c.FleetCriticalDropPercent = DefaultFleetCriticalDropPercent
	}

	if c.StaleIncidentMinMinutes <= 0 {
		c.StaleIncidentMinMinutes = int(DefaultStaleIncidentMinAge / time.Minute)
	}

	if c.StaleIncidentPeriodMultiplier <= 0 {
		c.StaleIncidentPeriodMultiplier = DefaultStaleIncidentPeriodMultiplier
	}

	if c.StaleIncidentCriticalCount <= 0 {
		c.StaleIncidentCriticalCount = DefaultStaleIncidentCriticalCount
	}

	if c.StaleIncidentScanLimit <= 0 {
		c.StaleIncidentScanLimit = DefaultStaleIncidentScanLimit
	}
}

// Interval is the self-reschedule delay.
func (c *Config) Interval() time.Duration {
	return time.Duration(c.IntervalMinutes) * time.Minute
}

// RenotifyAfter is how long an ONGOING anomaly stays silent.
func (c *Config) RenotifyAfter() time.Duration {
	return time.Duration(c.RenotifyAfterMinutes) * time.Minute
}

// DarkRegionMinOverdueAge is how late the oldest overdue job must be.
func (c *Config) DarkRegionMinOverdueAge() time.Duration {
	return time.Duration(c.DarkRegionMinOverdueMinutes) * time.Minute
}

// DarkRegionCriticalAge is the age at which a dark region becomes critical.
func (c *Config) DarkRegionCriticalAge() time.Duration {
	return time.Duration(c.DarkRegionCriticalMinutes) * time.Minute
}

// StaleIncidentMinAge is the floor of max(N × period, floor).
func (c *Config) StaleIncidentMinAge() time.Duration {
	return time.Duration(c.StaleIncidentMinMinutes) * time.Minute
}

// Severity is the parsed delivery bar.
func (c *Config) Severity() Severity {
	return ParseSeverity(c.MinSeverity)
}

// Parameter validation errors.
var (
	// ErrInvalidParameterShape is returned when the value is not a JSON object.
	ErrInvalidParameterShape = errors.New("platform_watchdog must be a JSON object")
	// ErrInvalidRecipient is returned for a blank entry in recipients.
	ErrInvalidRecipient = errors.New("platform_watchdog recipients must be non-empty user UIDs")
	// ErrInvalidMinSeverity is returned for an unknown minSeverity token.
	ErrInvalidMinSeverity = errors.New(`platform_watchdog minSeverity must be one of "info", "warning", "critical"`)
)

// ValidateParameter rejects a platform_watchdog value the watchdog could not
// decode at run time.
//
// This runs on the write, not on the read, on purpose. A malformed value only
// surfaces at the next hourly run — as a failing job on an alerting path that
// nobody is watching, which is precisely the failure mode this spec exists to
// remove. Catching it in the PUT hands the mistake back to the person making
// it, while they are still looking.
func ValidateParameter(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidParameterShape, err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidParameterShape, err)
	}

	for _, recipient := range cfg.Recipients {
		if strings.TrimSpace(recipient) == "" {
			return ErrInvalidRecipient
		}
	}

	switch cfg.MinSeverity {
	case "", "info", "warning", "critical":
	default:
		return fmt.Errorf("%w (got %q)", ErrInvalidMinSeverity, cfg.MinSeverity)
	}

	return nil
}

// LoadConfig reads and decodes the `platform_watchdog` system parameter.
// A missing parameter yields the disabled default, which is the correct
// behavior for every instance that has never opted in.
func LoadConfig(ctx context.Context, dbSvc db.Service) (*Config, error) {
	param, err := dbSvc.GetSystemParameter(ctx, ParamPlatformWatchdog)
	if err != nil {
		return nil, fmt.Errorf("get platform_watchdog parameter: %w", err)
	}

	if param == nil || param.Value == nil {
		return DefaultConfig(), nil
	}

	raw, err := json.Marshal(param.Value["value"])
	if err != nil {
		return nil, fmt.Errorf("marshal platform_watchdog value: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("unmarshal platform_watchdog value: %w", err)
	}

	cfg.applyDefaults()

	return cfg, nil
}
