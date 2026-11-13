package auth

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
)

// sqlFault selects the queries a queryFault fails: those whose SQL contains
// every fragment, once the first skip of them have gone through. With single
// set, only the first of those fails and the later ones go through again.
type sqlFault struct {
	fragments []string
	skip      int
	single    bool
}

// failingSQL selects every query whose SQL contains all the fragments, for
// example failingSQL(`UPDATE "users"`).
func failingSQL(fragments ...string) sqlFault {
	return sqlFault{fragments: fragments}
}

// after lets the first n matching queries through.
func (f sqlFault) after(n int) sqlFault {
	f.skip = n

	return f
}

// once fails a single query: the store recovers right after.
func (f sqlFault) once() sqlFault {
	f.single = true

	return f
}

// queryFault is a bun query hook that fails chosen queries by handing the
// driver an already canceled context, like testsupport.FaultHook, but picks
// them by their SQL text instead of their position. A test can then name the
// store call whose failure it is about, and stays valid when an unrelated
// query is added in front of it.
type queryFault struct {
	mu      sync.Mutex
	fault   sqlFault
	armed   bool
	matched int
	fired   int
}

func installQueryFault(dbSvc db.Service) *queryFault {
	hook := &queryFault{}
	dbSvc.DB().AddQueryHook(hook)

	return hook
}

// BeforeQuery implements bun.QueryHook.
func (q *queryFault) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.armed {
		return ctx
	}

	for _, fragment := range q.fault.fragments {
		if !strings.Contains(event.Query, fragment) {
			return ctx
		}
	}

	q.matched++
	if q.matched <= q.fault.skip || (q.fault.single && q.fired > 0) {
		return ctx
	}

	q.fired++

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	return canceled
}

// AfterQuery implements bun.QueryHook.
func (q *queryFault) AfterQuery(context.Context, *bun.QueryEvent) {}

// during runs operation with fault armed and requires that the fault fired:
// a fault that matched no query proves nothing about the branch it targets.
func (q *queryFault) during(t *testing.T, fault sqlFault, operation func()) {
	t.Helper()

	q.mu.Lock()
	q.fault, q.armed, q.matched, q.fired = fault, true, 0, 0
	q.mu.Unlock()

	operation()

	q.mu.Lock()
	fired := q.fired
	q.armed = false
	q.mu.Unlock()

	require.Positive(t, fired, "no query matched %q (after %d)", fault.fragments, fault.skip)
}
