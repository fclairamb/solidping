package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// SLO alert policy kinds. There are exactly two built-ins — the product ships
// them, operators tune them, nobody invents a third from the API.
const (
	// SLOAlertPolicyKindFast is the "we are about to spend the month in an
	// afternoon" policy: a short long-window and a high threshold.
	SLOAlertPolicyKindFast = "fast"
	// SLOAlertPolicyKindSlow is the "this has been quietly eating the budget
	// all morning" policy: a longer window and a lower threshold.
	SLOAlertPolicyKindSlow = "slow"
)

// Alert severities. Not stored on the incident — this codebase attaches
// severity to escalation-policy steps, not to incidents — but carried into the
// incident title, details and every notification body so the person paged can
// tell a fast burn from a slow one without opening the dashboard.
const (
	// SLOAlertSeverityCritical is the fast-burn default.
	SLOAlertSeverityCritical = "critical"
	// SLOAlertSeverityWarning is the slow-burn default.
	SLOAlertSeverityWarning = "warning"
)

// DefaultSLOAlertMinSamples is the per-window probe floor below which a window
// is inconclusive.
//
// Deliberately low. A check whose period exceeds shortWindow/minSamples can
// never satisfy its own short window, and an alert policy that silently never
// fires is worse than one that occasionally fires on thin evidence — the
// operator can see and raise this number, but cannot see an alert that never
// happened.
const DefaultSLOAlertMinSamples = 3

// SLOAlertPolicy is one built-in multiwindow burn-rate policy attached to one
// SLO.
//
// Google-SRE-style multiwindow alerting: the LONG window proves the burn is
// significant, the SHORT window proves it is still happening. An alert fires
// only when both exceed Threshold, which is precisely what stops a spike that
// ended forty minutes ago from paging for the rest of the hour.
//
// Thresholds and windows are columns rather than constants because the
// SRE-workbook numbers are a starting point, not a law: an org whose traffic
// shape makes 14.4x twitchy has to be able to retune it without a deploy.
type SLOAlertPolicy struct {
	// Named explicitly rather than left to the inflector: "SLOAlertPolicy" is
	// exactly the acronym-prefixed shape struct-name pluralization gets wrong.
	bun.BaseModel `bun:"table:slo_alert_policies,alias:slo_alert_policies"`

	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	SLOUID          string `bun:"slo_uid,notnull"`
	// Kind is the built-in identity, unique per SLO.
	Kind string `bun:"kind,notnull"`
	// Enabled defaults to false: upgrading to a version that has alerting must
	// never start paging on its own.
	Enabled bool `bun:"enabled,notnull"`
	// LongWindowSeconds / ShortWindowSeconds bound the two rolling windows the
	// evaluator measures, both ending at "now".
	LongWindowSeconds  int `bun:"long_window_seconds,notnull"`
	ShortWindowSeconds int `bun:"short_window_seconds,notnull"`
	// Threshold is the burn-rate multiple both windows must exceed. 1.0 spends
	// the calendar budget exactly by period end; 14.4 spends a 30-day budget in
	// about two hours.
	Threshold float64 `bun:"threshold,notnull"`
	Severity  string  `bun:"severity,notnull"`
	// MinSamples is the per-window probe floor. Below it the window is
	// INCONCLUSIVE: it does not fire, and it equally does not count as "below
	// threshold" for the resolution hysteresis.
	MinSamples int `bun:"min_samples,notnull"`
	// LastEvaluatedAt / LastLongBurnRate / LastShortBurnRate are the live
	// readout the dashboard's Alerting section renders. Stored so the UI and
	// the evaluator cannot disagree about what the burn rate was a minute ago.
	LastEvaluatedAt   *time.Time `bun:"last_evaluated_at"`
	LastLongBurnRate  *float64   `bun:"last_long_burn_rate"`
	LastShortBurnRate *float64   `bun:"last_short_burn_rate"`
	// BelowThresholdSince is the hysteresis anchor: the instant BOTH windows
	// first dropped below Threshold. Resolution waits until that has held for a
	// full short window; it is cleared the moment either window goes back over,
	// so a flapping burn never resolves.
	BelowThresholdSince *time.Time `bun:"below_threshold_since"`
	CreatedAt           time.Time  `bun:"created_at,notnull"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull"`
}

// LongWindow returns the long window as a duration.
func (p *SLOAlertPolicy) LongWindow() time.Duration {
	return time.Duration(p.LongWindowSeconds) * time.Second
}

// ShortWindow returns the short (confirmation) window as a duration.
func (p *SLOAlertPolicy) ShortWindow() time.Duration {
	return time.Duration(p.ShortWindowSeconds) * time.Second
}

// SLOAlertPolicyDefault describes one built-in policy's shipped configuration.
type SLOAlertPolicyDefault struct {
	Kind               string
	LongWindowSeconds  int
	ShortWindowSeconds int
	Threshold          float64
	Severity           string
}

// DefaultSLOAlertPolicies is the shipped pair, following the SRE-workbook
// 99.9% table: fast = 1h/5m at 14.4x (2% of a 30-day budget per hour), slow =
// 6h/30m at 6x.
func DefaultSLOAlertPolicies() []SLOAlertPolicyDefault {
	return []SLOAlertPolicyDefault{
		{
			Kind:               SLOAlertPolicyKindFast,
			LongWindowSeconds:  3600,
			ShortWindowSeconds: 300,
			Threshold:          14.4,
			Severity:           SLOAlertSeverityCritical,
		},
		{
			Kind:               SLOAlertPolicyKindSlow,
			LongWindowSeconds:  6 * 3600,
			ShortWindowSeconds: 1800,
			Threshold:          6,
			Severity:           SLOAlertSeverityWarning,
		},
	}
}

// NewSLOAlertPolicy builds a policy row from a built-in default.
func NewSLOAlertPolicy(orgUID, sloUID string, def SLOAlertPolicyDefault) *SLOAlertPolicy {
	now := time.Now()

	return &SLOAlertPolicy{
		UID:                uuid.New().String(),
		OrganizationUID:    orgUID,
		SLOUID:             sloUID,
		Kind:               def.Kind,
		Enabled:            false,
		LongWindowSeconds:  def.LongWindowSeconds,
		ShortWindowSeconds: def.ShortWindowSeconds,
		Threshold:          def.Threshold,
		Severity:           def.Severity,
		MinSamples:         DefaultSLOAlertMinSamples,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

// SLOAlertPolicyUpdate carries the fields a PATCH may change plus the
// evaluator's state writes. Nil means "leave alone".
type SLOAlertPolicyUpdate struct {
	Enabled            *bool
	LongWindowSeconds  *int
	ShortWindowSeconds *int
	Threshold          *float64
	Severity           *string
	MinSamples         *int

	LastEvaluatedAt   *time.Time
	LastLongBurnRate  *float64
	LastShortBurnRate *float64

	BelowThresholdSince *time.Time
	// ClearBelowThresholdSince sets the hysteresis anchor back to NULL — used
	// the instant either window climbs back over the threshold.
	ClearBelowThresholdSince bool
	// ClearLastBurnRates nulls the live readout when a window turns
	// inconclusive, so the UI shows "no data" rather than a stale number.
	ClearLastBurnRates bool
}
