package checks

import (
	"context"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// defaultCheckStatsTTL is how long a computed org stats snapshot is served
// before it is recomputed (spec 2026-08-02-06). The counters drive dashboard
// KPI tiles that already poll on a 30s cadence and are informational, so a
// minute of staleness is well within budget — and it means one aggregate
// query per org per minute no matter how many operators have the dashboard
// open.
//
// Membership-changing writes bust the cache explicitly (spec
// 2026-09-01-01): check create and delete — the two chokepoints
// (insertCheckResolvingSlugRace, Service.DeleteCheck) every write path
// (create, upsert, import, apply, clone) funnels through — call
// checkStatsCache.invalidate so the very next fetch recomputes instead of
// riding out the TTL. This closes the "dashboard stuck on the empty-org hero
// for up to a minute after the first check is created" gap. Everything else
// — enabled/disabled toggles, config edits, and status transitions from the
// worker pipeline — deliberately keeps riding the TTL: those never flip
// Total across zero, create/delete are rare and cheap places to bust, and
// wiring every field-level write would trade a minute of counter staleness
// for no real benefit.
const defaultCheckStatsTTL = time.Minute

// availability24hWindow is the trailing window Availability24h is computed
// over (spec 2026-08-26-09) — "24h Availability" on the dashboard tile.
const availability24hWindow = 24 * time.Hour

// CheckStatsResponse is the org-wide check aggregate served by
// GET /api/v1/orgs/{org}/checks/stats.
//
// Every field is computed by a SQL GROUP BY over the whole checks table, so
// unlike the dashboard's previous client-side counting it is unaffected by the
// list endpoint's 100-row page clamp.
//
// Scope: non-deleted, non-internal checks — the exact set the checks list
// shows by default. Total/ByStatus/Down/HardDown span enabled AND disabled
// checks (the dashboard's down tiles filter on status alone); Enabled and
// Disabled partition the same set by the enabled flag.
type CheckStatsResponse struct {
	// Total is Enabled + Disabled, i.e. every in-scope check.
	Total int `json:"total"`
	// Enabled counts checks with enabled = true.
	Enabled int `json:"enabled"`
	// Disabled counts checks with enabled = false.
	Disabled int `json:"disabled"`
	// ByStatus maps every known wire status name to its count. Every key is
	// always present (0 when empty) so clients can index it without guards.
	// Keys are the same tokens `check.status` carries in the list response.
	ByStatus map[string]int `json:"byStatus"`
	// Down counts checks the dashboard renders as failing. It mirrors the
	// frontend's isDownStatus (down | error | timeout); `error` and `timeout`
	// are result-level statuses a check-level status can never hold, so in
	// practice this equals ByStatus["down"].
	Down int `json:"down"`
	// HardDown counts checks failing hard, mirroring the frontend's
	// isHardDownStatus (down | error). Same reasoning as Down: today
	// HardDown == Down, and the dashboard's "timeouts only" banner variant is
	// therefore never reached. That equality is preserved on purpose — the
	// stats endpoint must report what the UI already reports, not a different
	// (even if arguably better) truth.
	HardDown int `json:"hardDown"`
	// Availability24h is 100*success/total over the trailing 24h window,
	// combining `hour` rollup and `raw` result rows (spec 2026-08-26-09) —
	// db.Service.GetOrgAvailability24h does the actual fold, in SQL, mirroring
	// models.RawAvailability's exclusion rules for the raw tier. nil when the
	// window has no countable data (empty or brand-new org): the dashboard
	// must render that as "no data", never as a fabricated 100%.
	Availability24h *float64 `json:"availability24h"`
}

// checkStatsEntry is one cached org snapshot.
type checkStatsEntry struct {
	stats      CheckStatsResponse
	computedAt time.Time
}

// checkStatsCache is the per-org in-memory stats cache. A plain
// mutex-guarded map: entries are tiny, orgs per process are few, and the
// hit path does no allocation beyond the response copy.
type checkStatsCache struct {
	mu      sync.Mutex
	entries map[string]checkStatsEntry
}

// checkStatusNames lists every wire status a check row can carry — the exact
// key set ByStatus is seeded with. models.CheckStatus.String() maps unknown
// column values to WireStatusUnknown, so this set is exhaustive.
func checkStatusNames() []string {
	return []string{
		models.WireStatusCreated,
		models.WireStatusUp,
		models.WireStatusDown,
		models.WireStatusValidating,
		models.WireStatusDegraded,
		models.WireStatusWarning,
		models.WireStatusUnknown,
	}
}

// isDownWireStatus mirrors the dashboard's isDownStatus helper
// (web/dash0/src/components/dashboard/dashboard-page.tsx).
func isDownWireStatus(status string) bool {
	return status == models.WireStatusDown ||
		status == models.WireStatusError ||
		status == models.WireStatusTimeout
}

// isHardDownWireStatus mirrors the dashboard's isHardDownStatus helper.
func isHardDownWireStatus(status string) bool {
	return status == models.WireStatusDown || status == models.WireStatusError
}

// GetCheckStats returns the org's aggregate check counters, served from a
// per-org cache with a defaultCheckStatsTTL lifetime. A miss runs exactly one
// GROUP BY query; the result is never assembled by listing checks.
func (s *Service) GetCheckStats(ctx context.Context, orgSlug string) (CheckStatsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckStatsResponse{}, ErrOrganizationNotFound
	}

	ttl := s.checkStatsTTL
	if ttl <= 0 {
		ttl = defaultCheckStatsTTL
	}

	if cached, ok := s.checkStats.get(org.UID, ttl); ok {
		return cached, nil
	}

	rows, err := s.db.GetCheckStatusCounts(ctx, org.UID)
	if err != nil {
		return CheckStatsResponse{}, err
	}

	now := s.now()

	availCounts, err := s.db.GetOrgAvailability24h(ctx, org.UID, now.Add(-availability24hWindow), now)
	if err != nil {
		return CheckStatsResponse{}, err
	}

	stats := foldCheckStatusCounts(rows)
	if pct, ok := availCounts.Pct(); ok {
		stats.Availability24h = &pct
	}

	s.checkStats.put(org.UID, stats)

	return stats, nil
}

// foldCheckStatusCounts turns the (status, enabled) histogram rows into the
// response shape. Split out from GetCheckStats so the mapping — the
// correctness-sensitive part, since it must agree with the dashboard's status
// helpers — is directly unit-testable without a database.
func foldCheckStatusCounts(rows []models.CheckStatusCount) CheckStatsResponse {
	names := checkStatusNames()

	byStatus := make(map[string]int, len(names))
	for _, name := range names {
		byStatus[name] = 0
	}

	stats := CheckStatsResponse{ByStatus: byStatus}

	for _, row := range rows {
		name := row.Status.String()
		byStatus[name] += row.Count
		stats.Total += row.Count

		if row.Enabled {
			stats.Enabled += row.Count
		} else {
			stats.Disabled += row.Count
		}

		if isDownWireStatus(name) {
			stats.Down += row.Count
		}

		if isHardDownWireStatus(name) {
			stats.HardDown += row.Count
		}
	}

	return stats
}

// get returns the org's cached stats when the entry is younger than ttl.
func (c *checkStatsCache) get(orgUID string, ttl time.Duration) (CheckStatsResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[orgUID]
	if !ok || time.Since(entry.computedAt) >= ttl {
		return CheckStatsResponse{}, false
	}

	// Hand back a copy of the map: the cached snapshot is shared with every
	// concurrent hit, and a caller mutating it would poison the cache.
	out := entry.stats
	out.ByStatus = make(map[string]int, len(entry.stats.ByStatus))

	for k, v := range entry.stats.ByStatus {
		out.ByStatus[k] = v
	}

	return out, true
}

// put stores a freshly computed snapshot for the org. The ByStatus map is
// copied on the way in as well as on the way out, so the caller that triggered
// the miss cannot mutate the entry it just seeded.
func (c *checkStatsCache) put(orgUID string, stats CheckStatsResponse) {
	stored := stats
	stored.ByStatus = make(map[string]int, len(stats.ByStatus))

	for k, v := range stats.ByStatus {
		stored.ByStatus[k] = v
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]checkStatsEntry)
	}

	c.entries[orgUID] = checkStatsEntry{stats: stored, computedAt: time.Now()}
}

// invalidate drops the org's cached snapshot, if any, so the next
// GetCheckStats call recomputes instead of serving a stale hit. Called from
// every service path that changes the set of in-scope checks — see the
// defaultCheckStatsTTL doc comment for which writes those are.
func (c *checkStatsCache) invalidate(orgUID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, orgUID)
}
