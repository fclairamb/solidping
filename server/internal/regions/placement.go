package regions

import "slices"

// DefaultAutoRegionCount is N for an automatically placed check when nobody
// asked for another value (spec 2026-09-25-06, resolved question 2): two
// regions, capped down by the number of eligible regions and by the org's
// checks-per-minute limit.
const DefaultAutoRegionCount = 2

// MaxAutoRegionCount bounds regionCount. Far above any real region list; it
// only stops a typo (regionCount: 2000) from being taken literally.
const MaxAutoRegionCount = 50

// CandidateOrder is the order automatic placement considers regions in: the
// org's default_regions, then the system default_regions, then every other
// declared region. Duplicates are dropped (first position wins) and private
// (`@`) regions never appear — auto placement never places into one.
//
// It is the base order for every check of the org; the pool, the required
// capabilities and region health only ever FILTER it, never reorder it, which
// is what makes placement deterministic.
func CandidateOrder(orgDefaults, systemDefaults []string, declared []RegionDefinition) []string {
	out := make([]string, 0, len(orgDefaults)+len(systemDefaults)+len(declared))
	seen := make(map[string]bool, cap(out))

	add := func(slug string) {
		if slug == "" || IsPrivateRegion(slug) || seen[slug] {
			return
		}

		seen[slug] = true

		out = append(out, slug)
	}

	for _, slug := range orgDefaults {
		add(slug)
	}

	for _, slug := range systemDefaults {
		add(slug)
	}

	for i := range declared {
		add(declared[i].Slug)
	}

	return out
}

// PlacementInput is everything Place reads. Every field is data, no I/O: the
// same input always yields the same placement.
type PlacementInput struct {
	// Candidates is the base order (CandidateOrder).
	Candidates []string
	// Pool restricts the candidates to these slugs. Empty means any.
	Pool []string
	// Required are capability names the check needs (CapabilityBrowser for a
	// browser check, CapabilityIPv6 for an IPv6-pinned target). A region whose
	// verdict for one of them is an explicit CapabilityNo is not eligible;
	// "unknown" stays eligible, exactly as the advisory warnings treat it.
	Required []string
	// Capabilities is the annotated region index (CapabilityIndex). A region
	// missing from it reads as unknown.
	Capabilities map[string]RegionDefinition
	// Healthy is the set of regions that can run a job right now.
	Healthy map[string]bool
	// Current is the check's current placement. A current region that is
	// still eligible (and healthy) is kept: a re-evaluation never moves a check
	// that is fine where it is.
	Current []string
	// Count is N.
	Count int
}

// Eligible is the candidate order filtered by the pool and the required
// capabilities (not by health: an unhealthy region is still eligible, only
// less preferred).
func Eligible(input *PlacementInput) []string {
	out := make([]string, 0, len(input.Candidates))

	for _, slug := range input.Candidates {
		if len(input.Pool) > 0 && !slices.Contains(input.Pool, slug) {
			continue
		}

		if !hasCapabilities(input.Capabilities, slug, input.Required) {
			continue
		}

		out = append(out, slug)
	}

	return out
}

// hasCapabilities reports whether no required capability is an explicit "no"
// in the region.
func hasCapabilities(index map[string]RegionDefinition, slug string, required []string) bool {
	def, known := index[slug]
	if !known {
		return true
	}

	for _, name := range required {
		if def.CapabilityFor(name) == CapabilityNo {
			return false
		}
	}

	return true
}

// Place chooses up to Count regions out of the eligible candidates:
//
//  1. the current regions that are still eligible and healthy, in their
//     current order (stability: nothing moves without a reason);
//  2. then healthy eligible candidates in candidate order;
//  3. then, only when still short, unhealthy eligible candidates in candidate
//     order — so a check asked to run from two regions runs from two regions
//     when one of them recovers, instead of silently shrinking.
//
// When no eligible region is healthy at all (a fresh install, a test
// database, an instance whose workers have not beaten yet) health says
// nothing, so it is ignored and the plain candidate order wins.
//
// The result is empty only when there is no eligible region.
func Place(input *PlacementInput) []string {
	eligible := Eligible(input)
	count := min(input.Count, len(eligible))

	if count <= 0 {
		return nil
	}

	anyHealthy := false

	for _, slug := range eligible {
		if input.Healthy[slug] {
			anyHealthy = true

			break
		}
	}

	preferred := func(slug string) bool { return !anyHealthy || input.Healthy[slug] }

	out := make([]string, 0, count)

	take := func(slug string) {
		if len(out) < count && !slices.Contains(out, slug) {
			out = append(out, slug)
		}
	}

	for _, slug := range input.Current {
		if slices.Contains(eligible, slug) && preferred(slug) {
			take(slug)
		}
	}

	for _, slug := range eligible {
		if preferred(slug) {
			take(slug)
		}
	}

	for _, slug := range eligible {
		take(slug)
	}

	return out
}

// Replace swaps one region out of a placement: `from` is replaced, in place,
// by the first candidate (in order) that is healthy and not already used. It
// is the re-placement step of a region going dark (spec 2026-09-25-06 A2).
//
// It returns the new placement and the region moved to; ok is false when
// `from` is not in the placement or no healthy candidate is left, in which
// case the placement is returned unchanged — the check then goes stale like a
// pinned one.
func Replace(current, candidates []string, healthy map[string]bool, from string) ([]string, string, bool) {
	index := slices.Index(current, from)
	if index < 0 {
		return current, "", false
	}

	for _, slug := range candidates {
		if slug == from || !healthy[slug] || slices.Contains(current, slug) {
			continue
		}

		next := slices.Clone(current)
		next[index] = slug

		return next, slug, true
	}

	return current, "", false
}
