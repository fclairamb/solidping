package watchdog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// SubjectStaleChecks is the stable subject of the stale-checks anomaly: one
// anomaly per run carrying a count, never one per check.
const SubjectStaleChecks = "unexplained"

// staleChecksReported is how many of the longest-silent checks the digest names.
const staleChecksReported = 3

// staleCheck is one stale check the dark-region detector does not explain.
type staleCheck struct {
	ref      string
	regions  []string
	silentAt time.Time
	silence  time.Duration
}

// detectStaleChecks reports checks in the `stale` ("No data") status whose
// placement regions are NOT all dark (spec 2026-09-25-02 §6).
//
// A stale check in a dark region is already reported — by the dark-region
// detector, and to the org by the region notice — so counting it here would
// report the same outage twice. What is left is the platform bug class: a
// check whose region is alive but which still produced nothing (a stuck
// scheduler, a lease leak, rate limiting, a claim bug). Only the operator can
// fix those, and nothing else surfaces them.
//
// It reads the SAME RegionHealth report the dark-region detector evaluated, so
// "dark" has one definition per run. Without that report it cannot tell a
// region outage from a platform bug, and fails rather than guess.
func (s *Service) detectStaleChecks(
	ctx context.Context, cfg *Config, report *checks.RegionHealthReport,
) ([]Anomaly, error) {
	if report == nil {
		// Deliberately a failure, not an empty result: reporting every stale
		// check would double-report a region outage, reporting none would read
		// as "healthy". Only this detector's DEPENDENTS go down with region
		// health — fleet-collapse and stale-incidents are unaffected.
		return nil, fmt.Errorf("%w: cannot tell dark regions from platform bugs", ErrRegionHealthUnavailable)
	}

	placements, err := s.db.ListStaleCheckPlacements(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stale checks: %w", err)
	}

	unexplained := unexplainedStaleChecks(placements, report, s.now())
	if len(unexplained) == 0 {
		return nil, nil
	}

	return []Anomaly{staleChecksAnomaly(unexplained, cfg)}, nil
}

// darkRegions indexes a RegionHealth report: the keys of every region with no
// live worker (cloud: slug; private: `<org slug>/<slug>`), and whether ANY
// cloud region is alive — an any-region job is claimable by every cloud worker,
// so it is dark only when they all are.
func darkRegions(report *checks.RegionHealthReport) (map[string]bool, bool) {
	dark := make(map[string]bool, len(report.Regions))
	anyCloudAlive := false

	for i := range report.Regions {
		row := &report.Regions[i]
		if row.LiveWorkers > 0 {
			if row.Organization == "" {
				anyCloudAlive = true
			}

			continue
		}

		dark[darkRegionSubject(row)] = true
	}

	return dark, anyCloudAlive
}

// placementKey is how a check_jobs region is keyed against the report.
func placementKey(placement *models.StaleCheckPlacement) string {
	if regions.IsPrivateRegion(placement.Region) {
		return placement.OrganizationSlug + "/" + placement.Region
	}

	return placement.Region
}

// unexplainedStaleChecks keeps the stale checks with at least one placement
// region that is not dark, longest-silent first.
func unexplainedStaleChecks(
	placements []models.StaleCheckPlacement, report *checks.RegionHealthReport, now time.Time,
) []staleCheck {
	dark, anyCloudAlive := darkRegions(report)

	type entry struct {
		check     staleCheck
		explained bool
	}

	byCheck := make(map[string]*entry, len(placements))
	order := make([]string, 0, len(placements))

	for i := range placements {
		placement := &placements[i]

		current, ok := byCheck[placement.CheckUID]
		if !ok {
			silentAt := placement.CreatedAt
			if placement.LastResultAt != nil {
				silentAt = *placement.LastResultAt
			}

			current = &entry{
				check: staleCheck{
					ref:      placementReference(placement),
					silentAt: silentAt,
					silence:  now.Sub(silentAt),
				},
				explained: true,
			}
			byCheck[placement.CheckUID] = current
			order = append(order, placement.CheckUID)
		}

		label := placement.Region
		if label == "" {
			label = "any"
		}

		current.check.regions = append(current.check.regions, label)

		var regionDark bool
		if placement.Region == "" {
			regionDark = !anyCloudAlive
		} else {
			regionDark = dark[placementKey(placement)]
		}

		if !regionDark {
			current.explained = false
		}
	}

	out := make([]staleCheck, 0, len(order))

	for _, uid := range order {
		if current := byCheck[uid]; !current.explained {
			out = append(out, current.check)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].silence > out[j].silence })

	return out
}

// placementReference names a stale check the way a human refers to it:
// `<org>/<slug>`, falling back to the name.
func placementReference(placement *models.StaleCheckPlacement) string {
	name := "(unnamed check)"

	switch {
	case placement.CheckSlug != nil && *placement.CheckSlug != "":
		name = *placement.CheckSlug
	case placement.CheckName != nil && *placement.CheckName != "":
		name = *placement.CheckName
	}

	return placement.OrganizationSlug + "/" + name
}

// staleChecksAnomaly folds every unexplained stale check into ONE anomaly.
func staleChecksAnomaly(stale []staleCheck, cfg *Config) Anomaly {
	severity := SeverityWarning
	if len(stale) >= cfg.StaleChecksCriticalCount {
		severity = SeverityCritical
	}

	shown := stale
	if len(shown) > staleChecksReported {
		shown = shown[:staleChecksReported]
	}

	details := make([]string, 0, len(shown))

	for i := range shown {
		details = append(details, fmt.Sprintf("%s in %s (last result %s, silent for %s)",
			shown[i].ref, strings.Join(shown[i].regions, ","),
			shown[i].silentAt.UTC().Format(time.RFC3339), roundDuration(shown[i].silence)))
	}

	return Anomaly{
		Detector: DetectorStaleChecks,
		Subject:  SubjectStaleChecks,
		Severity: severity,
		Headline: fmt.Sprintf(
			"%d check(s) show \"No data\" although their region is not dark — "+
				"they stopped producing results for a platform reason", len(stale)),
		Detail: "longest silent: " + strings.Join(details, "; "),
		Remediation: "the region is serving other work, so look at scheduling for these checks: " +
			"their check_jobs rows (scheduled_at, lease_expires_at), rate limiting, and the worker logs",
		Count: len(stale),
	}
}
