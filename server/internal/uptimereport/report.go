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
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
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

// The JSON tags below are deliberately PascalCase, matching the Go field names
// and the `{{.OrgName}}` / `{{.HasData}}` references in uptime-report.html.
//
// This is NOT cosmetic. A report never reaches the template as this struct: it
// is stored in the email job's config, marshaled to JSON, persisted, and
// unmarshaled back into an `any` — so html/template sees a map[string]any and
// looks fields up BY THE JSON KEY. camelCase tags therefore make every
// `{{.OrgName}}` render as `<no value>` and every `{{if .HasData}}` evaluate
// false, which silently produces a blank "No data" digest rather than an error.
//
// PascalCase view-model keys are the house convention for every other template
// in the repo (auth, status subscribers, the notifier), which all pass plain
// map[string]any. This struct exists only to build that map with types; it is
// never a REST payload, so tagliatelle's camelCase rule — which guards the API
// surface — is exempted per-type below rather than repo-wide.
// TestUptimeReportRendersRealContent pins the round trip.

// CheckRow is one check's line in the report.
//
//nolint:tagliatelle // template view-model keys are PascalCase; see the note above.
type CheckRow struct {
	Name    string `json:"Name"`
	HasData bool   `json:"HasData"`
	// URL is the check's dash0 detail page, empty when the server has no
	// configured base URL — the template falls back to plain text.
	URL             string `json:"URL,omitempty"`
	AvailabilityPct string `json:"AvailabilityPct"`
	// AvailabilityColor is a hex color interpolated from AvailabilityPct
	// (see availabilityTextColor), filled only when HasData.
	AvailabilityColor string `json:"AvailabilityColor,omitempty"`
	// DownAllPeriod is set when the check measured data and EVERY probe
	// failed. The template states that as a fact rather than dressing it up as
	// a percentage.
	DownAllPeriod bool `json:"DownAllPeriod,omitempty"`
	// Days is the per-day availability strip, run-length encoded (see
	// DayCell). Empty when the strip could not be built.
	Days []DayCell `json:"Days,omitempty"`
}

// DayCell is one run of consecutive same-colored days in a check's strip.
//
// Run-length encoded rather than one cell per day: a healthy month collapses to
// a single cell, which is what keeps a 50-check monthly report far below
// Gmail's ~102 KB clipping limit (see TestUptimeReportStaysUnderGmailClipLimit).
//
//nolint:tagliatelle // template view-model keys are PascalCase; see the note above.
type DayCell struct {
	Color string `json:"Color"`
	Span  int    `json:"Span"`
	// Wide mirrors Span > 1 as a BOOL because the template must branch on it,
	// and html/template cannot compare a JSON-round-tripped float64 against an
	// integer literal ("incompatible types for comparison"). See the tag note
	// above: this view model reaches the template as a map, not as this struct.
	Wide bool `json:"Wide,omitempty"`
}

// SLORow is one objective's line in the report.
//
//nolint:tagliatelle // template view-model keys are PascalCase; see the note above.
type SLORow struct {
	Name    string `json:"Name"`
	HasData bool   `json:"HasData"`
	// URL is the objective's dash0 detail page, empty when the server has no
	// configured base URL — the template falls back to plain text. There is
	// deliberately no AvailabilityColor here: SLO rows already carry a
	// Healthy/At-risk/Breached badge driven by the objective's own target,
	// which is the right scale for an SLO (see Proposal item 3).
	URL             string `json:"URL,omitempty"`
	AttainmentPct   string `json:"AttainmentPct"`
	TargetPct       string `json:"TargetPct"`
	StateLabel      string `json:"StateLabel"`
	BudgetRemaining string `json:"BudgetRemaining"`
}

// Data is the template view model. Field names match uptime-report.html.
//
// It carries no recipient-specific value except UnsubscribeURL, which the
// caller fills per recipient — recipients are PII and must never leak into
// another recipient's copy.
//
//nolint:tagliatelle // template view-model keys are PascalCase; see the note above.
type Data struct {
	OrgName string `json:"OrgName"`
	// BrandName and OrgLogoURL are the branding keys base.html reads (see
	// email.ApplyOrgBranding). They live on the struct rather than being added
	// to the map after the fact because this view model IS a struct: the
	// report is marshaled into the email job's config and back.
	BrandName   string `json:"BrandName,omitempty"`
	OrgLogoURL  string `json:"OrgLogoURL,omitempty"`
	PeriodLabel string `json:"PeriodLabel"`
	ScopeLabel  string `json:"ScopeLabel"`
	Timezone    string `json:"Timezone"`

	HasData         bool   `json:"HasData"`
	AvailabilityPct string `json:"AvailabilityPct"`
	// AvailabilityColor is the headline metric's interpolated color, filled
	// only when HasData — see availabilityTextColor.
	AvailabilityColor string `json:"AvailabilityColor,omitempty"`
	CheckCount        int    `json:"CheckCount"`

	IncidentCount   int    `json:"IncidentCount"`
	LongestIncident string `json:"LongestIncident,omitempty"`
	// AverageIncident is TotalDowntime / IncidentCount, shown only when there
	// was at least one incident. There is deliberately no MTBF: at the 0-2
	// incidents a month that is the common case it is a degenerate statistic.
	AverageIncident string `json:"AverageIncident,omitempty"`
	TotalDowntime   string `json:"TotalDowntime,omitempty"`

	// --- Period-over-period trend -------------------------------------
	//
	// Every Show* flag is false when there is no previous-period data, and the
	// matching text is then empty: an absent baseline renders NOTHING, never
	// "±0.00%" and never in green (see applyTrend).
	PreviousPeriodLabel     string `json:"PreviousPeriodLabel,omitempty"`
	HasPreviousData         bool   `json:"HasPreviousData"`
	PreviousAvailabilityPct string `json:"PreviousAvailabilityPct,omitempty"`
	// PreviousIncidentCount carries NO omitempty on purpose. It is rendered
	// under ShowIncidentDelta, and the commonest healthy report is "0 incidents
	// last month, 0 this month" — with omitempty that zero drops out of the
	// marshaled map entirely and the template prints "<no value>" instead of
	// "0". Same reasoning as the PascalCase tag note above: the view model
	// reaches the template as a map, where a missing key is not a zero.
	PreviousIncidentCount   int    `json:"PreviousIncidentCount"`
	PreviousAvgResponseTime string `json:"PreviousAvgResponseTime,omitempty"`

	ShowAvailabilityDelta  bool   `json:"ShowAvailabilityDelta"`
	AvailabilityDeltaText  string `json:"AvailabilityDeltaText,omitempty"`
	AvailabilityDeltaColor string `json:"AvailabilityDeltaColor,omitempty"`

	ShowIncidentDelta  bool   `json:"ShowIncidentDelta"`
	IncidentDeltaText  string `json:"IncidentDeltaText,omitempty"`
	IncidentDeltaColor string `json:"IncidentDeltaColor,omitempty"`

	ShowResponseDelta  bool   `json:"ShowResponseDelta"`
	ResponseDeltaText  string `json:"ResponseDeltaText,omitempty"`
	ResponseDeltaColor string `json:"ResponseDeltaColor,omitempty"`

	// --- Response time -------------------------------------------------
	//
	// HasLatency is false — and the whole block absent — for a scope that was
	// down end to end or measured no duration at all.
	HasLatency      bool   `json:"HasLatency"`
	AvgResponseTime string `json:"AvgResponseTime,omitempty"`
	MinResponseTime string `json:"MinResponseTime,omitempty"`
	MaxResponseTime string `json:"MaxResponseTime,omitempty"`
	// SlowLine phrases the slow-probe count honestly across storage tiers —
	// exact "samples" from raw, "peaks" from rollups, never summed together.
	SlowLine string `json:"SlowLine,omitempty"`
	// SlowNote explains what a peak is, present only when peaks contribute.
	SlowNote string `json:"SlowNote,omitempty"`
	// LatencyNote states that failed samples are included in these numbers —
	// the rule uptimebar and the aggregation job both apply.
	LatencyNote string `json:"LatencyNote,omitempty"`

	// --- Degenerate-period statement ------------------------------------
	DownAllPeriod      bool   `json:"DownAllPeriod"`
	DownAllPeriodLabel string `json:"DownAllPeriodLabel,omitempty"`

	Checks []CheckRow `json:"Checks,omitempty"`
	// DayStripLabel names the span and the timezone the strip is bucketed in.
	DayStripLabel string `json:"DayStripLabel,omitempty"`
	// StripsShown is how many rows actually carry a day strip. It can be lower
	// than len(Checks) when the strip payload budget bit (see maxStripCells) —
	// in which case ShowStripBudgetNote is set and the template says so, rather
	// than leaving a reader to read a strip-less row as a broken email.
	StripsShown         int    `json:"StripsShown"`
	ShowStripBudgetNote bool   `json:"ShowStripBudgetNote"`
	StripBudgetNote     string `json:"StripBudgetNote,omitempty"`
	// Truncated and friends surface the maxCheckRows cap instead of silently
	// dropping rows. The table is sorted worst-first, so what survives the cap
	// is the lowest-uptime checks.
	Truncated      bool `json:"Truncated"`
	TruncatedShown int  `json:"TruncatedShown,omitempty"`
	TruncatedTotal int  `json:"TruncatedTotal,omitempty"`

	SLOs []SLORow `json:"SLOs,omitempty"`

	DashboardURL   string `json:"DashboardURL,omitempty"`
	DocsURL        string `json:"DocsURL,omitempty"`
	UnsubscribeURL string `json:"UnsubscribeURL,omitempty"`
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
		BrandName:   org.Name,
		OrgLogoURL:  orgLogoURL(org),
		PeriodLabel: periodLabel(schedule, window),
		ScopeLabel:  scopeLabel(schedule, len(checks)),
		Timezone:    schedule.Timezone,
		CheckCount:  len(checks),
	}

	baseURL := ""
	if b.cfg != nil {
		data.DashboardURL = b.cfg.Server.BaseURL
		baseURL = b.cfg.Server.BaseURL
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

	overall := b.applyCheckTable(ctx, data, org, checks, byCheck, checkUIDs, window, hints, baseURL)

	applyOverall(data, overall, checks)

	incidents, err := b.incidentBlock(ctx, org.UID, checkUIDs, window, now)
	if err != nil {
		return nil, err
	}

	applyIncidents(data, incidents)

	if trendErr := b.applyPreviousPeriod(
		ctx, org.UID, schedule, checkUIDs, window, now, hints, data, overall, incidents.Count,
	); trendErr != nil {
		return nil, trendErr
	}

	if schedule.IncludeSLOs {
		sloRows, sloErr := b.sloRows(ctx, org, baseURL, schedule, checkUIDs, window, now)
		if sloErr != nil {
			return nil, sloErr
		}

		data.SLOs = sloRows
	}

	return data, nil
}

// applyCheckTable builds the per-check rows (with their day strips), orders
// them worst-first, applies both caps, and returns the scope-level aggregate
// they sum to.
func (b *Builder) applyCheckTable(
	ctx context.Context, data *Data, org *models.Organization, checks []*models.Check,
	byCheck map[string]uptimebar.BucketStats, checkUIDs []string,
	window slo.Window, hints uptimebar.Hints, baseURL string,
) uptimebar.BucketStats {
	plan := planDayStrip(window)
	strips := b.dayStrips(ctx, org.UID, checkUIDs, plan, hints)

	overall, rows := buildCheckRows(checks, byCheck, strips, baseURL, org.Slug)

	sortWorstFirst(rows)

	if len(rows) > maxCheckRows {
		data.Truncated = true
		data.TruncatedShown = maxCheckRows
		data.TruncatedTotal = len(rows)
		rows = rows[:maxCheckRows]
	}

	stripsShown, stripsWanted := applyStripBudget(rows)

	data.Checks = rows
	data.StripsShown = stripsShown

	if stripsShown > 0 {
		data.DayStripLabel = plan.label()
	}

	// Say it when the strip budget bit, for the same reason the row cap says
	// "showing the 50 lowest-uptime checks of N": a row rendered without the
	// strip its neighbors have reads as a broken email, not as a deliberate
	// limit.
	if stripsWanted > stripsShown {
		data.ShowStripBudgetNote = true
		data.StripBudgetNote = stripBudgetNote(stripsShown)
	}

	return overall
}

// applyOverall fills the headline availability, the down-all-period statement
// and the response-time block from the scope-level aggregate.
func applyOverall(data *Data, overall uptimebar.BucketStats, checks []*models.Check) {
	if pct, ok := overall.AvailabilityPct(); ok {
		data.HasData = true
		data.AvailabilityPct = formatPct(pct)
		data.AvailabilityColor = availabilityTextColor(pct)
	}

	if isDownAllPeriod(overall) {
		data.DownAllPeriod = true
		data.DownAllPeriodLabel = downAllPeriodLabel(checks)
	}

	applyLatency(data, overall)
}

// applyIncidents fills the incident block. Each derived figure is shown only
// when it exists: an average of "0s" over zero incidents is not a fact.
func applyIncidents(data *Data, incidents availability.PeriodIncidents) {
	data.IncidentCount = incidents.Count

	if incidents.LongestSeconds > 0 {
		data.LongestIncident = formatDuration(incidents.LongestSeconds)
	}

	if incidents.AverageSeconds > 0 {
		data.AverageIncident = formatDuration(incidents.AverageSeconds)
	}

	if incidents.TotalDowntimeSeconds > 0 {
		data.TotalDowntime = formatDuration(incidents.TotalDowntimeSeconds)
	}
}

// buildCheckRows folds the per-check window stats into rows and, at the same
// time, into the scope-level total. Returning both from one pass is what keeps
// "the headline" and "the sum of the table" from ever disagreeing.
func buildCheckRows(
	checks []*models.Check, byCheck map[string]uptimebar.BucketStats,
	strips map[string][]DayCell, baseURL, orgSlug string,
) (uptimebar.BucketStats, []CheckRow) {
	var overall uptimebar.BucketStats

	rows := make([]CheckRow, 0, len(checks))

	for _, check := range checks {
		stats := byCheck[check.UID]
		overall.Add(stats)

		row := CheckRow{
			Name: checkDisplayName(check),
			URL:  checkReportURL(baseURL, orgSlug, check),
			Days: strips[check.UID],
		}

		if pct, ok := stats.AvailabilityPct(); ok {
			row.HasData = true
			row.AvailabilityPct = formatPct(pct)
			row.AvailabilityColor = availabilityTextColor(pct)
			row.DownAllPeriod = pct == 0
		}

		rows = append(rows, row)
	}

	return overall, rows
}

// sortWorstFirst orders the table by ascending availability, no-data checks
// last, ties broken by name.
//
// This is what makes the maxCheckRows cap safe: alphabetical order let a check
// that was down all month sit invisibly below the fold of a 50-row table, and
// the whole point of a digest is that the broken thing is the first thing you
// see. A no-data check is not "0%" — it goes last, not first.
func sortWorstFirst(rows []CheckRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]

		if left.HasData != right.HasData {
			return left.HasData
		}

		if left.HasData && left.AvailabilityPct != right.AvailabilityPct {
			// AvailabilityPct is formatPct's fixed-width 3-decimal rendering
			// of a percentage in [0, 100], so a numeric compare is needed:
			// "9.000" sorts after "10.000" as a string.
			return parsePct(left.AvailabilityPct) < parsePct(right.AvailabilityPct)
		}

		return left.Name < right.Name
	})
}

// parsePct reads back what formatPct wrote. A value that cannot be parsed
// sorts as the worst possible, so a formatting bug surfaces at the TOP of the
// table rather than hiding at the bottom.
func parsePct(formatted string) float64 {
	pct, err := strconv.ParseFloat(formatted, 64)
	if err != nil {
		return -1
	}

	return pct
}

// downAllPeriodLabel names the subject of the "was down for the entire period"
// statement: the check itself when the scope is a single check, and a plain
// collective phrase otherwise.
func downAllPeriodLabel(checks []*models.Check) string {
	if len(checks) == 1 {
		return checkDisplayName(checks[0])
	}

	return "Every monitored check"
}

// applyPreviousPeriod runs the same aggregation over the CALENDAR period before
// the reported one and fills the trend block.
//
// A failure to read the previous period is not fatal to the digest: the report
// is about the period that just closed, and losing the comparison must not lose
// the report. It logs and leaves every Show* flag false, which renders exactly
// as a first-ever run does — no delta at all.
func (b *Builder) applyPreviousPeriod(
	ctx context.Context, orgUID string, schedule *models.ReportSchedule, checkUIDs []string,
	window slo.Window, now time.Time, hints uptimebar.Hints,
	data *Data, current uptimebar.BucketStats, currentIncidents int,
) error {
	loc, err := slo.LoadLocation(schedule.Timezone)
	if err != nil {
		loc = time.UTC
	}

	previous := slo.PrecedingWindow(loc, window, schedule.Frequency == models.ReportFrequencyWeekly)
	data.PreviousPeriodLabel = periodLabel(schedule, previous)

	byCheck, err := uptimebar.WindowAvailability(
		ctx, b.db, orgUID, checkUIDs, previous.Start, previous.End, hints)
	if err != nil {
		slog.WarnContext(ctx, "uptime report could not read the previous period; "+
			"sending the digest without period-over-period deltas",
			"organization_uid", orgUID, "error", err)

		return nil
	}

	var previousOverall uptimebar.BucketStats

	for _, checkUID := range checkUIDs {
		previousOverall.Add(byCheck[checkUID])
	}

	previousIncidents, err := b.incidentBlock(ctx, orgUID, checkUIDs, previous, now)
	if err != nil {
		return err
	}

	applyTrend(data, &trendInputs{
		current:           current,
		previous:          previousOverall,
		currentIncidents:  currentIncidents,
		previousIncidents: previousIncidents.Count,
	})

	return nil
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
			// Check incidents only — the digest reports downtime, not budget
			// alerts (spec 2026-08-21-08).
			Kinds: []string{models.IncidentKindCheck},
			Since: &window.Start,
			Until: &window.End,
			Limit: incidentFetchLimit,
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
	ctx context.Context, org *models.Organization, baseURL string, schedule *models.ReportSchedule,
	checkUIDs []string, window slo.Window, now time.Time,
) ([]SLORow, error) {
	if b.slos == nil {
		return nil, nil
	}

	orgUID := org.UID

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
			URL:        sloReportURL(baseURL, org.Slug, objective),
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

// checkReportURL builds the dash0 detail-page URL for a check row, mirroring
// escalationCheckURL (job_escalation_step.go). Returns "" when any required
// component is missing, so the template falls back to plain text rather than
// emitting a half-built href.
func checkReportURL(baseURL, orgSlug string, check *models.Check) string {
	if baseURL == "" || orgSlug == "" || check == nil || check.UID == "" {
		return ""
	}

	return fmt.Sprintf("%s/dash0/orgs/%s/checks/%s", strings.TrimRight(baseURL, "/"), orgSlug, check.UID)
}

// sloReportURL builds the dash0 detail-page URL for an objective row. Returns
// "" when any required component is missing.
func sloReportURL(baseURL, orgSlug string, objective *models.SLO) string {
	if baseURL == "" || orgSlug == "" || objective == nil || objective.UID == "" {
		return ""
	}

	return fmt.Sprintf("%s/dash0/orgs/%s/slos/%s", strings.TrimRight(baseURL, "/"), orgSlug, objective.UID)
}

// availabilityTextColor interpolates a dark, text-safe hex color across the
// product's existing availability thresholds — NOT linearly over 0–100.
// Everything interesting in availability lives between 98 and 100, so the
// ramp is anchored on the same boundaries as the badge scale
// (badges/service.go's availabilityColor):
//
//	>= 99.9 (DefaultAvailabilityThresholdUp)        full green  #15803d
//	   99.0 (DefaultAvailabilityThresholdDegraded)  amber       #b45309
//	   98.0                                         orange (blend of red/amber)
//	<= 95.0                                         full red    #b91c1c
//
// Colors come from base.html's dark state-text family (green/amber/red), not
// the badge SVG fill palette (badges/svg.go's `#4c1`) — a fill color is
// unreadable as text on the email's white cells. The orange stop is not a
// fourth hardcoded color: it is a blend between amber and red, so the whole
// ramp is one continuous gradient anchored on exactly the three colors the
// rest of the product already uses for state text.
func availabilityTextColor(pct float64) string {
	const (
		green = "#15803d"
		amber = "#b45309"
		red   = "#b91c1c"

		orangeMidpoint = 0.5
		redFloor       = 95.0
	)

	orange := blendHexColor(red, amber, orangeMidpoint)

	switch {
	case pct >= models.DefaultAvailabilityThresholdUp:
		return green
	case pct >= models.DefaultAvailabilityThresholdDegraded:
		// [99.0, 99.9) blends amber -> green.
		degradedFloor := models.DefaultAvailabilityThresholdDegraded

		return blendHexColor(amber, green, fraction(pct, degradedFloor, models.DefaultAvailabilityThresholdUp))
	case pct >= 98.0:
		// [98.0, 99.0) blends orange -> amber.
		return blendHexColor(orange, amber, fraction(pct, 98.0, models.DefaultAvailabilityThresholdDegraded))
	case pct > redFloor:
		// (95.0, 98.0) blends red -> orange.
		return blendHexColor(red, orange, fraction(pct, redFloor, 98.0))
	default:
		return red
	}
}

// fraction returns how far pct sits between lo and hi, clamped to [0, 1].
func fraction(pct, lo, hi float64) float64 {
	if hi <= lo {
		return 0
	}

	frac := (pct - lo) / (hi - lo)
	if frac < 0 {
		return 0
	}

	if frac > 1 {
		return 1
	}

	return frac
}

// blendHexColor linearly interpolates between two "#rrggbb" colors at amount
// in [0, 1] (0 == from, 1 == to).
func blendHexColor(from, to string, amount float64) string {
	fromR, fromG, fromB := hexRGB(from)
	toR, toG, toB := hexRGB(to)

	red := lerpByte(fromR, toR, amount)
	green := lerpByte(fromG, toG, amount)
	blue := lerpByte(fromB, toB, amount)

	return fmt.Sprintf("#%02x%02x%02x", red, green, blue)
}

// hexRGB decodes a "#rrggbb" string into its three byte components. Callers
// pass only the fixed constants above, so a malformed string returns zeros
// rather than erroring.
func hexRGB(hex string) (byte, byte, byte) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0
	}

	var red, green, blue uint64

	_, err := fmt.Sscanf(hex[1:], "%02x%02x%02x", &red, &green, &blue)
	if err != nil {
		return 0, 0, 0
	}

	return byte(red), byte(green), byte(blue)
}

// lerpByte interpolates one byte channel at amount in [0, 1].
func lerpByte(from, to byte, amount float64) byte {
	return byte(math.Round(float64(from) + (float64(to)-float64(from))*amount))
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

// formatDuration renders a span for the report. A unit that lands on zero is
// dropped rather than printed: "42m 0s" and "1h 0m" are noise next to the
// figures they sit beside, and the digest is read, not parsed.
func formatDuration(seconds int64) string {
	span := time.Duration(seconds) * time.Second

	switch {
	case span >= 24*time.Hour:
		days, hours := int(span.Hours())/24, int(span.Hours())%24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}

		return fmt.Sprintf("%dd %dh", days, hours)
	case span >= time.Hour:
		hours, minutes := int(span.Hours()), int(span.Minutes())%60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}

		return fmt.Sprintf("%dh %dm", hours, minutes)
	case span >= time.Minute:
		minutes, remainder := int(span.Minutes()), int(span.Seconds())%60
		if remainder == 0 {
			return fmt.Sprintf("%dm", minutes)
		}

		return fmt.Sprintf("%dm %ds", minutes, remainder)
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

// orgLogoURL reads the org's logo, "" when it has none. base.html treats an
// empty value as "wear the SolidPing logo".
func orgLogoURL(org *models.Organization) string {
	if org == nil || org.LogoURL == nil {
		return ""
	}

	return *org.LogoURL
}
