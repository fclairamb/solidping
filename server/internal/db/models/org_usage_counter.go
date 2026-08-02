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
)

// OrgUsageCounter is a persistent per-org, per-kind, per-month counter backing
// the SMS/voice monthly quotas. PeriodStart is the first day of the UTC month
// as an ISO date string ("2006-01-02"), stored in a date column on PostgreSQL
// and text on SQLite.
type OrgUsageCounter struct {
	bun.BaseModel `bun:"table:org_usage_counters"`

	OrganizationUID string `bun:"organization_uid,pk"`
	Kind            string `bun:"kind,pk"`
	PeriodStart     string `bun:"period_start,pk"`
	Count           int    `bun:"count,notnull,default:0"`
}
