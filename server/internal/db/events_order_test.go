package db_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListEventsOrdersByUIDTieBreakInBothDialects is a STRUCTURAL guard, and
// deliberately so.
//
// The behavioral test (testEventsKeysetPaginationTieBreak) pages through
// twenty events that share one created_at and asserts none is skipped — but
// both engines happen to return a stable order for a small table even WITHOUT
// the tie-break, so it passes either way at that size and proves nothing on
// its own. The guarantee only appears once the plan changes: a parallel seq
// scan, a different index, a larger table.
//
// So this asserts the contract directly instead: the keyset predicate
// `created_at < ? OR (created_at = ? AND uid < ?)` is only correct when the
// ORDER BY makes the tie deterministic, and both dialects must spell it the
// same way. Deleting the tie-break fails here immediately rather than
// producing an audit log that silently loses rows in production.
func TestListEventsOrdersByUIDTieBreakInBothDialects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The ORDER BY of the ListEvents query, in either dialect's store.
	orderRE := regexp.MustCompile(`Order\("created_at DESC",\s*"uid DESC"\)`)

	for _, source := range []string{
		filepath.Join("postgres", "postgres.go"),
		filepath.Join("sqlite", "sqlite.go"),
	} {
		content, err := os.ReadFile(filepath.Clean(source))
		r.NoError(err)

		// Slice out the ListEvents body: the file holds dozens of other
		// created_at orderings that are none of this test's business.
		text := listEventsBody(t, string(content))

		// Positive control: this really is the ListEvents body, so a passing
		// assertion below cannot mean "we sliced the wrong thing".
		r.Containsf(text, "created_at = ? AND uid < ?",
			"%s must use the two-column keyset predicate", source)
		r.Containsf(text, "filter.CursorTimestamp", "%s slice must be the real ListEvents", source)

		r.Truef(orderRE.MatchString(text),
			"%s must order events by (created_at DESC, uid DESC): without the uid tie-break "+
				"the keyset cursor can skip rows that share a created_at, which an audit "+
				"trail produces constantly (bulk applies, SSO backfills, failed-login bursts)",
			source)

		// And the single-column ordering must not linger inside this query.
		r.NotContainsf(text, `Order("created_at DESC").`,
			"%s still contains a single-column event ordering", source)
	}
}

// listEventsBody slices the ListEvents function out of a store source file.
func listEventsBody(t *testing.T, source string) string {
	t.Helper()

	start := strings.Index(source, "func (s *Service) ListEvents(")
	require.GreaterOrEqual(t, start, 0, "ListEvents must exist in this file")

	rest := source[start+1:]

	end := strings.Index(rest, "\nfunc ")
	require.GreaterOrEqual(t, end, 0, "ListEvents must be followed by another function")

	return rest[:end]
}
