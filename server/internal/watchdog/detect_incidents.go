package watchdog

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// uidChunkSize bounds how many UIDs go into one IN(...) clause. bun.List
// inlines every value as a SQL literal, so this bounds generated statement
// size and parse cost rather than any driver placeholder limit — same
// reasoning and same number as sqlite.checksByUIDsChunkSize.
const uidChunkSize = 500

// SubjectStaleIncidents is the stable subject of the frozen-incident anomaly.
// One anomaly per run carrying a count, not one per incident: 61 frozen
// incidents must produce one line in one digest, never 61 pages.
const SubjectStaleIncidents = "active"

// staleIncidentsReported is how many of the oldest frozen incidents the digest
// names. Three is enough to recognize the pattern without turning the message
// into a dump.
const staleIncidentsReported = 3

// lastResultRow is the projection of the grouped MAX(period_start) query.
type lastResultRow struct {
	CheckUID string     `bun:"check_uid"`
	LastAt   *time.Time `bun:"last_at"`
}

// staleIncident is one frozen incident with everything the digest needs.
type staleIncident struct {
	number   int64
	checkRef string
	lastAt   *time.Time
	staleFor time.Duration
}

// detectStaleIncidents reports active incidents whose check has stopped
// producing results.
//
// This is the second half of the 2026-08-24 damage: 61 incidents stayed in
// `active`, ~50 of them for targets that had already recovered, because a
// check that stops being executed keeps its last state forever. Escalations
// had already fired at humans for outages that were over — the frozen
// incident is not cosmetic, it is a page.
//
// Three bounded queries, no per-incident round trip.
func (s *Service) detectStaleIncidents(ctx context.Context, cfg *Config) ([]Anomaly, error) {
	now := s.now()

	incidents, err := s.listActiveIncidents(ctx, cfg.StaleIncidentScanLimit)
	if err != nil {
		return nil, err
	}

	if len(incidents) == 0 {
		return nil, nil
	}

	checkUIDs := distinctCheckUIDs(incidents)

	checksByUID, err := s.loadChecks(ctx, checkUIDs)
	if err != nil {
		return nil, err
	}

	lastResults, err := s.lastRawResultPerCheck(ctx, checkUIDs)
	if err != nil {
		return nil, err
	}

	stale := collectStaleIncidents(incidents, checksByUID, lastResults, cfg, now)
	if len(stale) == 0 {
		return nil, nil
	}

	return []Anomaly{staleIncidentsAnomaly(stale, cfg, now)}, nil
}

// listActiveIncidents scans the open incidents, oldest first, capped so a
// genuinely huge outage cannot make the watchdog itself expensive.
func (s *Service) listActiveIncidents(ctx context.Context, limit int) ([]*models.Incident, error) {
	var incidents []*models.Incident

	err := s.db.DB().NewSelect().
		Model(&incidents).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		OrderExpr("started_at ASC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active incidents: %w", err)
	}

	return incidents, nil
}

// loadChecks fetches the checks behind the open incidents in one query.
// Disabled and deleted checks are excluded here rather than filtered later:
// a check nobody executes on purpose has no business being reported as one
// that mysteriously stopped.
func (s *Service) loadChecks(ctx context.Context, uids []string) (map[string]*models.Check, error) {
	if len(uids) == 0 {
		return map[string]*models.Check{}, nil
	}

	out := make(map[string]*models.Check, len(uids))

	for batch := range slices.Chunk(uids, uidChunkSize) {
		var rows []*models.Check

		err := s.db.DB().NewSelect().
			Model(&rows).
			Where("uid IN (?)", bun.List(batch)).
			Where("deleted_at IS NULL").
			Where("enabled = ?", true).
			Scan(ctx)
		if err != nil {
			return nil, fmt.Errorf("load checks for stale incidents: %w", err)
		}

		for _, row := range rows {
			out[row.UID] = row
		}
	}

	return out, nil
}

// lastRawResultPerCheck is the grouped "when did this check last produce
// anything" query — one scan for every check, never one query per incident.
func (s *Service) lastRawResultPerCheck(ctx context.Context, uids []string) (map[string]*time.Time, error) {
	if len(uids) == 0 {
		return map[string]*time.Time{}, nil
	}

	out := make(map[string]*time.Time, len(uids))

	for batch := range slices.Chunk(uids, uidChunkSize) {
		var rows []lastResultRow

		err := s.db.DB().NewSelect().
			TableExpr("results").
			ColumnExpr("check_uid, MAX(period_start) AS last_at").
			Where("period_type = ?", "raw").
			Where("check_uid IN (?)", bun.List(batch)).
			GroupExpr("check_uid").
			Scan(ctx, &rows)
		if err != nil {
			return nil, fmt.Errorf("last result per check: %w", err)
		}

		for i := range rows {
			out[rows[i].CheckUID] = rows[i].LastAt
		}
	}

	return out, nil
}

// distinctCheckUIDs collects the check UIDs behind a set of incidents.
func distinctCheckUIDs(incidents []*models.Incident) []string {
	seen := make(map[string]bool, len(incidents))
	out := make([]string, 0, len(incidents))

	for _, incident := range incidents {
		if incident.CheckUID == "" || seen[incident.CheckUID] {
			continue
		}

		seen[incident.CheckUID] = true

		out = append(out, incident.CheckUID)
	}

	return out
}

// collectStaleIncidents applies the max(N × period, floor) staleness rule.
//
// The reference instant is the LATER of the last result and the incident
// start: an incident opened one second ago on a check whose previous result
// predates it is not frozen, it is new.
func collectStaleIncidents(
	incidents []*models.Incident, checksByUID map[string]*models.Check,
	lastResults map[string]*time.Time, cfg *Config, now time.Time,
) []staleIncident {
	out := make([]staleIncident, 0, len(incidents))

	for _, incident := range incidents {
		check, ok := checksByUID[incident.CheckUID]
		if !ok {
			continue
		}

		threshold := staleThreshold(check, cfg)

		reference := incident.StartedAt

		lastAt := lastResults[incident.CheckUID]
		if lastAt != nil && lastAt.After(reference) {
			reference = *lastAt
		}

		silence := now.Sub(reference)
		if silence < threshold {
			continue
		}

		out = append(out, staleIncident{
			number:   incident.Number,
			checkRef: checkReference(check),
			lastAt:   lastAt,
			staleFor: silence,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].staleFor > out[j].staleFor })

	return out
}

// staleThreshold is max(multiplier × period, floor) for one check.
func staleThreshold(check *models.Check, cfg *Config) time.Duration {
	threshold := time.Duration(check.Period) * time.Duration(cfg.StaleIncidentPeriodMultiplier)
	if floor := cfg.StaleIncidentMinAge(); threshold < floor {
		threshold = floor
	}

	return threshold
}

// checkReference names a check the way a human refers to it — slug first,
// then name, never the UID.
func checkReference(check *models.Check) string {
	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug
	}

	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	return "(unnamed check)"
}

// staleIncidentsAnomaly folds every frozen incident into ONE anomaly carrying
// the count and the three oldest.
func staleIncidentsAnomaly(stale []staleIncident, cfg *Config, now time.Time) Anomaly {
	severity := SeverityWarning
	if len(stale) >= cfg.StaleIncidentCriticalCount {
		severity = SeverityCritical
	}

	shown := stale
	if len(shown) > staleIncidentsReported {
		shown = shown[:staleIncidentsReported]
	}

	details := make([]string, 0, len(shown))

	for i := range shown {
		last := "never"
		if shown[i].lastAt != nil {
			last = shown[i].lastAt.UTC().Format(time.RFC3339)
		}

		details = append(details, fmt.Sprintf("#%d %s (last result %s, silent for %s)",
			shown[i].number, shown[i].checkRef, last, roundDuration(shown[i].staleFor)))
	}

	return Anomaly{
		Detector: DetectorStaleIncidents,
		Subject:  SubjectStaleIncidents,
		Severity: severity,
		Headline: fmt.Sprintf(
			"%d active incident(s) are frozen: their check has produced no result for longer than max(%d × period, %s)",
			len(stale), cfg.StaleIncidentPeriodMultiplier, cfg.StaleIncidentMinAge(),
		),
		Detail: "oldest: " + strings.Join(details, "; ") +
			" (as of " + now.UTC().Format(time.RFC3339) + ")",
		Remediation: "these incidents cannot resolve until their checks run again — " +
			"fix execution first (GET /api/v1/system/regions/health), then re-verify their state",
		Count: len(stale),
	}
}
