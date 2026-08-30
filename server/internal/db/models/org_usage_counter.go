package models

import "github.com/uptrace/bun"

// Usage-counter kinds.
const (
	// UsageCounterKindSMS counts outbound SMS messages per month.
	UsageCounterKindSMS = "sms"
	// UsageCounterKindVoice counts outbound voice calls per month.
	UsageCounterKindVoice = "voice"
	// UsageCounterKindWhatsApp counts outbound WhatsApp template messages per
	// month.
	UsageCounterKindWhatsApp = "whatsapp"
	// UsageCounterKindCheckRateLimited counts check executions that were
	// deferred because the org exhausted its MaxChecksPerMinute budget. Unlike
	// the three kinds above it is a DAILY counter: PeriodStart is the UTC day,
	// because "were executions skipped today?" is the question the over-limit
	// banner answers, and a monthly bucket would keep the banner lit for weeks
	// after the org came back under its cap (spec 2026-08-26-03).
	UsageCounterKindCheckRateLimited = "check_rate_limited"
)

// OrgUsageCounter is a persistent per-org, per-kind, per-period counter backing
// the SMS/voice/WhatsApp monthly quotas and the daily rate-limited-skip tally.
// PeriodStart is an ISO date string ("2006-01-02") whose granularity is decided
// by the kind — the first day of the UTC month for the monthly quotas, the UTC
// day itself for UsageCounterKindCheckRateLimited. It is stored in a date
// column on PostgreSQL and text on SQLite.
type OrgUsageCounter struct {
	bun.BaseModel `bun:"table:org_usage_counters"`

	OrganizationUID string `bun:"organization_uid,pk"`
	Kind            string `bun:"kind,pk"`
	PeriodStart     string `bun:"period_start,pk"`
	Count           int    `bun:"count,notnull"`
}
