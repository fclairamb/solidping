// Package freshness is the minute sweep that moves a check whose results
// stopped to the `stale` ("No data") status (spec 2026-09-25-02).
//
// checks.status only changes when a result arrives, so before this sweep a
// check that stopped running kept its last status forever: on 2026-09-24 a
// region was dark for 8 hours and its 12 pinned checks read `up` the whole
// time. The cause does not matter here — a dead region, a stuck scheduler, a
// lease leak, rate limiting, a database stall all look the same from the
// check's row — which is exactly why the sweep only reads the row.
//
// Entering stale deliberately BYPASSES the incident pipeline
// (incidents.ProcessCheckResult): one guarded compare-and-set per check, no
// event, no notification, the streak untouched. Leaving stale goes through the
// pipeline, on the next real result.
package freshness

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// sweepBatchSize bounds one sweep. Candidates are read oldest-first, so a
// region dying with more checks than this drains over a few minutes instead of
// making one sweep unbounded.
const sweepBatchSize = 1000

// Placement region labels for solidping_checks_stale.
const (
	// RegionLabelAny labels an any-region job (and a stale check with no job).
	RegionLabelAny = "any"
	// RegionLabelPrivate folds every private (`@`) region: their slugs are
	// org-relative, so one label per slug would merge orgs.
	RegionLabelPrivate = "private"
)

// Service is the freshness sweeper.
type Service struct {
	db        db.Service
	incidents *incidents.Service
	rt        *realtime.Publisher
	logger    *slog.Logger

	// gaugeMu guards gaugeRegions, the region labels this process has ever
	// published, so a region whose stale count drops to zero is written back
	// as 0 instead of freezing at its last value.
	gaugeMu      sync.Mutex
	gaugeRegions map[string]struct{}
}

// NewService builds the freshness sweeper. rt may be nil (realtime disabled):
// every Publisher method is nil-receiver safe.
func NewService(
	dbService db.Service, incidentSvc *incidents.Service, realtimePub *realtime.Publisher, logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{
		db:           dbService,
		incidents:    incidentSvc,
		rt:           realtimePub,
		logger:       logger,
		gaugeRegions: make(map[string]struct{}),
	}
}

// SweepStale runs one sweep and returns how many checks it moved to stale.
//
// Per-check failures are logged and skipped: one check whose maintenance
// lookup failed must not keep every other check green for another minute.
func (s *Service) SweepStale(ctx context.Context, now time.Time) (int, error) {
	candidates, err := s.db.ListStaleCandidates(ctx, now, sweepBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list stale candidates: %w", err)
	}

	marked := 0

	for _, check := range candidates {
		changed, markErr := s.sweepCheck(ctx, check, now)
		if markErr != nil {
			s.logger.WarnContext(ctx, "Freshness sweep failed for a check",
				"checkUid", check.UID, "error", markErr)

			continue
		}

		if changed {
			marked++
		}
	}

	s.publishGauge(ctx)

	return marked, nil
}

// sweepCheck applies the exact threshold and the maintenance exclusion to one
// candidate, then writes the guarded update.
func (s *Service) sweepCheck(ctx context.Context, check *models.Check, now time.Time) (bool, error) {
	// The candidate query only applied the 5-minute floor on SQLite; this is
	// the exact max(3 × period, 5 min) rule on both engines.
	if !check.IsDataStale(now) {
		return false, nil
	}

	// A check never ENTERS stale while a maintenance window is open. The first
	// sweep after the window closes evaluates it normally.
	if s.incidents != nil {
		// Same cached resolver the result path uses, evaluated at the sweep's
		// own instant so both halves agree on when a window is open.
		inMaintenance, err := s.incidents.MaintenanceResolver().IsActiveAt(ctx, check.UID, now)
		if err != nil {
			return false, fmt.Errorf("maintenance lookup: %w", err)
		}

		if inMaintenance {
			return false, nil
		}
	}

	cutoff := now.Add(-check.StaleThreshold())

	changed, err := s.db.MarkCheckStale(ctx, check.UID, check.Status, cutoff, now)
	if err != nil {
		return false, err
	}

	if !changed {
		// A result (or an edit) won the race: the compare-and-set is what
		// guarantees fresh evidence always beats the sweep.
		return false, nil
	}

	since := check.FreshnessReference()

	s.logger.InfoContext(ctx, "Check went stale: no real result within its freshness threshold",
		"checkUid", check.UID,
		"previousStatus", check.Status.String(),
		"lastResultAt", check.LastResultAt,
		"threshold", check.StaleThreshold().String(),
	)

	// Coalesced, not immediate: when a whole region dies every one of its
	// checks flips in the same sweep, and above realtime.CollapseCheckUids
	// they collapse into one "all checks" hint for the org.
	s.rt.Publish(ctx, check.OrganizationUID, check.UID, realtime.KindChecks)

	if s.incidents != nil {
		if _, recErr := s.incidents.RecordMonitoringInterrupted(ctx, check, since); recErr != nil {
			s.logger.WarnContext(ctx, "Failed to record monitoring interrupted on the incident timeline",
				"checkUid", check.UID, "error", recErr)
		}
	}

	return true, nil
}

// PlacementRegionLabel maps a check_jobs region onto its gauge label.
func PlacementRegionLabel(region string) string {
	switch {
	case region == "":
		return RegionLabelAny
	case regions.IsPrivateRegion(region):
		return RegionLabelPrivate
	default:
		return region
	}
}

// StaleCountsByRegion folds placements into distinct stale checks per label.
func StaleCountsByRegion(placements []models.StaleCheckPlacement) map[string]int {
	seen := make(map[string]map[string]struct{})

	for i := range placements {
		label := PlacementRegionLabel(placements[i].Region)
		if seen[label] == nil {
			seen[label] = make(map[string]struct{})
		}

		seen[label][placements[i].CheckUID] = struct{}{}
	}

	out := make(map[string]int, len(seen))
	for label, uids := range seen {
		out[label] = len(uids)
	}

	return out
}

// publishGauge refreshes solidping_checks_stale. A failed read leaves the
// gauge at its previous values rather than publishing zeros — "healthy" is not
// a fact this run established.
func (s *Service) publishGauge(ctx context.Context) {
	placements, err := s.db.ListStaleCheckPlacements(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "Failed to count stale checks for the gauge", "error", err)

		return
	}

	counts := StaleCountsByRegion(placements)

	s.gaugeMu.Lock()
	defer s.gaugeMu.Unlock()

	for label := range counts {
		s.gaugeRegions[label] = struct{}{}
	}

	// The any-region label always exists, so `solidping_checks_stale > 0`
	// has a series to evaluate from the very first sweep.
	s.gaugeRegions[RegionLabelAny] = struct{}{}

	for label := range s.gaugeRegions {
		prommetrics.SetChecksStale(label, float64(counts[label]))
	}
}
