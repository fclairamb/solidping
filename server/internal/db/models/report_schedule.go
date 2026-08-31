package models

import (
	"time"

	"github.com/google/uuid"
)

// Report schedule frequencies.
const (
	// ReportFrequencyWeekly emits one report per ISO week (Monday start).
	ReportFrequencyWeekly = "weekly"
	// ReportFrequencyMonthly emits one report per calendar month.
	ReportFrequencyMonthly = "monthly"
)

// DefaultReportTimezone is the timezone a new schedule gets when none is given.
const DefaultReportTimezone = "UTC"

// IsValidReportFrequency reports whether a frequency string is accepted.
func IsValidReportFrequency(frequency string) bool {
	return frequency == ReportFrequencyWeekly || frequency == ReportFrequencyMonthly
}

// ReportSchedule is a recurring uptime-report digest.
//
// It is deliberately NOT a column on SLO: a digest is useful for checks that
// carry no formal objective at all, and an SLO is useful without anybody being
// emailed about it.
type ReportSchedule struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	Name            string `bun:"name,notnull"`
	Frequency       string `bun:"frequency,notnull"`
	Timezone        string `bun:"timezone,notnull"`
	// Recipients is PII, held to the same bar as status-page subscriber
	// addresses: never logged, never emitted into events, only ever read back
	// to the org's own admins.
	Recipients []string `bun:"recipients,type:jsonb,nullzero"`
	// CheckUIDs / CheckGroupUIDs scope the digest. Both empty means org-wide.
	CheckUIDs      []string `bun:"check_uids,type:jsonb,nullzero"`
	CheckGroupUIDs []string `bun:"check_group_uids,type:jsonb,nullzero"`
	IncludeSLOs    bool     `bun:"include_slos,notnull"`
	Enabled        bool     `bun:"enabled,notnull"`
	// LastPeriodStart is the UTC start of the last period actually reported.
	// It is the duplicate-run suppression key: a second job fire for the same
	// closed period is a no-op, which is what makes multi-replica scheduling
	// safe without leader election.
	LastPeriodStart *time.Time `bun:"last_period_start"`
	LastRunAt       *time.Time `bun:"last_run_at"`
	CreatedAt       time.Time  `bun:"created_at,notnull"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// IsOrgWide reports whether the schedule covers every check in the org.
func (r *ReportSchedule) IsOrgWide() bool {
	return len(r.CheckUIDs) == 0 && len(r.CheckGroupUIDs) == 0
}

// NewReportSchedule builds a report schedule with a generated UID and defaults.
func NewReportSchedule(orgUID, name, frequency string) *ReportSchedule {
	now := time.Now()

	if !IsValidReportFrequency(frequency) {
		frequency = ReportFrequencyMonthly
	}

	return &ReportSchedule{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Name:            name,
		Frequency:       frequency,
		Timezone:        DefaultReportTimezone,
		IncludeSLOs:     true,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// ReportScheduleUpdate carries the fields a PATCH may change. Nil = leave alone.
type ReportScheduleUpdate struct {
	Name           *string
	Frequency      *string
	Timezone       *string
	Recipients     *[]string
	CheckUIDs      *[]string
	CheckGroupUIDs *[]string
	IncludeSLOs    *bool
	Enabled        *bool
}
