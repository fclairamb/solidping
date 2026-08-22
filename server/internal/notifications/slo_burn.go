package notifications

import (
	"fmt"
	"math"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Detail keys written by the burn evaluator (handlers/incidents/slo_burn.go).
// Duplicated as literals rather than imported so this package keeps not
// depending on handlers/*.
const (
	burnKeySLOName             = "slo_name"
	burnKeyPolicyKind          = "slo_alert_policy_kind"
	burnKeySeverity            = "severity"
	burnKeyThreshold           = "burn_threshold"
	burnKeyRateLong            = "burn_rate_long"
	burnKeyRateShort           = "burn_rate_short"
	burnKeyPeak                = "burn_peak"
	burnKeyLongWindowSeconds   = "long_window_seconds"
	burnKeyShortWindowSeconds  = "short_window_seconds"
	burnKeyBudgetRemainingSecs = "budget_remaining_seconds"
	burnKeyProjectedExhaustion = "projected_exhaustion_at"
	burnKeyTargetPct           = "target_pct"
)

// BurnInfo is the burn-specific half of a notification: the three numbers an
// operator needs before they can decide anything — how fast the budget is
// burning, how much is left, and when it runs out.
//
// A page that says only "your error budget is burning" forces the reader back
// to the dashboard before they can act, which is exactly what paging is
// supposed to avoid.
type BurnInfo struct {
	SLOName    string
	PolicyKind string
	Severity   string
	// PolicyLabel is the human name — "Fast burn" / "Slow burn".
	PolicyLabel string
	Threshold   float64
	// LongRate / ShortRate are the two windows' burn rates. Both had to exceed
	// Threshold for this to have fired at all.
	LongRate  float64
	ShortRate float64
	PeakRate  float64
	// LongWindow / ShortWindow describe which windows those rates cover, so the
	// numbers are interpretable without opening the policy.
	LongWindow  time.Duration
	ShortWindow time.Duration
	TargetPct   float64

	BudgetRemaining time.Duration
	// ProjectedExhaustion is when the remaining budget runs out at the current
	// pace. Zero means "not projected to run out inside this window".
	ProjectedExhaustion time.Time
}

// BurnInfoFor extracts the burn payload from an incident, or nil when the
// incident is not a burn alert.
func BurnInfoFor(incident *models.Incident) *BurnInfo {
	if incident == nil || incident.Kind != models.IncidentKindSLOBurn || incident.Details == nil {
		return nil
	}

	details := incident.Details

	info := &BurnInfo{
		SLOName:         jsonString(details[burnKeySLOName]),
		PolicyKind:      jsonString(details[burnKeyPolicyKind]),
		Severity:        jsonString(details[burnKeySeverity]),
		Threshold:       jsonFloat(details[burnKeyThreshold]),
		LongRate:        jsonFloat(details[burnKeyRateLong]),
		ShortRate:       jsonFloat(details[burnKeyRateShort]),
		PeakRate:        jsonFloat(details[burnKeyPeak]),
		TargetPct:       jsonFloat(details[burnKeyTargetPct]),
		LongWindow:      time.Duration(jsonFloat(details[burnKeyLongWindowSeconds])) * time.Second,
		ShortWindow:     time.Duration(jsonFloat(details[burnKeyShortWindowSeconds])) * time.Second,
		BudgetRemaining: time.Duration(jsonFloat(details[burnKeyBudgetRemainingSecs])) * time.Second,
	}

	info.PolicyLabel = "Slow burn"
	if info.PolicyKind == models.SLOAlertPolicyKindFast {
		info.PolicyLabel = "Fast burn"
	}

	info.ProjectedExhaustion = jsonTime(details[burnKeyProjectedExhaustion])

	return info
}

// RateText renders a burn rate the way an operator reads it.
func (b *BurnInfo) RateText() string {
	return fmt.Sprintf("%.1fx", b.LongRate)
}

// ShortRateText renders the confirmation window's rate.
func (b *BurnInfo) ShortRateText() string {
	return fmt.Sprintf("%.1fx", b.ShortRate)
}

// ThresholdText renders the configured threshold.
func (b *BurnInfo) ThresholdText() string {
	return fmt.Sprintf("%.1fx", b.Threshold)
}

// BudgetRemainingText renders the remaining error budget as a duration, or
// "exhausted" once it has gone negative. Never a bare negative number: "-3h12m
// remaining" is a sentence nobody should have to parse at 3am.
func (b *BurnInfo) BudgetRemainingText() string {
	if b.BudgetRemaining <= 0 {
		return "exhausted"
	}

	return humanDuration(b.BudgetRemaining)
}

// ProjectedExhaustionText renders when the budget runs out, or a plain
// statement that it is not projected to inside this window.
func (b *BurnInfo) ProjectedExhaustionText() string {
	if b.ProjectedExhaustion.IsZero() {
		return "not within this window"
	}

	return b.ProjectedExhaustion.UTC().Format("2006-01-02 15:04:05 UTC")
}

// WindowText names the two windows the rates cover.
func (b *BurnInfo) WindowText() string {
	return fmt.Sprintf("%s / %s", humanDuration(b.LongWindow), humanDuration(b.ShortWindow))
}

// SummaryLines is the shared three-line body every non-templated channel
// appends, so a Slack message, a Discord embed and an SMS all say the same
// three things in the same order.
func (b *BurnInfo) SummaryLines() []string {
	return []string{
		fmt.Sprintf("Burn rate: %s over %s (%s over %s), threshold %s",
			b.RateText(), humanDuration(b.LongWindow),
			b.ShortRateText(), humanDuration(b.ShortWindow), b.ThresholdText()),
		fmt.Sprintf("Error budget remaining: %s", b.BudgetRemainingText()),
		fmt.Sprintf("Projected exhaustion: %s", b.ProjectedExhaustionText()),
	}
}

// humanDuration renders a duration in the coarsest unit that stays honest.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}

	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(math.Round(d.Minutes())))
	case d < 24*time.Hour:
		hours := int(d / time.Hour)
		minutes := int((d % time.Hour) / time.Minute)

		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}

		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)

		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}

		return fmt.Sprintf("%dd%dh", days, hours)
	}
}

// jsonString / jsonFloat / jsonTime read a JSONB detail value defensively: the
// same map arrives either straight from the evaluator (native Go types) or
// round-tripped through the database (everything numeric is float64, every
// time is a string).
func jsonString(value any) string {
	text, _ := value.(string)

	return text
}

func jsonFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	default:
		return 0
	}
}

func jsonTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed
			}
		}
	}

	return time.Time{}
}
