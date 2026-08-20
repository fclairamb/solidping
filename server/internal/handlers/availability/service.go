// Package availability provides the server-side per-period availability
// statistics endpoint for a single check. It replaces the old client-side
// estimate (which fabricated downtime as (1−availability)×calendar-length) with
// real measurements from the shared uptimebar engine, plus a confirmed-incident
// wall-clock block.
package availability

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Service errors, translated to HTTP codes by the handler.
var (
	// ErrOrganizationNotFound is returned when the organization slug doesn't resolve.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound is returned when the check identifier doesn't resolve to a check in the org.
	ErrCheckNotFound = errors.New("check not found")
	// ErrInvalidPeriod is returned when a period token is unknown, the list is
	// empty/too long, or a window exceeds the sanity lookback cap.
	ErrInvalidPeriod = errors.New("invalid period")
)

const (
	// maxPeriods caps how many periods one request may ask for.
	maxPeriods = 12
	// maxConcurrentPeriods bounds the fan-out of per-window computations. periods
	// is caller-controlled (up to maxPeriods), so this keeps one HTTP request from
	// opening an unbounded number of simultaneous DB connections.
	maxConcurrentPeriods = 6
	// maxLookbackYears is a pure input-sanity bound, not a data horizon: the
	// uptimebar union includes the month tier, which is terminal (never rolled
	// further, never deleted), so any lookback is answerable regardless of the
	// raw/hour/day retention tuning. This only rejects absurd tokens like
	// "999999d" that would produce meaningless multi-century windows.
	maxLookbackYears = 10

	// Calendar period tokens, resolved against the request timezone.
	tokenToday = "today"
	tokenMTD   = "mtd"
	tokenYTD   = "ytd"
)

// Service provides business logic for the availability endpoint.
type Service struct {
	db  db.Service
	cfg *config.Config
}

// NewService creates a new availability service. cfg may be nil (e.g. a test
// exercising the service in isolation) — the uptime-bar queries then fall back
// to the documented retention defaults instead of the org's configured values.
func NewService(dbService db.Service, cfg *config.Config) *Service {
	return &Service{db: dbService, cfg: cfg}
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
	rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)

	return uptimebar.Hints{
		RetentionRawHours: rawHours,
		RetentionHourDays: hourDays,
		RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, s.db, orgUID),
	}
}

// PeriodIncidents is the confirmed-outage (wall-clock) block for one window —
// definition B in the spec. Built from incidents whose started_at falls in the
// window, clamped to the window edges.
type PeriodIncidents struct {
	Count                int   `json:"count"`
	LongestSeconds       int64 `json:"longestSeconds,omitempty"`
	AverageSeconds       int64 `json:"averageSeconds,omitempty"`
	TotalDowntimeSeconds int64 `json:"totalDowntimeSeconds,omitempty"`
}

// Period is one row of the response: the probe-ratio availability
// (definition A — primary) plus the incident wall-clock block (definition B).
type Period struct {
	Period           string    `json:"period"`
	WindowStart      time.Time `json:"windowStart"`
	WindowEnd        time.Time `json:"windowEnd"`
	MonitoredSeconds int64     `json:"monitoredSeconds"`
	Partial          bool      `json:"partial"`
	HasData          bool      `json:"hasData"`
	TotalChecks      int       `json:"totalChecks"`
	SuccessfulChecks int       `json:"successfulChecks"`
	// AvailabilityPct is null (and HasData false) when TotalChecks == 0 — no data
	// is not 100%. The UI renders "-".
	AvailabilityPct *float64        `json:"availabilityPct"`
	DowntimeSeconds int64           `json:"downtimeSeconds"`
	Incidents       PeriodIncidents `json:"incidents"`
}

// ListAvailabilityResponse wraps the period list in `data` per repo convention.
type ListAvailabilityResponse struct {
	Data []Period `json:"data"`
}

// GetAvailabilityOptions carries the parsed request parameters.
type GetAvailabilityOptions struct {
	Periods []string // raw period tokens, e.g. ["today", "7d", "30d", "365d"]
	TZ      string   // IANA zone for calendar tokens; "" means UTC
}

// GetAvailability resolves the check, then for each requested period computes the
// probe-ratio availability/downtime from the shared engine and the incident block.
func (s *Service) GetAvailability(
	ctx context.Context, orgSlug, checkIdent string, opts *GetAvailabilityOptions,
) (*ListAvailabilityResponse, error) {
	loc, err := resolveLocation(opts.TZ)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	windows, err := parsePeriods(opts.Periods, now, loc)
	if err != nil {
		return nil, err
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdent)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	periods := make([]Period, len(windows))

	// Resolved ONCE for the whole request: every window shares the same org, so
	// re-resolving per window would multiply the parameter/probe-rate lookups by
	// the number of requested periods (five on the check detail page) inside a
	// spec whose whole point is removing per-request latency.
	hints := s.uptimebarHints(ctx, org.UID)

	// The windows are independent — each computePeriod only reads org.UID, check
	// and its own window, and writes only its own slot. Fan them out concurrently
	// so the request costs the slowest window instead of their sum, bounded so a
	// caller-controlled period count can't open unbounded DB connections. Written
	// by index (never appended) so the response stays in the requested order.
	errg, errgCtx := errgroup.WithContext(ctx)
	errg.SetLimit(maxConcurrentPeriods)

	for i := range windows {
		errg.Go(func() error {
			row, periodErr := s.computePeriod(errgCtx, org.UID, check, windows[i], now, hints)
			if periodErr != nil {
				return periodErr
			}

			periods[i] = row

			return nil
		})
	}

	if err := errg.Wait(); err != nil {
		return nil, err
	}

	return &ListAvailabilityResponse{Data: periods}, nil
}

// periodWindow is a resolved [start, end) for a requested token.
type periodWindow struct {
	token string
	start time.Time
	end   time.Time
}

// computePeriod builds the DTO for one resolved window.
func (s *Service) computePeriod(
	ctx context.Context, orgUID string, check *models.Check, window periodWindow, now time.Time,
	hints uptimebar.Hints,
) (Period, error) {
	monitored := monitoredDuration(window, check.CreatedAt.UTC(), now)

	stats, err := uptimebar.WindowAvailability(
		ctx, s.db, orgUID, []string{check.UID}, window.start, window.end,
		hints)
	if err != nil {
		return Period{}, err
	}

	bucket := stats[check.UID]

	row := buildPeriodRow(window, bucket, monitored, check.CreatedAt.UTC())

	incidents, err := s.fetchIncidents(ctx, orgUID, check.UID, window)
	if err != nil {
		return Period{}, err
	}

	row.Incidents = incidentBlock(incidents, window, now)

	return row, nil
}

// monitoredDuration is min(windowLength, now − createdAt): the check did not
// exist before createdAt, so that time is not measured (and not downtime).
func monitoredDuration(window periodWindow, createdAt, now time.Time) time.Duration {
	monitored := window.end.Sub(window.start)

	if createdAt.After(window.start) {
		if fromCreation := now.Sub(createdAt); fromCreation < monitored {
			monitored = fromCreation
		}
	}

	if monitored < 0 {
		monitored = 0
	}

	return monitored
}

// buildPeriodRow assembles the probe-ratio part of the DTO from the window's
// folded BucketStats. availabilityPct is nil (hasData false) when the window has
// no countable checks — no data ≠ 100%.
func buildPeriodRow(
	window periodWindow, bucket uptimebar.BucketStats, monitored time.Duration, createdAt time.Time,
) Period {
	row := Period{
		Period:           window.token,
		WindowStart:      window.start,
		WindowEnd:        window.end,
		MonitoredSeconds: int64(monitored.Seconds()),
		Partial:          createdAt.After(window.start),
		TotalChecks:      bucket.Total,
		SuccessfulChecks: bucket.Up,
	}

	if pct, ok := bucket.AvailabilityPct(); ok {
		row.HasData = true
		row.AvailabilityPct = &pct
		// Probe-time downtime: (1 − avail) × monitoredSeconds. The window is a
		// single bucket here, so the per-bucket Σ(failedFraction×duration) the spec
		// recommends collapses to exactly this — attributed only to measured time.
		// Round to the nearest second so float error doesn't systematically
		// under-report (e.g. 0.1×604800 = 60479.99… → 60480).
		failedFraction := 1 - pct/100
		row.DowntimeSeconds = int64(math.Round(failedFraction * monitored.Seconds()))
	}

	return row
}

// fetchIncidents loads incidents whose started_at falls in the window for the
// check (per-check or as a group member).
func (s *Service) fetchIncidents(
	ctx context.Context, orgUID, checkUID string, window periodWindow,
) ([]*models.Incident, error) {
	filter := &models.ListIncidentsFilter{
		OrganizationUID: orgUID,
		MemberCheckUID:  checkUID,
		Since:           &window.start,
		Until:           &window.end,
		Limit:           1000,
	}

	incidents, _, err := s.db.ListIncidents(ctx, filter)

	return incidents, err
}

// IncidentBlock is the exported form of incidentBlock, for callers outside this
// package that need the same wall-clock outage summary over an arbitrary window
// — currently the SLO status endpoint, which shows it as context beside the
// probe-ratio attainment (spec 2026-08-20-01). Keeping one implementation is
// the point: an SLO page whose "3 incidents, longest 42 min" disagreed with the
// availability API would be worse than showing nothing.
func IncidentBlock(incidents []*models.Incident, start, end, now time.Time) PeriodIncidents {
	return incidentBlock(incidents, periodWindow{start: start, end: end}, now)
}

// incidentBlock computes the wall-clock outage stats (definition B), clamping
// each incident to the window (an unresolved incident runs to now). Incidents
// that didn't actually start inside the window are ignored (defensive against a
// loose filter).
func incidentBlock(incidents []*models.Incident, window periodWindow, now time.Time) PeriodIncidents {
	var (
		count   int
		longest int64
		total   int64
	)

	for _, inc := range incidents {
		startedAt := inc.StartedAt.UTC()
		if startedAt.Before(window.start) || !startedAt.Before(window.end) {
			continue
		}

		end := now
		if inc.ResolvedAt != nil {
			end = inc.ResolvedAt.UTC()
		}

		clampedEnd := end
		if clampedEnd.After(window.end) {
			clampedEnd = window.end
		}

		dur := int64(clampedEnd.Sub(startedAt).Seconds())
		if dur < 0 {
			dur = 0
		}

		count++
		total += dur

		if dur > longest {
			longest = dur
		}
	}

	block := PeriodIncidents{Count: count}

	if count > 0 {
		block.LongestSeconds = longest
		block.TotalDowntimeSeconds = total
		block.AverageSeconds = total / int64(count)
	}

	return block
}

// resolveLocation loads the IANA zone for calendar-token resolution; empty → UTC.
func resolveLocation(timezone string) (*time.Location, error) {
	if timezone == "" {
		return time.UTC, nil
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown timezone %q", ErrInvalidPeriod, timezone)
	}

	return loc, nil
}

// parsePeriods resolves each token to a [start, now) window and validates the set.
func parsePeriods(tokens []string, now time.Time, loc *time.Location) ([]periodWindow, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("%w: at least one period is required", ErrInvalidPeriod)
	}

	if len(tokens) > maxPeriods {
		return nil, fmt.Errorf("%w: at most %d periods allowed", ErrInvalidPeriod, maxPeriods)
	}

	maxLookback := time.Duration(maxLookbackYears) * 365 * 24 * time.Hour

	windows := make([]periodWindow, 0, len(tokens))

	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			return nil, fmt.Errorf("%w: empty period token", ErrInvalidPeriod)
		}

		start, err := resolvePeriodStart(token, now, loc)
		if err != nil {
			return nil, err
		}

		if start.After(now) || now.Sub(start) > maxLookback {
			return nil, fmt.Errorf(
				"%w: %q exceeds the maximum lookback (%d years)",
				ErrInvalidPeriod, token, maxLookbackYears)
		}

		windows = append(windows, periodWindow{token: token, start: start, end: now})
	}

	return windows, nil
}

// resolvePeriodStart maps one token to its window start.
//   - Calendar tokens (today/mtd/ytd) resolve against loc.
//   - Trailing durations (24h/7d/30d/90d/365d/1y, suffix h/d/w/y) are tz-independent.
func resolvePeriodStart(token string, now time.Time, loc *time.Location) (time.Time, error) {
	switch strings.ToLower(token) {
	case tokenToday:
		local := now.In(loc)
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

		return midnight.UTC(), nil
	case tokenMTD:
		local := now.In(loc)
		monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)

		return monthStart.UTC(), nil
	case tokenYTD:
		local := now.In(loc)
		yearStart := time.Date(local.Year(), 1, 1, 0, 0, 0, 0, loc)

		return yearStart.UTC(), nil
	}

	dur, err := parseDurationToken(token)
	if err != nil {
		return time.Time{}, err
	}

	return now.Add(-dur).UTC(), nil
}

// parseDurationToken parses a trailing-duration token like "24h", "7d", "30d",
// "90d", "365d", "1y", "2w". time.ParseDuration only knows h/m/s, so day/week/
// year suffixes need this small parser.
func parseDurationToken(token string) (time.Duration, error) {
	if len(token) < 2 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidPeriod, token)
	}

	unit := token[len(token)-1]
	numPart := token[:len(token)-1]

	n, err := strconv.Atoi(numPart)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidPeriod, token)
	}

	var unitDur time.Duration

	switch unit {
	case 'h':
		unitDur = time.Hour
	case 'd':
		unitDur = 24 * time.Hour
	case 'w':
		unitDur = 7 * 24 * time.Hour
	case 'y':
		unitDur = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("%w: %q", ErrInvalidPeriod, token)
	}

	dur := time.Duration(n) * unitDur
	// A huge n overflows the int64 nanosecond range and wraps; a wrapped value
	// no longer divides back to n. Catch it here so the caller's lookback check
	// sees a real error instead of a garbage window.
	if dur/unitDur != time.Duration(n) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidPeriod, token)
	}

	return dur, nil
}
