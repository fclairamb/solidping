package scheduling

import (
	"hash/fnv"
	"sort"
	"time"
)

// Phase-locked rescheduling (spec 2026-07-05-08 D1): a check job's next
// scheduled_at is derived from a deterministic phase rather than from
// "scheduled_at + period" (which drifts under lateness) or "now + period"
// (which lockstep-herds every late job onto the same claim-batch timestamp
// after a restart). The phase depends only on data every worker already has
// (the job's attached check), so it is reproducible across processes with no
// shared state and no schema change.
//
// jitter = hash(check.UID) mod basePeriod   — spreads different checks across
//
//	the base period instead of all ticking on the wall-clock minute boundary.
//
// phase  = jitter + i × basePeriod           — region i always fires
//
//	i × basePeriod after region 0, the round-robin leveling the user expects.
//
// next   = smallest t > now with t.Unix() ≡ phase (mod jobPeriod)
//
// A late or missed run resumes at the next phase-aligned tick: no drift, no
// lockstep, self-healing after any outage length.

// JitterFor returns a deterministic, check-scoped offset in [0, basePeriod)
// derived from checkUID. Different checks get different jitter (spreading
// them across the base period instead of herding on the same tick);  the same
// check always gets the same jitter (stable across processes and restarts).
//
// basePeriod <= 0 returns 0 (no spreading possible).
func JitterFor(checkUID string, basePeriod time.Duration) time.Duration {
	if basePeriod <= 0 {
		return 0
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(checkUID)) // fnv.Write never errors

	return time.Duration(h.Sum64() % uint64(basePeriod))
}

// RegionIndex returns the index of region within a sorted copy of regions,
// so callers always agree on the same index regardless of the order regions
// was originally supplied in (reconcile and the worker's release path must
// compute the same index for the same region set). Returns 0 when region is
// nil, regions is empty, or region is not found in regions (stale job about
// to be reconciled away, or a no-region job) — these are the edge cases
// called out by spec 2026-07-05-08 D1, and 0 degrades to "jitter-only phase"
// rather than an error.
func RegionIndex(region *string, regions []string) int {
	if region == nil || len(regions) == 0 {
		return 0
	}

	sorted := make([]string, len(regions))
	copy(sorted, regions)
	sort.Strings(sorted)

	for i, r := range sorted {
		if r == *region {
			return i
		}
	}

	return 0
}

// NextAligned returns the smallest instant strictly after now whose Unix
// second is congruent to this job's phase modulo jobPeriod. basePeriod is the
// check's pre-split period (used only to derive the jitter and the region
// spacing); jobPeriod is the actual period this job fires on (the post-split
// period for multi-region checks, or basePeriod itself for single/no-region
// jobs).
//
// basePeriod <= 0 or jobPeriod <= 0 falls back to now.Add(jobPeriod) — this
// should not happen in practice (reconcile never produces a zero period) but
// avoids a divide-by-zero / infinite-loop degenerate case.
func NextAligned(
	now time.Time,
	basePeriod time.Duration,
	jobPeriod time.Duration,
	checkUID string,
	region *string,
	regions []string,
) time.Time {
	if basePeriod <= 0 || jobPeriod <= 0 {
		return now.Add(jobPeriod)
	}

	jitter := JitterFor(checkUID, basePeriod)
	i := RegionIndex(region, regions)
	phase := (jitter + time.Duration(i)*basePeriod) % jobPeriod

	periodSecs := int64(jobPeriod / time.Second)
	if periodSecs <= 0 {
		// Sub-second period: fall back to plain jitter-from-now, phase
		// alignment below the second is not meaningful here.
		return now.Add(jobPeriod)
	}

	phaseSecs := int64(phase / time.Second)
	nowSecs := now.Unix()

	// Smallest t > now with t ≡ phaseSecs (mod periodSecs).
	rem := nowSecs % periodSecs
	if rem < 0 {
		rem += periodSecs
	}

	delta := phaseSecs - rem
	if delta <= 0 {
		delta += periodSecs
	}

	next := now.Add(time.Duration(delta) * time.Second)
	// Guard against the rare boundary case where rounding to whole seconds
	// lands exactly on "now" (or before it, if now itself had a sub-second
	// component pushing nowSecs' remainder equal to phaseSecs): step forward
	// one more full period so callers always get a strictly-future tick.
	if !next.After(now) {
		next = next.Add(time.Duration(periodSecs) * time.Second)
	}

	return next
}
