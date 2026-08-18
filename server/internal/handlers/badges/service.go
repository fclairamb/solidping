package badges

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Badge service errors.
var (
	ErrCheckNotFound        = errors.New("check not found")
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrInvalidFormat        = errors.New("invalid badge format")
)

// Component token constants.
const (
	componentStatus            = "status"
	componentAvailability      = "availability"
	componentDuration          = "duration"
	componentResponseTime      = "response-time"
	componentUptimeBar         = "uptime-bar"
	componentResponseTimeGraph = "response-time-graph"
	statusUp                   = "up"
	statusDown                 = "down"
	statusUnknown              = "unknown"
)

// defaultBadgeWidth is the combined image width used when a bar or graph row is
// present and no explicit width is requested.
const defaultBadgeWidth = 300

// BadgeOptions contains options for badge generation.
type BadgeOptions struct {
	Period   string // "24h", "7d", "30d", "90d"
	Label    string // Custom label (default: check name)
	Style    string // "flat", "flat-square"
	MinWidth int    // 0 = no minimum
	Width    int    // combined width for bar/graph rows; 0 = use default
}

// Service provides badge generation functionality.
type Service struct {
	dbSvc db.Service
	cfg   *config.Config
}

// NewService creates a new badge service. cfg may be nil (e.g. surfaces that
// don't have an app config to hand, like the MCP handler) — the uptime-bar
// query's safety cap then falls back to the documented retention defaults
// instead of the org's actual configured values.
func NewService(dbSvc db.Service, cfg *config.Config) *Service {
	return &Service{dbSvc: dbSvc, cfg: cfg}
}

// uptimebarHints resolves everything uptimebar needs to bound its queries, ONCE
// per request: the live raw/hour aggregation retention and the org's measured
// probe rate.
//
// Retention is resolved with the same precedence as the aggregation job itself —
// env > performance.* global parameter > legacy koanf field > documented default
// (systemconfig.ResolveReadSideRetention). It must NOT read the koanf config
// alone: the server "Aggregation" settings tab writes the performance.*
// parameters, which never reach the koanf struct, so a stale hint would make
// uptimebar clamp its raw-tier query shorter than the window the job actually
// keeps raw for — silently dropping raw rows no rollup covers yet.
func (s *Service) uptimebarHints(ctx context.Context, orgUID string) uptimebar.Hints {
	rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, s.dbSvc, s.cfg)

	return uptimebar.Hints{
		RetentionRawHours: rawHours,
		RetentionHourDays: hourDays,
		RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, s.dbSvc, orgUID),
	}
}

func isAllowedComponent(token string) bool {
	switch token {
	case componentStatus, componentAvailability, componentDuration, componentResponseTime,
		componentUptimeBar, componentResponseTimeGraph:
		return true
	default:
		return false
	}
}

// isTextToken reports whether a token renders as text in row 1.
func isTextToken(token string) bool {
	switch token {
	case componentStatus, componentAvailability, componentDuration, componentResponseTime:
		return true
	default:
		return false
	}
}

// parseComponents validates and returns the ordered list of component tokens.
// Every token is optional; the only requirements are that the path segment is
// non-empty and that each token is known and unique.
func parseComponents(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		token := strings.TrimSpace(part)
		if !isAllowedComponent(token) {
			return nil, ErrInvalidFormat
		}

		if seen[token] {
			return nil, ErrInvalidFormat
		}

		seen[token] = true
		result = append(result, token)
	}

	if len(result) == 0 {
		return nil, ErrInvalidFormat
	}

	return result, nil
}

// GenerateBadge generates a composable multi-row SVG badge for a check.
func (s *Service) GenerateBadge(
	ctx context.Context, orgSlug, checkIdentifier, components string, opts BadgeOptions,
) (string, error) {
	// 1. Parse components first — fast validation before DB access.
	tokens, err := parseComponents(components)
	if err != nil {
		return "", err
	}

	// 2. Resolve organization.
	org, err := s.dbSvc.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return "", ErrOrganizationNotFound
	}

	// 3. Resolve check by UID or slug (auto-detected).
	check, err := s.dbSvc.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return "", ErrCheckNotFound
	}

	// 4. Set defaults.
	opts = s.applyDefaults(opts, check)

	// 5. Split tokens into row-1 text tokens and bar/graph rows.
	textTokens, hasBar, hasGraph := splitTokens(tokens)

	// 6. Fetch row-1 data.
	results, err := s.fetchResults(ctx, org.UID, check.UID, textTokens, opts.Period)
	if err != nil {
		return "", err
	}

	// 7. Determine combined width (0 → row-1 natural width, resolved below).
	width := opts.MinWidth
	if hasBar || hasGraph {
		width = opts.Width
		if width <= 0 {
			width = defaultBadgeWidth
		}
	}

	// 8. Render row 1.
	value := s.composeValue(textTokens, results)
	color := resolveColor(textTokens, results)
	rows := []string{renderBadgeRow(opts.Label, value, color, opts.Style, width, 0)}

	totalHeight := rowHeightText

	// 9. Fetch bucket data and append bar/graph rows.
	if hasBar || hasGraph {
		rows, totalHeight, err = s.appendRowFragments(
			ctx, org.UID, check.UID, opts, width, hasBar, hasGraph, rows, totalHeight,
		)
		if err != nil {
			return "", err
		}
	}

	// 10. Resolve final width to row-1 natural width for text-only badges.
	if width <= 0 {
		_, _, width = textWidths(opts.Label, value, opts.MinWidth)
	}

	return ComposeBadgeSVG(rows, width, totalHeight), nil
}

// splitTokens partitions parsed tokens into the row-1 text tokens and the
// presence of the uptime-bar and response-time-graph rows. Returns
// (textTokens, hasBar, hasGraph).
func splitTokens(tokens []string) ([]string, bool, bool) {
	textTokens := make([]string, 0, len(tokens))

	hasBar := false
	hasGraph := false

	for _, token := range tokens {
		switch token {
		case componentUptimeBar:
			hasBar = true
		case componentResponseTimeGraph:
			hasGraph = true
		default:
			if isTextToken(token) {
				textTokens = append(textTokens, token)
			}
		}
	}

	return textTokens, hasBar, hasGraph
}

// appendRowFragments fetches the shared bucket data and appends the bar and
// graph row fragments. It returns the updated rows slice and total height.
func (s *Service) appendRowFragments(
	ctx context.Context, orgUID, checkUID string, opts BadgeOptions,
	width int, hasBar, hasGraph bool, rows []string, totalHeight int,
) ([]string, int, error) {
	availMap, durationMap, win, err := s.fetchBucketData(
		ctx, orgUID, checkUID, opts.Period,
	)
	if err != nil {
		return rows, totalHeight, err
	}

	if hasBar {
		segments := buildBarSegments(availMap, win.bucketStart, win.n, win.bucketDuration)
		labels := computeUptimeBarLabels(win.bucketStart, win.n, win.bucketDuration)
		barValues := computeUptimeBarValues(availMap, win.bucketStart, win.n, win.bucketDuration, width)
		yOffset := totalHeight + rowGap
		rows = append(rows, renderUptimeBarRow(segments, labels, barValues, width, rowHeightBar, yOffset, opts.Style))
		totalHeight = yOffset + rowHeightBar
	}

	if hasGraph {
		points := buildGraphPoints(durationMap, win.bucketStart, win.n, win.bucketDuration)
		yOffset := totalHeight + rowGap
		rows = append(rows, renderResponseTimeGraphRow(points, width, rowHeightGraph, yOffset, opts.Style))
		totalHeight = yOffset + rowHeightGraph
	}

	return rows, totalHeight, nil
}

// barWindow is the time anchor of an uptime-bar render: the n buckets of
// bucketDuration starting at bucketStart (oldest), the newest being the current,
// in-progress bucket.
type barWindow struct {
	bucketStart    time.Time
	n              int
	bucketDuration time.Duration
}

// bucketStatsForPeriod runs the shared uptimebar bucketing for one check over the
// uptime-bar window for the given period and returns the per-bucket stats plus
// the window anchor. This is the single seam the badge buckets from; the status
// page buckets from the same uptimebar helper, so the two surfaces cannot diverge
// on which data feeds the bar.
//
// The window spans n buckets ending at the current, in-progress bucket, so a
// freshly-created check shows its data immediately: the current bucket is filled
// from raw rows until the hourly rollup runs. Warning rows count as up
// (CountsAsUp), matching the rolled-up tier and the aggregation job.
func (s *Service) bucketStatsForPeriod(
	ctx context.Context, orgUID, checkUID, period string,
) (map[time.Time]uptimebar.BucketStats, barWindow, error) {
	_, n, bucketDuration := uptimeBarPeriodInfo(period)

	now := time.Now().UTC()
	// Anchor on the current bucket: the last of the n segments is now.Truncate,
	// the oldest is (n-1) buckets earlier. Using -(n-1) rather than -n keeps the
	// in-progress bucket inside the rendered window.
	bucketStart := now.Truncate(bucketDuration).Add(-time.Duration(n-1) * bucketDuration)
	win := barWindow{bucketStart: bucketStart, n: n, bucketDuration: bucketDuration}

	hints := s.uptimebarHints(ctx, orgUID)

	byCheck, err := uptimebar.BucketAvailability(
		ctx, s.dbSvc, orgUID, []string{checkUID}, bucketDuration, bucketStart, n,
		hints,
	)
	if err != nil {
		return nil, barWindow{}, err
	}

	return byCheck[checkUID], win, nil
}

// fetchBucketData returns per-bucket availability and average-duration maps for
// the uptime bar plus the window anchor, projected from the shared per-bucket
// stats.
func (s *Service) fetchBucketData(
	ctx context.Context, orgUID, checkUID, period string,
) (map[time.Time]float64, map[time.Time]*float64, barWindow, error) {
	byBucket, win, err := s.bucketStatsForPeriod(ctx, orgUID, checkUID, period)
	if err != nil {
		return nil, nil, barWindow{}, err
	}

	availMap := make(map[time.Time]float64, win.n)
	durationMap := make(map[time.Time]*float64, win.n)

	for bucket := range byBucket {
		stats := byBucket[bucket]

		if pct, ok := stats.AvailabilityPct(); ok {
			availMap[bucket] = pct
		}

		if dur, ok := stats.AvgDuration(); ok {
			v := dur
			durationMap[bucket] = &v
		}
	}

	return availMap, durationMap, win, nil
}

// BucketAvailabilityForPeriod returns the badge's per-bucket availability stats
// for a check over the uptime-bar window of the given period. It exposes the
// exact bucketing the badge renders from so other surfaces (the status page) can
// assert cross-surface parity. The keys are bucket-start times truncated to the
// period's bucket duration.
func BucketAvailabilityForPeriod(
	ctx context.Context, s *Service, orgUID, checkUID, period string,
) (map[time.Time]uptimebar.BucketStats, error) {
	byBucket, _, err := s.bucketStatsForPeriod(ctx, orgUID, checkUID, period)
	if err != nil {
		return nil, err
	}

	return byBucket, nil
}

// buildBarSegments resolves the colored segments for the uptime bar row.
func buildBarSegments(
	availMap map[time.Time]float64, bucketStart time.Time, n int, bucketDuration time.Duration,
) []string {
	segments := make([]string, n)

	for i := range n {
		t := bucketStart.Add(time.Duration(i) * bucketDuration)
		if pct, ok := availMap[t]; ok {
			segments[i] = uptimeBarColor(pct)
		} else {
			segments[i] = ColorGray
		}
	}

	return segments
}

// computeUptimeBarLabels returns one label per uptime-bar segment, anchoring the
// colored strip in time. The label set depends on the period (derived from
// bucketDuration and n): weekday names for 7d, 6-hour ticks for 24h, week
// boundaries for 30d, and month boundaries for 90d. Slots without a label hold
// an empty string. This is a pure function (no DB access).
func computeUptimeBarLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string {
	switch {
	case bucketDuration == 24*time.Hour && n == 7:
		return weekdayLabels(bucketStart, n, bucketDuration)
	case bucketDuration == time.Hour && n == 24:
		return hourTickLabels(n)
	case bucketDuration == 24*time.Hour && n == 30:
		return weekBoundaryLabels(bucketStart, n, bucketDuration)
	case bucketDuration == 24*time.Hour && n == 90:
		return monthBoundaryLabels(bucketStart, n, bucketDuration)
	default:
		return make([]string, n)
	}
}

// uptimeBarOverlayMinSegWidth is the minimum rendered segment width (px) at
// which a per-segment availability percentage is overlaid inside the bar.
// Below this, segments are too narrow to fit the text. 30 px matches the prior
// implicit behavior (7d/300px shows at 42 px, 7d/200px hides at 27 px).
const uptimeBarOverlayMinSegWidth = 30

// computeUptimeBarValues returns the per-segment availability percentage to
// overlay inside each colored bar. The overlay only appears when each segment is
// rendered wide enough to fit the text — i.e. the minimum segment width exceeds
// uptimeBarOverlayMinSegWidth — regardless of the active period or badge width.
// renderUptimeBarRow splits the bar into n segments with 1-px gaps, so
// segWidth = (width - (n - 1)) / n (integer division). The remainder is spread
// evenly across the segments, so this floor is the minimum segment width and
// individual segment widths differ by at most 1px. When segments are too
// narrow, every period returns an all-empty slice (no overlay). Segments with no
// data (gray) get an empty string. This is a pure function (no DB access).
func computeUptimeBarValues(
	availMap map[time.Time]float64, bucketStart time.Time, n int, bucketDuration time.Duration,
	width int,
) []string {
	values := make([]string, n)

	// Only overlay text when segments are wide enough to fit it.
	segWidth := 0
	if n > 0 {
		segWidth = (width - (n - 1)) / n
	}
	if segWidth <= uptimeBarOverlayMinSegWidth {
		return values
	}

	for i := range n {
		t := bucketStart.Add(time.Duration(i) * bucketDuration)
		if pct, ok := availMap[t]; ok {
			values[i] = formatBarPercent(pct)
		}
	}

	return values
}

// formatBarPercent formats an availability percentage for the compact in-bar
// overlay: whole numbers render without decimals ("100%"), everything else to
// one decimal ("98.6%").
func formatBarPercent(pct float64) string {
	if pct == math.Trunc(pct) {
		return fmt.Sprintf("%.0f%%", pct)
	}

	return fmt.Sprintf("%.1f%%", pct)
}

// weekdayLabels labels every segment with its 3-letter weekday name (7d).
func weekdayLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string {
	labels := make([]string, n)
	for i := range n {
		labels[i] = bucketStart.Add(time.Duration(i) * bucketDuration).Weekday().String()[:3]
	}

	return labels
}

// hourTickLabels labels every 6th segment with an hour mark (24h).
func hourTickLabels(n int) []string {
	labels := make([]string, n)
	for i := range n {
		if i%6 == 0 {
			labels[i] = fmt.Sprintf("%dh", i)
		}
	}

	return labels
}

// weekBoundaryLabels labels the first segment and every Monday with "Jan 2" (30d).
func weekBoundaryLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string {
	labels := make([]string, n)
	for i := range n {
		t := bucketStart.Add(time.Duration(i) * bucketDuration)
		if i == 0 || t.Weekday() == time.Monday {
			labels[i] = t.Format("Jan 2")
		}
	}

	return labels
}

// monthBoundaryLabels labels the first segment and each month's first day with
// its 3-letter month name (90d).
func monthBoundaryLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string {
	labels := make([]string, n)
	for i := range n {
		t := bucketStart.Add(time.Duration(i) * bucketDuration)
		if i == 0 || t.Day() == 1 {
			labels[i] = t.Format("Jan")
		}
	}

	return labels
}

// buildGraphPoints resolves the per-bucket average response times (oldest →
// newest); nil entries mark buckets with no data.
func buildGraphPoints(
	durationMap map[time.Time]*float64, bucketStart time.Time, n int, bucketDuration time.Duration,
) []*float64 {
	points := make([]*float64, n)

	for i := range n {
		t := bucketStart.Add(time.Duration(i) * bucketDuration)
		points[i] = durationMap[t]
	}

	return points
}

// uptimeBarPeriodInfo returns the periodType, number of segments, and bucket
// duration for the given period string.
func uptimeBarPeriodInfo(period string) (string, int, time.Duration) {
	switch period {
	case "24h":
		return models.PeriodTypeHour, 24, time.Hour
	case "7d":
		return models.PeriodTypeDay, 7, 24 * time.Hour
	case "90d":
		return models.PeriodTypeDay, 90, 24 * time.Hour
	default: // "30d"
		return models.PeriodTypeDay, 30, 24 * time.Hour
	}
}

// uptimeBarColor returns the SVG hex color for the given availability
// percentage. Badges are check-scoped (no status-page context) and
// deliberately stay on the global default thresholds — see
// statuspages package's spec 2026-08-03-01 Decisions.
func uptimeBarColor(pct float64) string {
	switch {
	case pct >= models.DefaultAvailabilityThresholdUp:
		return ColorGreen
	case pct >= models.DefaultAvailabilityThresholdDegraded:
		return ColorYellow
	case pct >= 98:
		return ColorOrange
	default:
		return ColorRed
	}
}

// fetchResults fetches the results needed for the given tokens.
func (s *Service) fetchResults(
	ctx context.Context, orgUID, checkUID string, tokens []string, period string,
) ([]*models.Result, error) {
	needsPeriod := false

	for _, token := range tokens {
		if token == componentAvailability || token == componentResponseTime {
			needsPeriod = true

			break
		}
	}

	if needsPeriod {
		periodDuration := parsePeriod(period)
		startTime := time.Now().Add(-periodDuration)
		filter := &models.ListResultsFilter{
			OrganizationUID:  orgUID,
			CheckUIDs:        []string{checkUID},
			PeriodTypes:      []string{"raw"},
			PeriodStartAfter: &startTime,
			// Badges render status, availability and duration only — never the
			// metrics/output blobs (spec 2026-07-24-02 §5).
			SkipBlobs: true,
		}

		res, err := s.dbSvc.ListResults(ctx, filter)
		if err != nil {
			return nil, err
		}

		return res.Results, nil
	}

	// Only status / duration — fetch latest 1 raw result.
	filter := &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       []string{checkUID},
		PeriodTypes:     []string{"raw"},
		Limit:           1,
		SkipBlobs:       true,
	}

	res, err := s.dbSvc.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	return res.Results, nil
}

// composeValue builds the badge value string from the selected tokens.
func (s *Service) composeValue(tokens []string, results []*models.Result) string {
	parts := make([]string, 0, len(tokens))

	for _, token := range tokens {
		switch token {
		case componentStatus:
			parts = append(parts, formatStatus(results))
		case componentAvailability:
			parts = append(parts, formatAvailability(calculateAvailability(results)))
		case componentDuration:
			dur, isUp, ok := calculateStatusDuration(results)
			if ok {
				if isUp {
					parts = append(parts, "↑ "+formatDuration(dur))
				} else {
					parts = append(parts, "↓ "+formatDuration(dur))
				}
			}
			// When unknown (ok=false), omit the duration component.
		case componentResponseTime:
			parts = append(parts, formatResponseTime(results))
		}
	}

	return strings.Join(parts, " ")
}

func (s *Service) applyDefaults(opts BadgeOptions, check *models.Check) BadgeOptions {
	if opts.Period == "" {
		opts.Period = "30d"
	}

	if opts.Label == "" && check.Name != nil {
		opts.Label = *check.Name
	}

	if opts.Style == "" {
		opts.Style = "flat"
	}

	return opts
}

// formatStatus returns the status string for the most recent result.
func formatStatus(results []*models.Result) string {
	if len(results) == 0 {
		return statusUnknown
	}

	if results[0].Status == nil {
		return statusUnknown
	}

	switch *results[0].Status {
	case int(models.ResultStatusUp):
		return statusUp
	case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
		return statusDown
	default:
		return statusUnknown
	}
}

// resolveColor returns the badge color based on component precedence.
func resolveColor(tokens []string, results []*models.Result) string {
	hasStatus := false
	hasAvailability := false

	for _, token := range tokens {
		if token == componentStatus {
			hasStatus = true
		}

		if token == componentAvailability {
			hasAvailability = true
		}
	}

	if hasStatus {
		switch formatStatus(results) {
		case statusUp:
			return ColorGreen
		case statusDown:
			return ColorRed
		default:
			return ColorGray
		}
	}

	if hasAvailability {
		return availabilityColor(calculateAvailability(results))
	}

	return ColorGray
}

// formatResponseTime computes the mean response time from results.
func formatResponseTime(results []*models.Result) string {
	var sum float64

	count := 0

	for _, result := range results {
		if result.Duration != nil {
			sum += float64(*result.Duration)
			count++
		}
	}

	if count == 0 {
		return "n/a"
	}

	// mean is in milliseconds (Duration field stores ms, not seconds)
	mean := sum / float64(count)

	if mean < 1000 {
		return fmt.Sprintf("%dms", int(math.Round(mean)))
	}

	return fmt.Sprintf("%.1fs", mean/1000)
}

// calculateStatusDuration returns the time since last status change, a
// boolean indicating whether the current status is up, and a third boolean
// indicating whether the status is known at all.
//
// Rows excluded from availability are skipped in BOTH walks: lifecycle
// markers (created/running) as before, and — since spec 2026-08-18-10 —
// models.ResultStatusAbandoned. A reaped attempt is not a reading of the
// service, so a badge whose newest row is abandoned must fall back to the
// last real observation rather than announcing "down for 3 days" because our
// own worker crashed. Under the previous encoding these rows carried
// status=error and did exactly that.
func calculateStatusDuration(results []*models.Result) (time.Duration, bool, bool) {
	// Find the most recent actionable status.
	currentStatus := -1

	for _, res := range results {
		if res.Status == nil {
			continue
		}

		s := models.ResultStatus(*res.Status)
		if s.ExcludedFromAvailability() {
			continue
		}

		currentStatus = *res.Status

		break
	}

	if currentStatus == -1 {
		return 0, false, false
	}

	isUp := currentStatus == int(models.ResultStatusUp)

	// Walk newest→oldest, stopping at the first transition away from currentStatus.
	// Record the period_start of the oldest consecutive result with currentStatus.
	lastChangeTime := time.Time{}
	seenCount := 0

	for _, res := range results {
		if res.Status == nil {
			continue
		}

		s := models.ResultStatus(*res.Status)
		if s.ExcludedFromAvailability() {
			continue
		}

		if *res.Status != currentStatus {
			// First result that differs — stop here.
			break
		}

		lastChangeTime = res.PeriodStart
		seenCount++
	}

	if seenCount == 0 {
		return 0, isUp, false
	}

	return time.Since(lastChangeTime), isUp, true
}

func parsePeriod(period string) time.Duration {
	switch period {
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "90d":
		return 90 * 24 * time.Hour
	default: // "30d"
		return 30 * 24 * time.Hour
	}
}

// calculateAvailability computes the availability percentage over raw results
// using the shared rule (excludes created/running lifecycle markers; counts
// up+warning as success), so badges agree with the status page and the
// aggregation job. Warning now counts as up — a deliberate cross-surface
// alignment (see spec 2026-06-30-02).
func calculateAvailability(results []*models.Result) float64 {
	upCount, total := models.RawAvailability(results)
	if total == 0 {
		return 0
	}

	return float64(upCount) / float64(total) * 100
}

// availabilityColor stays on the global default thresholds — see
// uptimeBarColor.
func availabilityColor(pct float64) string {
	switch {
	case pct >= models.DefaultAvailabilityThresholdUp:
		return ColorGreen
	case pct >= models.DefaultAvailabilityThresholdDegraded:
		return ColorYellow
	case pct >= 98:
		return ColorOrange
	default:
		return ColorRed
	}
}

func formatAvailability(pct float64) string {
	if pct >= 99.99 {
		return fmt.Sprintf("%.2f%%", pct)
	}

	return fmt.Sprintf("%.1f%%", pct)
}

func formatDuration(duration time.Duration) string {
	switch {
	case duration >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(duration.Hours()/24))
	case duration >= time.Hour:
		return fmt.Sprintf("%dh", int(duration.Hours()))
	default:
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
}
