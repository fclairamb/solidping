// Package uptimereport builds the view model for the scheduled uptime-report
// email.
//
// It lives outside both the handler and the job so the "send me one now" test
// endpoint and the scheduled job render byte-identical reports: a test send
// that differs from the real thing is worse than no test send at all.
package uptimereport

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// TemplateName is the email template this package renders into.
const TemplateName = "uptime-report.html"

// incidentFetchLimit mirrors the availability API's per-window incident cap.
const incidentFetchLimit = 1000

// maxCheckRows bounds the per-check table so an org with thousands of checks
// does not produce an unsendable email.
const maxCheckRows = 50

// CheckRow is one check's line in the report.
type CheckRow struct {
	Name            string `json:"name"`
	HasData         bool   `json:"hasData"`
	AvailabilityPct string `json:"availabilityPct"`
}

// SLORow is one objective's line in the report.
type SLORow struct {
	Name            string `json:"name"`
	HasData         bool   `json:"hasData"`
	AttainmentPct   string `json:"attainmentPct"`
	TargetPct       string `json:"targetPct"`
	StateLabel      string `json:"stateLabel"`
	BudgetRemaining string `json:"budgetRemaining"`
}

// Data is the template view model. Field names match uptime-report.html.
//
// It carries no recipient-specific value except UnsubscribeURL, which the
// caller fills per recipient — recipients are PII and must never leak into
// another recipient's copy.
type Data struct {
	OrgName     string `json:"orgName"`
	PeriodLabel string `json:"periodLabel"`
	ScopeLabel  string `json:"scopeLabel"`
	Timezone    string `json:"timezone"`

	HasData         bool   `json:"hasData"`
	AvailabilityPct string `json:"availabilityPct"`
	CheckCount      int    `json:"checkCount"`

	IncidentCount   int    `json:"incidentCount"`
	LongestIncident string `json:"longestIncident,omitempty"`
	TotalDowntime   string `json:"totalDowntime,omitempty"`

	Checks []CheckRow `json:"checks,omitempty"`
	SLOs   []SLORow   `json:"slos,omitempty"`

	DashboardURL   string `json:"dashboardUrl,omitempty"`
	DocsURL        string `json:"docsUrl,omitempty"`
	UnsubscribeURL string `json:"unsubscribeUrl,omitempty"`
}

// Builder renders reports.
type Builder struct {
	db   db.Service
	cfg  *config.Config
	slos *slos.Service
}

// NewBuilder creates a report builder.
func NewBuilder(dbService db.Service, cfg *config.Config, sloSvc *slos.Service) *Builder {
	return &Builder{db: dbService, cfg: cfg, slos: sloSvc}
}

// Window resolves the period a schedule reports on for a given "now": the
// period that most recently CLOSED, in the schedule's own timezone.
func Window(schedule *models.ReportSchedule, now time.Time) (slo.Window, *time.Location) {
	loc, err := slo.LoadLocation(schedule.Timezone)
	if err != nil {
		loc = time.UTC
	}

	return slo.PreviousWindow(loc, now, schedule.Frequency == models.ReportFrequencyWeekly), loc
}

// Build assembles the report for one schedule over one window.
//
//nolint:funlen // a linear assembly of one view model.
func (b *Builder) Build(
	ctx context.Context, org *models.Organization, schedule *models.ReportSchedule,
	window slo.Window, now time.Time,
) (*Data, error) {
	checks, err := b.scopeChecks(ctx, org.UID, schedule)
	if err != nil {
		return nil, err
	}

	data := &Data{
		OrgName:     org.Name,
		PeriodLabel: periodLabel(schedule, window),
		ScopeLabel:  scopeLabel(schedule, len(checks)),
		Timezone:    schedule.Timezone,
		CheckCount:  len(checks),
	}

	if b.cfg != nil {
		data.DashboardURL = b.cfg.Server.BaseURL
	}

	if len(checks) == 0 {
		return data, nil
	}

	checkUIDs := make([]string, 0, len(checks))
	for _, check := range checks {
		checkUIDs = append(checkUIDs, check.UID)
	}

	rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, b.db, b.cfg)
	hints := uptimebar.Hints{
		RetentionRawHours: rawHours,
		RetentionHourDays: hourDays,
		RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, b.db, org.UID),
	}

	byCheck, err := uptimebar.WindowAvailability(ctx, b.db, org.UID, checkUIDs, window.Start, window.End, hints)
	if err != nil {
		return nil, fmt.Errorf("window availability: %w", err)
	}

	var overall uptimebar.BucketStats

	rows := make([]CheckRow, 0, len(checks))

	for _, check := range checks {
		stats := byCheck[check.UID]
		overall.Add(stats)

		row := CheckRow{Name: checkDisplayName(check)}
		if pct, ok := stats.AvailabilityPct(); ok {
			row.HasData = true
			row.AvailabilityPct = formatPct(pct)
		}

		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if len(rows) > maxCheckRows {
		rows = rows[:maxCheckRows]
	}

	data.Checks = rows

	if pct, ok := overall.AvailabilityPct(); ok {
		data.HasData = true
		data.AvailabilityPct = formatPct(pct)
	}

	incidents, err := b.incidentBlock(ctx, org.UID, checkUIDs, window, now)
	if err != nil {
		return nil, err
	}

	data.IncidentCount = incidents.Count
	if incidents.LongestSeconds > 0 {
		data.LongestIncident = formatDuration(incidents.LongestSeconds)
	}

	if incidents.TotalDowntimeSeconds > 0 {
		data.TotalDowntime = formatDuration(incidents.TotalDowntimeSeconds)
	}

	if schedule.IncludeSLOs {
		sloRows, sloErr := b.sloRows(ctx, org.UID, schedule, checkUIDs, window, now)
		if sloErr != nil {
			return nil, sloErr
		}

		data.SLOs = sloRows
	}

	return data, nil
}

// scopeChecks expands a schedule to the checks it covers. Both scope lists
// empty means org-wide.
func (b *Builder) scopeChecks(
	ctx context.Context, orgUID string, schedule *models.ReportSchedule,
) ([]*models.Check, error) {
	internal := "false"

	if schedule.IsOrgWide() {
		checks, _, err := b.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{Internal: &internal})
		if err != nil {
			return nil, fmt.Errorf("list org checks: %w", err)
		}

		return checks, nil
	}

	seen := make(map[string]struct{})
	out := make([]*models.Check, 0)

	for _, uid := range schedule.CheckUIDs {
		check, err := b.db.GetCheck(ctx, orgUID, uid)
		if err != nil {
			continue
		}

		if _, dup := seen[check.UID]; dup {
			continue
		}

		seen[check.UID] = struct{}{}

		out = append(out, check)
	}

	for _, groupUID := range schedule.CheckGroupUIDs {
		group := groupUID

		checks, _, err := b.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{
			CheckGroupUID: &group,
			Internal:      &internal,
		})
		if err != nil {
			return nil, fmt.Errorf("list group checks: %w", err)
		}

		for _, check := range checks {
			if _, dup := seen[check.UID]; dup {
				continue
			}

			seen[check.UID] = struct{}{}

			out = append(out, check)
		}
	}

	return out, nil
}

func (b *Builder) incidentBlock(
	ctx context.Context, orgUID string, checkUIDs []string, window slo.Window, now time.Time,
) (availability.PeriodIncidents, error) {
	seen := make(map[string]struct{})
	all := make([]*models.Incident, 0)

	for _, checkUID := range checkUIDs {
		filter := &models.ListIncidentsFilter{
			OrganizationUID: orgUID,
			MemberCheckUID:  checkUID,
			Since:           &window.Start,
			Until:           &window.End,
			Limit:           incidentFetchLimit,
		}

		incidents, _, err := b.db.ListIncidents(ctx, filter)
		if err != nil {
			return availability.PeriodIncidents{}, fmt.Errorf("list incidents: %w", err)
		}

		for _, inc := range incidents {
			if _, dup := seen[inc.UID]; dup {
				continue
			}

			seen[inc.UID] = struct{}{}

			all = append(all, inc)
		}
	}

	return availability.IncidentBlock(all, window.Start.UTC(), window.End.UTC(), now.UTC()), nil
}

// sloRows evaluates every enabled objective whose scope intersects the report's.
func (b *Builder) sloRows(
	ctx context.Context, orgUID string, schedule *models.ReportSchedule,
	checkUIDs []string, window slo.Window, now time.Time,
) ([]SLORow, error) {
	if b.slos == nil {
		return nil, nil
	}

	objectives, err := b.db.ListSLOs(ctx, orgUID, models.ListSLOsFilter{EnabledOnly: true})
	if err != nil {
		return nil, fmt.Errorf("list slos: %w", err)
	}

	inScope := make(map[string]struct{}, len(checkUIDs))
	for _, uid := range checkUIDs {
		inScope[uid] = struct{}{}
	}

	rows := make([]SLORow, 0, len(objectives))

	for _, objective := range objectives {
		if !schedule.IsOrgWide() {
			members, scopeErr := b.slos.ScopeCheckUIDs(ctx, orgUID, objective)
			if scopeErr != nil {
				return nil, scopeErr
			}

			if !intersects(members, inScope) {
				continue
			}
		}

		status, evalErr := b.slos.EvaluateWindow(ctx, orgUID, objective, window, now)
		if evalErr != nil {
			return nil, evalErr
		}

		row := SLORow{
			Name:       objective.Name,
			HasData:    status.HasData,
			TargetPct:  formatPct(status.TargetPct),
			StateLabel: stateLabel(status.State),
		}

		if status.AttainmentPct != nil {
			row.AttainmentPct = formatPct(*status.AttainmentPct)
		}

		if status.HasData {
			row.BudgetRemaining = formatSignedDuration(status.BudgetRemainingSeconds)
		}

		rows = append(rows, row)
	}

	return rows, nil
}

func intersects(members []string, inScope map[string]struct{}) bool {
	for _, uid := range members {
		if _, ok := inScope[uid]; ok {
			return true
		}
	}

	return false
}

func checkDisplayName(check *models.Check) string {
	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	if check.Slug != nil {
		return *check.Slug
	}

	return check.UID
}

func periodLabel(schedule *models.ReportSchedule, window slo.Window) string {
	if schedule.Frequency == models.ReportFrequencyWeekly {
		return fmt.Sprintf(
			"%s – %s",
			window.Start.Format("2 Jan 2006"),
			window.End.Add(-time.Second).Format("2 Jan 2006"),
		)
	}

	return window.Start.Format("January 2006")
}

func scopeLabel(schedule *models.ReportSchedule, checkCount int) string {
	if schedule.IsOrgWide() {
		return fmt.Sprintf("All checks (%d)", checkCount)
	}

	return fmt.Sprintf("%s (%d checks)", schedule.Name, checkCount)
}

func stateLabel(state string) string {
	switch state {
	case slo.StateHealthy:
		return "Healthy"
	case slo.StateAtRisk:
		return "At risk"
	case slo.StateBreached:
		return "Breached"
	default:
		return "No data"
	}
}

func formatPct(pct float64) string {
	return fmt.Sprintf("%.3f", math.Round(pct*1000)/1000)
}

func formatDuration(seconds int64) string {
	span := time.Duration(seconds) * time.Second

	switch {
	case span >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(span.Hours())/24, int(span.Hours())%24)
	case span >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(span.Hours()), int(span.Minutes())%60)
	case span >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(span.Minutes()), int(span.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// formatSignedDuration renders a possibly-negative budget. A breached budget
// must read as an overspend ("-12m 30s"), never silently clamp to zero.
func formatSignedDuration(seconds int64) string {
	if seconds < 0 {
		return "-" + formatDuration(-seconds)
	}

	return formatDuration(seconds)
}
