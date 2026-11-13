package jobtypes

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// queryFault is a bun query hook that fails every query whose SQL contains
// all of its fragments, by handing the driver an already canceled context.
// It lets a test name the one store call that breaks ("the claim UPDATE")
// instead of counting queries, and then assert what the job does about it.
type queryFault struct {
	fragments []string
	hits      atomic.Int64
	disarmed  atomic.Bool
}

// failQueries installs a queryFault on the database. Install it after the
// fixture is seeded: the hook is active from this call on.
func failQueries(bunDB *bun.DB, fragments ...string) *queryFault {
	fault := &queryFault{fragments: fragments}
	bunDB.AddQueryHook(fault)

	return fault
}

// BeforeQuery implements bun.QueryHook.
func (f *queryFault) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if f.disarmed.Load() {
		return ctx
	}

	for _, fragment := range f.fragments {
		if !strings.Contains(event.Query, fragment) {
			return ctx
		}
	}

	f.hits.Add(1)

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	return canceled
}

// AfterQuery implements bun.QueryHook.
func (f *queryFault) AfterQuery(context.Context, *bun.QueryEvent) {}

// disarm stops the fault, so the assertions that follow read the real state.
func (f *queryFault) disarm() { f.disarmed.Store(true) }

// requireFired is the positive control of a fault test: the query the test
// meant to break was issued, so what it asserts next is about that failure.
func (f *queryFault) requireFired(t *testing.T) {
	t.Helper()
	f.disarm()
	require.Positive(t, f.hits.Load(), "no query matched %q: the fault never fired", f.fragments)
}
