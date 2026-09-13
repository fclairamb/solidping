package checkerdef

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// periodBackfillTypesMarker matches the `PERIOD_BACKFILL_TYPES:` comment line
// both dialects' 021_v0_28_0.up.sql carry in their period-below-floor-backfill
// section (spec 2026-09-11-07) — a comma-separated list of check types.
var periodBackfillTypesMarker = regexp.MustCompile(`PERIOD_BACKFILL_TYPES:\s*([a-z0-9_, ]+)`)

// typesDeclaringMinPeriod returns, sorted, every checkerdef type whose meta
// declares a MinPeriod above the global floor.
func typesDeclaringMinPeriod() []string {
	var types []string

	for _, meta := range ListCheckTypeMetas() {
		if meta.MinPeriod > 0 {
			types = append(types, string(meta.Type))
		}
	}

	sort.Strings(types)

	return types
}

// parsePeriodBackfillTypesMarker extracts and sorts the type list from a
// migration file's PERIOD_BACKFILL_TYPES marker comment.
func parsePeriodBackfillTypesMarker(t *testing.T, path string) []string {
	t.Helper()
	r := require.New(t)

	content, err := os.ReadFile(path)
	r.NoError(err)

	match := periodBackfillTypesMarker.FindStringSubmatch(string(content))
	r.NotEmpty(match, "%s must carry a PERIOD_BACKFILL_TYPES marker comment", path)

	rawTypes := strings.Split(match[1], ",")
	types := make([]string, 0, len(rawTypes))

	for _, raw := range rawTypes {
		types = append(types, strings.TrimSpace(raw))
	}

	sort.Strings(types)

	return types
}

// TestPeriodBackfillTypeListMatchesCheckerdef pins the period-below-floor
// backfill's hardcoded type list, in BOTH dialects' 021_v0_28_0.up.sql, against
// checkerdef's own set of types declaring a MinPeriod. The SQL can't read Go
// metadata, so a future check type with a floor could otherwise be silently
// missed by the backfill — this test fails the build the moment checkerdef and
// the migration's type list disagree.
//
// The migration deliberately lists every floor-declaring type, including ones
// (js, browser) whose floor sits at or below the flat 1-minute fingerprint the
// backfill predicate matches on, so their branch of the migration's WHERE
// clause never actually updates a row. That is what makes this parity
// assertion meaningful: it is pinned against the FULL set, not just the
// subset that happens to need backfilling today.
func TestPeriodBackfillTypeListMatchesCheckerdef(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	want := typesDeclaringMinPeriod()
	r.NotEmpty(want, "sanity: checkerdef must declare at least one type with a MinPeriod")

	for _, path := range []string{
		"../../db/postgres/migrations/021_v0_28_0.up.sql",
		"../../db/sqlite/migrations/021_v0_28_0.up.sql",
	} {
		got := parsePeriodBackfillTypesMarker(t, path)
		r.Equal(want, got, "%s: PERIOD_BACKFILL_TYPES must match every checkerdef type with a MinPeriod", path)
	}
}
