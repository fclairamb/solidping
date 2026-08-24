package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// RegionHealthRow is one region slug's ghost-detection summary (spec
// 2026-08-24-09) — a single row for every slug seen anywhere: in the declared
// `regions` system parameter, in a check's `regions` array, in a
// `check_jobs.region`, or in a worker's announced region.
type RegionHealthRow struct {
	Slug     string `json:"slug"`
	Declared bool   `json:"declared"`
	// LiveWorkers counts non-deleted workers whose last_active_at is within
	// regions.WorkerLivenessWindow AND whose announced region matches this
	// slug under the scheduler's own prefix rule (workerRegion has slug as a
	// prefix) — the exact predicate checkjobsvc.applyCloudRegionScope uses to
	// claim a job.
	LiveWorkers int `json:"liveWorkers"`
	// LastWorkerSeenAt is the max last_active_at across every matching
	// worker, INCLUDING soft-deleted ones — it dates when the region went
	// dark, which is the first thing an operator wants during triage.
	LastWorkerSeenAt *time.Time `json:"lastWorkerSeenAt"`
	// ChecksReferencing counts distinct, non-deleted checks whose `regions`
	// array names this slug.
	ChecksReferencing int `json:"checksReferencing"`
	// Jobs counts check_jobs rows carrying this slug. NULL-region (any-region)
	// jobs are excluded — they are claimable by every cloud worker by
	// construction and never belong to a specific slug.
	Jobs int `json:"jobs"`
	// JobsOverdue is the subset of Jobs whose scheduledAt has already passed.
	JobsOverdue int `json:"jobsOverdue"`
	// OldestOverdueAt is the earliest scheduledAt among JobsOverdue, nil when
	// there are none.
	OldestOverdueAt *time.Time `json:"oldestOverdueAt"`
	// Ghost is true when something depends on this slug (a job or a check
	// reference) and nothing live can serve it. A declared region with zero
	// live workers and zero references is dark but unused — reported with
	// LiveWorkers: 0 but Ghost: false, since nothing is actually stranded.
	Ghost bool `json:"ghost"`
}

// RegionHealthReport is the body of GET /api/v1/system/regions/health.
type RegionHealthReport struct {
	Regions     []RegionHealthRow `json:"regions"`
	GhostCount  int               `json:"ghostCount"`
	GeneratedAt time.Time         `json:"generatedAt"`
}

// checkRegionsRow is the narrow projection RegionHealth scans off `checks` —
// only the column the per-slug reference count needs.
type checkRegionsRow struct {
	Regions []string `bun:"regions,type:text[],array"`
}

// checkJobRegionRow is the narrow projection RegionHealth scans off
// `check_jobs` — only the columns the per-slug job stats need.
type checkJobRegionRow struct {
	Region      *string    `bun:"region"`
	ScheduledAt *time.Time `bun:"scheduled_at"`
}

// regionJobStats accumulates the job-side counters for one slug while
// scanning checkJobRegionRow.
type regionJobStats struct {
	jobs            int
	overdue         int
	oldestOverdueAt *time.Time
}

// RegionHealth computes the ghost-detection report (spec 2026-08-24-09): one
// row per region slug seen anywhere, with the live-worker count and reference
// counts needed to tell a genuinely stranded region apart from a merely
// unused one. This is the read-side companion of MigrateRegion (spec
// 2026-08-24-08) and the function the hourly watchdog (spec 2026-08-24-10)
// will call too — the detection rule lives exactly once.
//
// Three bounded, dialect-neutral scans (checks, check_jobs, workers) feed a
// single in-memory aggregation — no per-slug query, no per-check loop. This
// mirrors system.Service.LaneLoad, which aggregates the same kind of
// worker/region data the same way for the same reason.
func (s *Service) RegionHealth(ctx context.Context) (*RegionHealthReport, error) {
	now := time.Now()

	declared, err := s.declaredRegionSlugs(ctx)
	if err != nil {
		return nil, err
	}

	checksReferencing, err := s.regionCheckReferenceCounts(ctx)
	if err != nil {
		return nil, err
	}

	jobStats, err := s.regionJobStatsBySlug(ctx, now)
	if err != nil {
		return nil, err
	}

	var workers []*models.Worker
	if err := s.db.DB().NewSelect().
		Model(&workers).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list workers for region health: %w", err)
	}

	universe := regionSlugUniverse(declared, checksReferencing, jobStats, workers)

	liveCutoff := now.Add(-regions.WorkerLivenessWindow)

	rows := make([]RegionHealthRow, 0, len(universe))
	ghostCount := 0

	for _, slug := range universe {
		liveWorkers, lastSeen := workerCoverageForSlug(workers, slug, liveCutoff)

		stats := jobStats[slug]

		row := RegionHealthRow{
			Slug:              slug,
			Declared:          declared[slug],
			LiveWorkers:       liveWorkers,
			LastWorkerSeenAt:  lastSeen,
			ChecksReferencing: checksReferencing[slug],
			Jobs:              stats.jobs,
			JobsOverdue:       stats.overdue,
			OldestOverdueAt:   stats.oldestOverdueAt,
		}
		row.Ghost = (row.Jobs > 0 || row.ChecksReferencing > 0) && row.LiveWorkers == 0

		if row.Ghost {
			ghostCount++
		}

		rows = append(rows, row)
	}

	return &RegionHealthReport{
		Regions:     rows,
		GhostCount:  ghostCount,
		GeneratedAt: now,
	}, nil
}

// declaredRegionSlugs is the set of slugs in the `regions` system parameter.
func (s *Service) declaredRegionSlugs(ctx context.Context) (map[string]bool, error) {
	defs, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("get global regions: %w", err)
	}

	declared := make(map[string]bool, len(defs))
	for i := range defs {
		if defs[i].Slug != "" {
			declared[defs[i].Slug] = true
		}
	}

	return declared, nil
}

// regionCheckReferenceCounts scans every non-deleted check's `regions` array
// and returns, per slug, the number of distinct checks naming it. A slug
// repeated within one check's own array (API misuse) is de-duplicated so it
// never inflates the count.
func (s *Service) regionCheckReferenceCounts(ctx context.Context) (map[string]int, error) {
	var rows []checkRegionsRow

	if err := s.db.DB().NewSelect().
		TableExpr("checks").
		ColumnExpr("regions").
		Where("deleted_at IS NULL").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list check regions: %w", err)
	}

	counts := make(map[string]int)

	for _, row := range rows {
		seen := make(map[string]bool, len(row.Regions))

		for _, slug := range row.Regions {
			if slug == "" || seen[slug] {
				continue
			}

			seen[slug] = true
			counts[slug]++
		}
	}

	return counts, nil
}

// regionJobStatsBySlug scans every check_job carrying a non-NULL region and
// returns, per slug, the job count, overdue count and oldest overdue
// scheduledAt. NULL-region (any-region) jobs are excluded by the query.
func (s *Service) regionJobStatsBySlug(ctx context.Context, now time.Time) (map[string]regionJobStats, error) {
	var rows []checkJobRegionRow

	if err := s.db.DB().NewSelect().
		TableExpr("check_jobs").
		ColumnExpr("region, scheduled_at").
		Where("region IS NOT NULL").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list check job regions: %w", err)
	}

	stats := make(map[string]regionJobStats)

	for _, row := range rows {
		if row.Region == nil || *row.Region == "" {
			continue
		}

		entry := stats[*row.Region]
		entry.jobs++

		if row.ScheduledAt != nil && row.ScheduledAt.Before(now) {
			entry.overdue++

			if entry.oldestOverdueAt == nil || row.ScheduledAt.Before(*entry.oldestOverdueAt) {
				entry.oldestOverdueAt = row.ScheduledAt
			}
		}

		stats[*row.Region] = entry
	}

	return stats, nil
}

// regionSlugUniverse unions every source of a region slug: declared regions,
// distinct checks.regions elements, distinct check_jobs.region, and distinct
// regions of non-deleted workers. A slug known only through a soft-deleted
// worker does not, on its own, extend the universe.
func regionSlugUniverse(
	declared map[string]bool, checksReferencing map[string]int, jobStats map[string]regionJobStats,
	workers []*models.Worker,
) []string {
	seen := make(map[string]bool, len(declared)+len(checksReferencing)+len(jobStats))

	for slug := range declared {
		seen[slug] = true
	}

	for slug := range checksReferencing {
		seen[slug] = true
	}

	for slug := range jobStats {
		seen[slug] = true
	}

	for _, worker := range workers {
		if worker.DeletedAt != nil || worker.Region == nil || *worker.Region == "" {
			continue
		}

		seen[*worker.Region] = true
	}

	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}

	sort.Strings(out)

	return out
}

// workerCoverageForSlug returns, for one slug, the count of live workers
// serving it and the latest last_active_at among every matching worker
// (deleted included). "Matching" is the scheduler's own prefix rule: the
// worker's announced region must have slug as a prefix — mirrors
// checkjobsvc.applyCloudRegionScope's `? LIKE region || '%'` and
// system.Service.LaneLoad's strings.HasPrefix(workerRegion, jobRegion).
func workerCoverageForSlug(workers []*models.Worker, slug string, liveCutoff time.Time) (int, *time.Time) {
	liveWorkers := 0

	var lastSeen *time.Time

	for _, worker := range workers {
		if worker.Region == nil || !strings.HasPrefix(*worker.Region, slug) {
			continue
		}

		if worker.LastActiveAt != nil && (lastSeen == nil || worker.LastActiveAt.After(*lastSeen)) {
			lastSeen = worker.LastActiveAt
		}

		if worker.DeletedAt == nil && worker.LastActiveAt != nil && !worker.LastActiveAt.Before(liveCutoff) {
			liveWorkers++
		}
	}

	return liveWorkers, lastSeen
}
