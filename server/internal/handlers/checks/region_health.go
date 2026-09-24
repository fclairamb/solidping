package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// RegionHealthRow is one region's ghost-detection summary (spec 2026-08-24-09)
// — a single row for every region seen anywhere: in the declared `regions`
// system parameter, in a check's `regions` array, in a `check_jobs.region`, in
// a worker's announced region, or in an org agent's bound region.
//
// Cloud regions are global: one row per slug. Private regions (`@<slug>`) are
// org-relative — the org is implicit in the row's organization_uid — so they
// get one row per (organization, slug): `@paris` in org A and `@paris` in org
// B are two unrelated regions and are never merged (spec 2026-09-25-01).
type RegionHealthRow struct {
	Slug string `json:"slug"`
	// Organization is the owning org's slug for a private (`@`) region, and
	// empty for a cloud region.
	Organization string `json:"organization,omitempty"`
	Declared     bool   `json:"declared"`
	// LiveWorkers counts what can serve this region right now.
	//
	// Cloud region: non-deleted workers whose last_active_at is within
	// regions.WorkerLivenessWindow AND whose announced region matches this
	// slug under the scheduler's own prefix rule (workerRegion has slug as a
	// prefix) — the exact predicate checkjobsvc.applyCloudRegionScope uses to
	// claim a job. System agents are counted here, through their worker row.
	//
	// Private region: the org's active (not revoked) agents bound to exactly
	// this slug whose last_seen_at is within the same window — the predicate
	// ClaimJobsForAgent claims with (same org, exact region).
	LiveWorkers int `json:"liveWorkers"`
	// LastWorkerSeenAt dates when the region went dark, which is the first
	// thing an operator wants during triage. Cloud region: max last_active_at
	// across every matching worker, INCLUDING soft-deleted ones. Private
	// region: max last_seen_at across the org's non-deleted agents bound to
	// the slug, INCLUDING revoked ones.
	LastWorkerSeenAt *time.Time `json:"lastWorkerSeenAt"`
	// ChecksReferencing counts distinct, non-deleted checks whose `regions`
	// array names this slug (for a private region: only the owning org's).
	ChecksReferencing int `json:"checksReferencing"`
	// Jobs counts check_jobs rows carrying this slug (for a private region:
	// only the owning org's). NULL-region (any-region) jobs are excluded —
	// they are claimable by every cloud worker by construction and never
	// belong to a specific slug.
	Jobs int `json:"jobs"`
	// JobsOverdue is the subset of Jobs whose scheduledAt has already passed.
	JobsOverdue int `json:"jobsOverdue"`
	// OldestOverdueAt is the earliest scheduledAt among JobsOverdue, nil when
	// there are none.
	OldestOverdueAt *time.Time `json:"oldestOverdueAt"`
	// Ghost is true when something depends on this region (a job or a check
	// reference) and nothing live can serve it. A declared region with zero
	// live workers and zero references is dark but unused — reported with
	// LiveWorkers: 0 but Ghost: false, since nothing is actually stranded.
	Ghost bool `json:"ghost"`
}

// IsPrivate reports whether the row is an org-scoped private region.
func (r *RegionHealthRow) IsPrivate() bool {
	return regions.IsPrivateRegion(r.Slug)
}

// RegionHealthReport is the body of GET /api/v1/system/regions/health.
type RegionHealthReport struct {
	Regions     []RegionHealthRow `json:"regions"`
	GhostCount  int               `json:"ghostCount"`
	GeneratedAt time.Time         `json:"generatedAt"`
}

// regionKey identifies one region row. Cloud slugs are global, so their
// orgUID is always empty; a private (`@`) slug is org-relative, so it is keyed
// by the organization_uid of the row it came from.
type regionKey struct {
	orgUID string
	slug   string
}

// newRegionKey builds the key for a slug read off a row owned by orgUID. A
// cloud slug drops the org — `eu-1` is the same region for every org.
func newRegionKey(orgUID, slug string) regionKey {
	if !regions.IsPrivateRegion(slug) {
		return regionKey{slug: slug}
	}

	return regionKey{orgUID: orgUID, slug: slug}
}

// checkRegionsRow is the narrow projection RegionHealth scans off `checks` —
// only the columns the per-region reference count needs.
type checkRegionsRow struct {
	OrganizationUID string   `bun:"organization_uid"`
	Regions         []string `bun:"regions,type:text[],array"`
}

// checkJobRegionRow is the narrow projection RegionHealth scans off
// `check_jobs` — only the columns the per-region job stats need.
type checkJobRegionRow struct {
	OrganizationUID string     `bun:"organization_uid"`
	Region          *string    `bun:"region"`
	ScheduledAt     *time.Time `bun:"scheduled_at"`
}

// orgAgentRow is the narrow projection RegionHealth scans off `agents` — only
// the columns private-region liveness needs.
type orgAgentRow struct {
	OrganizationUID string     `bun:"organization_uid"`
	Region          string     `bun:"region"`
	Status          string     `bun:"status"`
	LastSeenAt      *time.Time `bun:"last_seen_at"`
}

// orgSlugRow is the narrow projection RegionHealth scans off `organizations`
// to name the org of each private row.
type orgSlugRow struct {
	UID  string `bun:"uid"`
	Slug string `bun:"slug"`
}

// regionJobStats accumulates the job-side counters for one region while
// scanning checkJobRegionRow.
type regionJobStats struct {
	jobs            int
	overdue         int
	oldestOverdueAt *time.Time
}

// RegionHealth computes the ghost-detection report (spec 2026-08-24-09): one
// row per region seen anywhere, with the live-worker count and reference
// counts needed to tell a genuinely stranded region apart from a merely
// unused one. This is the read-side companion of MigrateRegion (spec
// 2026-08-24-08) and the function the hourly watchdog (spec 2026-08-24-10)
// calls too — the detection rule lives exactly once.
//
// A handful of bounded, dialect-neutral scans (checks, check_jobs, workers,
// org agents, and the slugs of the orgs owning a private region) feed a single
// in-memory aggregation — no per-slug query, no per-check loop. This mirrors
// system.Service.LaneLoad, which aggregates the same kind of worker/region
// data the same way for the same reason.
//
// Cloud rows are served by workers (system agents included, through their
// worker row). Private rows are served by the org's agents, read from
// `agents.last_seen_at` — an org agent's worker row never carries its region
// (the workers.region check constraint forbids the `@` prefix) — see spec
// 2026-09-25-01.
func (s *Service) RegionHealth(ctx context.Context) (*RegionHealthReport, error) {
	now := s.now()

	declared, err := s.declaredRegionSlugs(ctx)
	if err != nil {
		return nil, err
	}

	checksReferencing, err := s.regionCheckReferenceCounts(ctx)
	if err != nil {
		return nil, err
	}

	jobStats, err := s.regionJobStatsByKey(ctx, now)
	if err != nil {
		return nil, err
	}

	workers, err := s.workersForRegionHealth(ctx)
	if err != nil {
		return nil, err
	}

	agents, err := s.orgAgentsForRegionHealth(ctx)
	if err != nil {
		return nil, err
	}

	universe := regionKeyUniverse(declared, checksReferencing, jobStats, workers, agents)

	orgSlugs, err := s.orgSlugsForKeys(ctx, universe)
	if err != nil {
		return nil, err
	}

	liveCutoff := now.Add(-regions.WorkerLivenessWindow)

	rows := make([]RegionHealthRow, 0, len(universe))
	ghostCount := 0

	for i := range universe {
		key := universe[i]

		row := RegionHealthRow{
			Slug:              key.slug,
			Declared:          key.orgUID == "" && declared[key.slug],
			ChecksReferencing: checksReferencing[key],
			Jobs:              jobStats[key].jobs,
			JobsOverdue:       jobStats[key].overdue,
			OldestOverdueAt:   jobStats[key].oldestOverdueAt,
		}

		if key.orgUID == "" {
			row.LiveWorkers, row.LastWorkerSeenAt = workerCoverageForSlug(workers, key.slug, liveCutoff)
		} else {
			row.LiveWorkers, row.LastWorkerSeenAt = agentCoverageForKey(agents, key, liveCutoff)
			row.Organization = orgSlugOrUID(orgSlugs, key.orgUID)
		}

		row.Ghost = (row.Jobs > 0 || row.ChecksReferencing > 0) && row.LiveWorkers == 0

		if row.Ghost {
			ghostCount++
		}

		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Slug != rows[j].Slug {
			return rows[i].Slug < rows[j].Slug
		}

		return rows[i].Organization < rows[j].Organization
	})

	return &RegionHealthReport{
		Regions:     rows,
		GhostCount:  ghostCount,
		GeneratedAt: now,
	}, nil
}

// orgSlugOrUID names a private row's org by slug. When the owning org row is
// gone entirely it falls back to the UID rather than emit a private row with
// no organization, which would read as a cloud one.
func orgSlugOrUID(orgSlugs map[string]string, orgUID string) string {
	if slug := orgSlugs[orgUID]; slug != "" {
		return slug
	}

	return orgUID
}

// workersForRegionHealth loads every worker, soft-deleted included (they date
// lastWorkerSeenAt).
func (s *Service) workersForRegionHealth(ctx context.Context) ([]*models.Worker, error) {
	var workers []*models.Worker

	if err := s.db.DB().NewSelect().
		Model(&workers).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list workers for region health: %w", err)
	}

	return workers, nil
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
// and returns, per region key, the number of distinct checks naming it. A
// private slug is counted under the check's own org. A slug repeated within
// one check's own array (API misuse) is de-duplicated so it never inflates
// the count.
func (s *Service) regionCheckReferenceCounts(ctx context.Context) (map[regionKey]int, error) {
	var rows []checkRegionsRow

	if err := s.db.DB().NewSelect().
		TableExpr("checks").
		ColumnExpr("organization_uid, regions").
		Where("deleted_at IS NULL").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list check regions: %w", err)
	}

	counts := make(map[regionKey]int)

	for i := range rows {
		row := &rows[i]
		seen := make(map[string]bool, len(row.Regions))

		for _, slug := range row.Regions {
			if slug == "" || seen[slug] {
				continue
			}

			seen[slug] = true
			counts[newRegionKey(row.OrganizationUID, slug)]++
		}
	}

	return counts, nil
}

// regionJobStatsByKey scans every check_job carrying a non-NULL region and
// returns, per region key, the job count, overdue count and oldest overdue
// scheduledAt. A private slug is aggregated under the job's own org.
// NULL-region (any-region) jobs are excluded by the query.
func (s *Service) regionJobStatsByKey(ctx context.Context, now time.Time) (map[regionKey]regionJobStats, error) {
	var rows []checkJobRegionRow

	if err := s.db.DB().NewSelect().
		TableExpr("check_jobs").
		ColumnExpr("organization_uid, region, scheduled_at").
		Where("region IS NOT NULL").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list check job regions: %w", err)
	}

	stats := make(map[regionKey]regionJobStats)

	for i := range rows {
		row := &rows[i]
		if row.Region == nil || *row.Region == "" {
			continue
		}

		key := newRegionKey(row.OrganizationUID, *row.Region)

		entry := stats[key]
		entry.jobs++

		if row.ScheduledAt != nil && row.ScheduledAt.Before(now) {
			entry.overdue++

			if entry.oldestOverdueAt == nil || row.ScheduledAt.Before(*entry.oldestOverdueAt) {
				entry.oldestOverdueAt = row.ScheduledAt
			}
		}

		stats[key] = entry
	}

	return stats, nil
}

// orgAgentsForRegionHealth loads every non-deleted ORG agent (any status) with
// just the columns private-region liveness needs. System agents are excluded
// on purpose: they serve a cloud region and are already counted through the
// worker row their connection registers, so reading them here too would count
// them twice.
func (s *Service) orgAgentsForRegionHealth(ctx context.Context) ([]orgAgentRow, error) {
	var rows []orgAgentRow

	if err := s.db.DB().NewSelect().
		TableExpr("agents").
		ColumnExpr("organization_uid, region, status, last_seen_at").
		Where("organization_uid IS NOT NULL").
		Where("deleted_at IS NULL").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list org agents for region health: %w", err)
	}

	out := rows[:0]

	for i := range rows {
		row := rows[i]
		// An org agent is always bound to an org-relative `@<slug>`; anything
		// else cannot be keyed to a private row, so it is skipped rather than
		// leaking into the global cloud namespace.
		if row.OrganizationUID == "" || !regions.IsPrivateRegion(row.Region) {
			continue
		}

		out = append(out, row)
	}

	return out, nil
}

// orgSlugsForKeys resolves the slug of every org owning a private key, in one
// query. Soft-deleted orgs are resolved too: their leftover rows still need a
// name.
func (s *Service) orgSlugsForKeys(ctx context.Context, keys []regionKey) (map[string]string, error) {
	uidSet := make(map[string]bool)

	for i := range keys {
		if keys[i].orgUID != "" {
			uidSet[keys[i].orgUID] = true
		}
	}

	slugs := make(map[string]string, len(uidSet))
	if len(uidSet) == 0 {
		return slugs, nil
	}

	uids := make([]string, 0, len(uidSet))
	for uid := range uidSet {
		uids = append(uids, uid)
	}

	var rows []orgSlugRow

	if err := s.db.DB().NewSelect().
		TableExpr("organizations").
		ColumnExpr("uid, slug").
		Where("uid IN (?)", bun.List(uids)).
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list organization slugs for region health: %w", err)
	}

	for i := range rows {
		slugs[rows[i].UID] = rows[i].Slug
	}

	return slugs, nil
}

// regionKeyUniverse unions every source of a region: declared regions,
// distinct checks.regions elements, distinct check_jobs.region, distinct
// regions of non-deleted workers, and the bound region of every loaded org
// agent. A slug known only through a soft-deleted worker does not, on its
// own, extend the universe. Keys are returned sorted by (slug, org UID).
func regionKeyUniverse(
	declared map[string]bool, checksReferencing map[regionKey]int, jobStats map[regionKey]regionJobStats,
	workers []*models.Worker, agents []orgAgentRow,
) []regionKey {
	seen := make(map[regionKey]bool, len(declared)+len(checksReferencing)+len(jobStats))

	for slug := range declared {
		seen[regionKey{slug: slug}] = true
	}

	for key := range checksReferencing {
		seen[key] = true
	}

	for key := range jobStats {
		seen[key] = true
	}

	for _, worker := range workers {
		if worker.DeletedAt != nil || worker.Region == nil || *worker.Region == "" {
			continue
		}

		// A worker row is never org-scoped, so it can only name a cloud
		// region (the workers.region check constraint forbids `@` anyway).
		if regions.IsPrivateRegion(*worker.Region) {
			continue
		}

		seen[regionKey{slug: *worker.Region}] = true
	}

	for i := range agents {
		seen[regionKey{orgUID: agents[i].OrganizationUID, slug: agents[i].Region}] = true
	}

	out := make([]regionKey, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].slug != out[j].slug {
			return out[i].slug < out[j].slug
		}

		return out[i].orgUID < out[j].orgUID
	})

	return out
}

// workerCoverageForSlug returns, for one cloud slug, the count of live workers
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

// agentCoverageForKey returns, for one private (org, slug) key, the count of
// that org's live agents serving it and the latest last_seen_at among every
// one of its agents bound to the slug (revoked included).
//
// Matching is EXACT region equality within the same org — the predicate
// ClaimJobsForAgent claims with — never the cloud prefix rule. Live means
// status active (a revoked agent can no longer authenticate, let alone claim)
// and last_seen_at within the liveness window, inclusive.
func agentCoverageForKey(agents []orgAgentRow, key regionKey, liveCutoff time.Time) (int, *time.Time) {
	liveWorkers := 0

	var lastSeen *time.Time

	for i := range agents {
		agent := &agents[i]
		if agent.OrganizationUID != key.orgUID || agent.Region != key.slug {
			continue
		}

		if agent.LastSeenAt == nil {
			continue
		}

		if lastSeen == nil || agent.LastSeenAt.After(*lastSeen) {
			lastSeen = agent.LastSeenAt
		}

		if agent.Status == models.AgentStatusActive && !agent.LastSeenAt.Before(liveCutoff) {
			liveWorkers++
		}
	}

	return liveWorkers, lastSeen
}
