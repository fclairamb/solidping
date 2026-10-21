package notifications

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
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

	info.PolicyLabel = info.policyLabelIn(i18n.English)
	info.ProjectedExhaustion = jsonTime(details[burnKeyProjectedExhaustion])

	return info
}

// policyLabelIn is the policy's human name, "Fast burn" / "Slow burn", in the
// reader's language. PolicyLabel holds the English one.
func (b *BurnInfo) policyLabelIn(loc i18n.Locale) string {
	if b.PolicyKind == models.SLOAlertPolicyKindFast {
		return loc.T("incident.burn.policy.fast")
	}

	return loc.T("incident.burn.policy.slow")
}

// severityLabelIn names the policy's severity ("critical", "warning") in the
// reader's language. An unknown value is shown as stored.
func (b *BurnInfo) severityLabelIn(loc i18n.Locale) string {
	switch b.Severity {
	case models.SLOAlertSeverityCritical, models.SLOAlertSeverityWarning:
		return loc.T("incident.burn.severity." + b.Severity)
	default:
		return b.Severity
	}
}

// RateText renders a burn rate the way an operator reads it.
func (b *BurnInfo) RateText() string {
	return multiplierIn(i18n.English, b.LongRate)
}

// ShortRateText renders the confirmation window's rate.
func (b *BurnInfo) ShortRateText() string {
	return multiplierIn(i18n.English, b.ShortRate)
}

// ThresholdText renders the configured threshold.
func (b *BurnInfo) ThresholdText() string {
	return multiplierIn(i18n.English, b.Threshold)
}

// BudgetRemainingText renders the remaining error budget as a duration, or
// "exhausted" once it has gone negative. Never a bare negative number: "-3h12m
// remaining" is a sentence nobody should have to parse at 3am.
func (b *BurnInfo) BudgetRemainingText() string {
	return b.budgetRemainingTextIn(i18n.English)
}

// budgetRemainingTextIn is BudgetRemainingText in the reader's language.
func (b *BurnInfo) budgetRemainingTextIn(loc i18n.Locale) string {
	if b.BudgetRemaining <= 0 {
		return loc.T("incident.burn.exhausted")
	}

	return humanDurationIn(loc, b.BudgetRemaining)
}

// ProjectedExhaustionText renders when the budget runs out, or a plain
// statement that it is not projected to inside this window. Always UTC and
// never with a year: the format chat channels have always shown.
func (b *BurnInfo) ProjectedExhaustionText() string {
	if b.ProjectedExhaustion.IsZero() {
		return b.projectedExhaustionTextIn(i18n.English)
	}

	return b.ProjectedExhaustion.UTC().Format("Jan 2, 15:04 UTC")
}

// projectedExhaustionTextIn is ProjectedExhaustionText for a mail: in the
// reader's language and timezone, with the year when it is not this one. Same
// English as ProjectedExhaustionText within the current year.
func (b *BurnInfo) projectedExhaustionTextIn(loc i18n.Locale) string {
	if b.ProjectedExhaustion.IsZero() {
		return loc.T("incident.burn.notWithinWindow")
	}

	return loc.Date(b.ProjectedExhaustion)
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
		"Error budget remaining: " + b.BudgetRemainingText(),
		"Projected exhaustion: " + b.ProjectedExhaustionText(),
	}
}

// humanDuration renders a duration in the coarsest unit that stays honest:
// "5m", "1h30m", "1d2h".
func humanDuration(duration time.Duration) string {
	return humanDurationIn(i18n.English, duration)
}

// humanDurationIn is humanDuration in the reader's language ("1 h 30 min").
func humanDurationIn(loc i18n.Locale, duration time.Duration) string {
	const key = "incident.burn.duration."

	if duration <= 0 {
		return loc.T(key+"minutes", "n", 0)
	}

	switch {
	case duration < time.Hour:
		return loc.T(key+"minutes", "n", int(math.Round(duration.Minutes())))
	case duration < 24*time.Hour:
		hours := int(duration / time.Hour)
		minutes := int((duration % time.Hour) / time.Minute)

		if minutes == 0 {
			return loc.T(key+"hours", "n", hours)
		}

		return loc.T(key+"hoursMinutes", "hours", hours, "minutes", minutes)
	default:
		days := int(duration / (24 * time.Hour))
		hours := int((duration % (24 * time.Hour)) / time.Hour)

		if hours == 0 {
			return loc.T(key+"days", "n", days)
		}

		return loc.T(key+"daysHours", "days", days, "hours", hours)
	}
}

// multiplierIn renders a burn rate or threshold: "14.4x", "14,4x" in French.
func multiplierIn(loc i18n.Locale, rate float64) string {
	return oneDecimalIn(loc, rate) + "x"
}

// oneDecimalIn renders value like %.1f (same rounding, no thousands
// separator) with the reader's decimal separator: "93.8" in English, "93,8"
// in French. Shared with channels that keep their English output, which a
// grouped or differently rounded number would change.
func oneDecimalIn(loc i18n.Locale, value float64) string {
	text := strconv.FormatFloat(value, 'f', 1, 64)

	if sep := strings.TrimFunc(loc.Number(1.5, 1), unicode.IsDigit); sep != "." {
		text = strings.Replace(text, ".", sep, 1)
	}

	return text
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
