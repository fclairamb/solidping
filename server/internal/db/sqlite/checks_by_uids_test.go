package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// checksByUIDsQueryCounter is a bun.QueryHook that counts how many SELECT
// queries a call issues — used below to prove GetChecksByUIDs genuinely
// splits a large UID list into checksByUIDsChunkSize-sized batches rather
// than sending it all as one query.
//
// Why this proves chunking rather than a SQLite bound-parameter error:
// bun's `Where("uid IN (?)", bun.List(uids))` renders every UID as an
// inlined SQL literal, not as a driver-bound `?` placeholder (verified by
// capturing bun.QueryEvent.Query directly — the emitted SQL contains
// `IN ('uid1', 'uid2', ...)` with the values spelled out, and a manual probe
// with a single unchunked query of two million inlined UUID literals against
// this repo's SQLite driver (modernc.org/sqlite) raised no error at all).
// So SQLITE_MAX_VARIABLE_NUMBER never fires here regardless of chunk size —
// the query-count assertion is the only way to prove the chunking code path
// is actually exercised and correct at a chunk boundary.
type checksByUIDsQueryCounter struct {
	total int
}

func (c *checksByUIDsQueryCounter) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (c *checksByUIDsQueryCounter) AfterQuery(_ context.Context, _ *bun.QueryEvent) {
	c.total++
}

// TestGetChecksByUIDs_ChunksLargeInputAcrossMultipleQueries proves
// GetChecksByUIDs splits an input larger than checksByUIDsChunkSize into
// multiple batched queries (rather than one unbounded IN(...) query), and
// correctly merges every chunk's results into a single map.
//
// This is the audit-flagged gap for spec 2026-08-18-01: the checks batch
// incidents.buildIncidentEnrichment issues is NOT bounded by the response
// page size the way the incident/group UID batches are — it accumulates one
// entry per distinct member check across every group incident on the page,
// and check-group membership itself has no size cap anywhere in the
// checkgroups/checks handlers. A page with a few incidents on large check
// groups can therefore push a single unchunked IN(...) list arbitrarily
// large. 1200 UIDs spans exactly 3 chunks at the production chunk size
// (500+500+200), with real checks seeded at positions spread across all
// three so a broken/undersized chunk boundary would drop some of them.
func TestGetChecksByUIDs_ChunksLargeInputAcrossMultipleQueries(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("checks-by-uids-org", "Checks By UIDs Org")
	r.NoError(s.CreateOrganization(ctx, org))

	const (
		totalUIDs  = 1200 // spans 3 chunks at checksByUIDsChunkSize (500)
		realChecks = 60   // seeded at evenly spread positions so every chunk gets some
	)

	stride := totalUIDs / realChecks
	uids := make([]string, totalUIDs)
	wantSlugs := make(map[string]string, realChecks) // uid -> slug

	for i := range totalUIDs {
		if i%stride == 0 && len(wantSlugs) < realChecks {
			check := models.NewCheck(org.UID, fmt.Sprintf("check-%d", i), "http")
			r.NoError(s.CreateCheck(ctx, check))
			uids[i] = check.UID
			wantSlugs[check.UID] = *check.Slug
		} else {
			// Non-existent UID — must simply be absent from the result, not
			// cause an error or silently truncate the real checks around it.
			uids[i] = uuid.NewString()
		}
	}

	r.Len(wantSlugs, realChecks, "fixture setup must have seeded exactly realChecks real checks")

	counter := &checksByUIDsQueryCounter{}
	s.DB().AddQueryHook(counter)

	got, err := s.GetChecksByUIDs(ctx, org.UID, uids)
	r.NoError(err)

	// The chunking proof: 1200 UIDs at a 500-sized chunk must issue exactly
	// 3 queries (500 + 500 + 200), never 1 (unchunked) and never per-UID.
	// This is also the positive control — a hook that silently never fired
	// would read 0, not 3, so the assertion can't pass vacuously.
	r.Equal(3, counter.total,
		"1200 UIDs at chunk size 500 must issue exactly 3 batched queries — a stuck-at-1 "+
			"count would mean chunking regressed to a single unbounded query")

	r.Len(got, realChecks, "every real check must come back, and none of the non-existent UIDs must")

	for uid, wantSlug := range wantSlugs {
		check, ok := got[uid]
		r.True(ok, "real check %s must be present in the result", uid)
		r.NotNil(check.Slug)
		r.Equal(wantSlug, *check.Slug)
	}
}
