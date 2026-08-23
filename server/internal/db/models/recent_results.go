package models

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Errors returned by RecentResultsPerCheckFilter.Validate.
var (
	// ErrRecentResultsNoOrganization is returned when the filter names no
	// organization — every index on `results` leads with organization_uid.
	ErrRecentResultsNoOrganization = errors.New("recent results: organization uid is required")
	// ErrRecentResultsNoTiers is returned when the filter names no tier branch.
	ErrRecentResultsNoTiers = errors.New("recent results: at least one tier is required")
	// ErrRecentResultsMixedTier is returned when one branch names both raw and
	// a rollup tier (or names none at all).
	//
	// This is the guard that makes spec 2026-08-22-05's worst failure mode
	// unwritable. A per-check LATERAL/correlated fetch WITHOUT a tier
	// predicate does not fall back to the old plan — it becomes one sequential
	// scan of `results` PER CHECK, measured at 12 274 ms for a 20-check page
	// against 662 ms for the query it replaced. Both partial indexes on
	// `results` split on `period_type = 'raw'`, so a branch straddling the
	// split is implied by neither and can only be a scan.
	ErrRecentResultsMixedTier = errors.New(
		"recent results: each tier must sit entirely on one side of the raw/rollup index split")
	// ErrRecentResultsNoSince is returned when a tier carries no lower bound.
	ErrRecentResultsNoSince = errors.New("recent results: each tier needs a period_start lower bound")
	// ErrRecentResultsNoLimit is returned when no usable per-check budget is set.
	ErrRecentResultsNoLimit = errors.New("recent results: a positive default per-check limit is required")
)

// RecentResultsTier is ONE tier-aligned branch of a RecentResultsPerCheck
// fetch: which aggregation tiers to read, and how far back to read them.
//
// Each branch gets its own lower bound because the tiers live in different
// windows: raw only exists for the configured raw retention (~24 h), while the
// rollups reach back months. One shared bound would either truncate the rollups
// or make the raw branch scan far past where raw rows can exist.
type RecentResultsTier struct {
	// PeriodTypes are the tiers this branch reads. They MUST all sit on one
	// side of the raw/rollup split (see PeriodTypesTierSide) — the
	// implementation restates that side as an explicit predicate so both
	// Postgres and SQLite can use the matching partial index.
	PeriodTypes []string
	// Since is this branch's inclusive period_start lower bound.
	Since time.Time
}

// RecentResultsPerCheckFilter describes a "newest N rows per check, per tier"
// fetch. It is deliberately NOT expressible as a ListResultsFilter: that is a
// generic filtered list whose Limit is global, and a global limit over several
// checks is exactly the over-fetch-and-discard this query shape exists to
// remove (spec 2026-08-22-05).
//
// The budget is per check rather than one number for the batch because the
// caller sizes it from the check's own region fan-out: a 3-region check needs
// three times the rows of a single-region one to fill the same per-region
// chart, and a global figure can only be the max of those — which over-fetches
// for every other check on the page.
type RecentResultsPerCheckFilter struct {
	// OrganizationUID scopes every branch. Required.
	OrganizationUID string
	// CheckUIDs are the checks to fetch for. An empty list yields no rows.
	CheckUIDs []string
	// Tiers are the tier-aligned branches, UNION ALLed together. Required, and
	// each one must be single-sided.
	Tiers []RecentResultsTier
	// PerCheckLimits is the per-check row budget, per branch. A check absent
	// from the map uses DefaultPerCheckLimit.
	PerCheckLimits map[string]int
	// DefaultPerCheckLimit is the budget for a check with no explicit entry.
	// Required (> 0) — an unbounded branch defeats the whole point.
	DefaultPerCheckLimit int
}

// LimitFor returns the per-branch row budget for one check.
func (f *RecentResultsPerCheckFilter) LimitFor(checkUID string) int {
	if limit, ok := f.PerCheckLimits[checkUID]; ok && limit > 0 {
		return limit
	}

	return f.DefaultPerCheckLimit
}

// Validate rejects a filter no dialect may execute. The mixed-tier rule is the
// important one: see ErrRecentResultsMixedTier.
func (f *RecentResultsPerCheckFilter) Validate() error {
	if f.OrganizationUID == "" {
		return ErrRecentResultsNoOrganization
	}

	if f.DefaultPerCheckLimit <= 0 {
		return ErrRecentResultsNoLimit
	}

	if len(f.Tiers) == 0 {
		return ErrRecentResultsNoTiers
	}

	for i := range f.Tiers {
		tier := &f.Tiers[i]

		if PeriodTypesTierSide(tier.PeriodTypes) == PeriodTierMixed {
			return fmt.Errorf("%w: %v", ErrRecentResultsMixedTier, tier.PeriodTypes)
		}

		if tier.Since.IsZero() {
			return fmt.Errorf("%w: %v", ErrRecentResultsNoSince, tier.PeriodTypes)
		}
	}

	return nil
}

// resultBlobColumns are the two JSON columns RecentResultsPerCheck never
// projects: the response-time chart plots duration/status only, and these are
// by far the widest part of a results row (spec 2026-07-24-02 §5).
var resultBlobColumns = map[string]struct{}{"metrics": {}, "output": {}}

// ResultColumnsWithoutBlobs returns every persisted column of Result except the
// two blobs, in struct-field order, qualified with the given table alias
// ("" for none).
//
// It is derived from the model's own bun tags rather than transcribed, so a
// column added to Result reaches this projection automatically instead of
// silently scanning as a zero value. TestResultColumnsWithoutBlobs pins that.
func ResultColumnsWithoutBlobs(alias string) []string {
	fields := reflect.VisibleFields(reflect.TypeOf(Result{}))
	columns := make([]string, 0, len(fields))

	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}

	for _, field := range fields {
		name, ok := resultColumnName(field)
		if !ok {
			continue
		}

		if _, blob := resultBlobColumns[name]; blob {
			continue
		}

		columns = append(columns, prefix+name)
	}

	return columns
}

// resultColumnName extracts the persisted column name from a Result field's bun
// tag, reporting false for transient (`bun:"-"`) and scan-only fields (those
// with a `scanonly` option, which no INSERT/SELECT of the base table produces).
func resultColumnName(field reflect.StructField) (string, bool) {
	tag, ok := field.Tag.Lookup("bun")
	if !ok || tag == "-" {
		return "", false
	}

	parts := strings.Split(tag, ",")

	name := strings.TrimSpace(parts[0])
	if name == "" || name == "-" {
		return "", false
	}

	for _, option := range parts[1:] {
		if strings.TrimSpace(option) == "scanonly" {
			return "", false
		}
	}

	return name, true
}
