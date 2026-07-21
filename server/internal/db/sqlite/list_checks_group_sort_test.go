package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ungroupedSortKeySentinel mirrors the COALESCE sentinel baked into
// groupSortKeyExpr — ungrouped checks sort strictly last.
const ungroupedSortKeySentinel = int64(2147483647)

// groupSortFixture seeds three groups (A sortOrder 0, B 10, C 20) plus two
// ungrouped checks, each with a controlled created_at so the within-bucket
// order (created_at DESC / uid DESC) is deterministic. It returns the org UID
// and the slugs in the exact order sort=group must produce.
func groupSortFixtureSQLite(t *testing.T, s *Service) (string, []string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("group-sort-org", "Group Sort Org")
	r.NoError(s.CreateOrganization(ctx, org))

	mkGroup := func(slug string, sortOrder int16) string {
		g := models.NewCheckGroup(org.UID, slug, slug)
		g.SortOrder = sortOrder
		r.NoError(s.CreateCheckGroup(ctx, g))

		return g.UID
	}

	groupA := mkGroup("grp-a", 0)
	groupB := mkGroup("grp-b", 10)
	groupC := mkGroup("grp-c", 20)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mkCheck := func(slug string, group *string, offsetSec int) {
		c := models.NewCheck(org.UID, slug, "http")
		c.CheckGroupUID = group
		c.CreatedAt = base.Add(time.Duration(offsetSec) * time.Second)
		r.NoError(s.CreateCheck(ctx, c))
	}

	// Within a bucket, created_at DESC then uid DESC. Offsets chosen so the
	// order is unambiguous by created_at alone. (Slugs must be 3-40 chars.)
	mkCheck("gs-a1", &groupA, 100)
	mkCheck("gs-a2", &groupA, 200)
	mkCheck("gs-b1", &groupB, 50)
	mkCheck("gs-b2", &groupB, 300)
	mkCheck("gs-c1", &groupC, 250)
	mkCheck("gs-u1", nil, 400)
	mkCheck("gs-u2", nil, 150)

	// A (a2,a1) → B (b2,b1) → C (c1) → ungrouped (u1,u2).
	return org.UID, []string{"gs-a2", "gs-a1", "gs-b2", "gs-b1", "gs-c1", "gs-u1", "gs-u2"}
}

// walkGroupSortedSQLite pages through the whole org with sort=group at the
// given page size, threading the composite cursor exactly as the service does,
// and returns the concatenated slug order across all pages.
func walkGroupSortedSQLite(t *testing.T, s *Service, orgUID string, limit int) []string {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	var order []string
	var cursorKey *int64
	var cursorCreated *time.Time
	var cursorUID *string

	for pages := 0; pages < 100; pages++ {
		filter := &models.ListChecksFilter{
			Limit:              limit,
			SortByGroup:        true,
			CursorGroupSortKey: cursorKey,
			CursorCreatedAt:    cursorCreated,
			CursorUID:          cursorUID,
		}

		checks, _, err := s.ListChecks(ctx, orgUID, filter)
		r.NoError(err)

		hasMore := len(checks) > limit
		if hasMore {
			checks = checks[:limit]
		}

		if len(checks) == 0 {
			break
		}

		for _, c := range checks {
			order = append(order, *c.Slug)
		}

		if !hasMore {
			break
		}

		last := checks[len(checks)-1]
		key := last.GroupSortKey
		created := last.CreatedAt
		uid := last.UID
		cursorKey, cursorCreated, cursorUID = &key, &created, &uid
	}

	return order
}

// TestListChecksSortByGroupOrdering pins the single-page ordering: groups by
// sort_order ascending, ungrouped last, created_at DESC within a bucket, and
// the effective GroupSortKey scanned onto each row.
func TestListChecksSortByGroupOrdering(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, want := groupSortFixtureSQLite(t, s)

	checks, total, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{
		Limit:       100,
		SortByGroup: true,
	})
	r.NoError(err)
	r.Equal(int64(7), total)

	got := make([]string, len(checks))
	for i, c := range checks {
		got[i] = *c.Slug
	}
	r.Equal(want, got, "sort=group must order by group sort_order, ungrouped last, created_at DESC within a bucket")

	// Effective group sort key surfaced on each row (used by the cursor).
	keyBySlug := make(map[string]int64, len(checks))
	for _, c := range checks {
		keyBySlug[*c.Slug] = c.GroupSortKey
	}
	r.Equal(int64(0), keyBySlug["gs-a1"])
	r.Equal(int64(0), keyBySlug["gs-a2"])
	r.Equal(int64(10), keyBySlug["gs-b1"])
	r.Equal(int64(20), keyBySlug["gs-c1"])
	r.Equal(ungroupedSortKeySentinel, keyBySlug["gs-u1"], "ungrouped sorts last via the COALESCE sentinel")
	r.Equal(ungroupedSortKeySentinel, keyBySlug["gs-u2"])
}

// TestListChecksSortByGroupCursorWalk walks the composite cursor at several
// page sizes — covering a boundary inside a group (limit 3), exactly on a
// group boundary (limit 2), and the grouped→ungrouped transition — and asserts
// each walk reconstructs the full single-page order with no gaps or repeats.
func TestListChecksSortByGroupCursorWalk(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, want := groupSortFixtureSQLite(t, s)

	for _, limit := range []int{1, 2, 3, 5, 7, 100} {
		got := walkGroupSortedSQLite(t, s, orgUID, limit)
		r.Equalf(want, got, "cursor page-walk at limit=%d must reconstruct the full sort=group order", limit)
	}
}

// TestListChecksSortByGroupUngroupedLast isolates the ungrouped-last property:
// every grouped check precedes every ungrouped one, independent of created_at.
func TestListChecksSortByGroupUngroupedLast(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, _ := groupSortFixtureSQLite(t, s)

	checks, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{Limit: 100, SortByGroup: true})
	r.NoError(err)

	// u1 has the newest created_at of all — under the default ordering it would
	// be first; under sort=group it must be after every grouped check.
	seenUngrouped := false
	for _, c := range checks {
		ungrouped := c.CheckGroupUID == nil
		if ungrouped {
			seenUngrouped = true
		} else {
			r.Falsef(seenUngrouped, "grouped check %q appeared after an ungrouped one", *c.Slug)
		}
	}
}

// TestListChecksSortByGroupReorderMidWalk documents the best-effort behavior
// when a group's sort_order changes between pages: like any keyset pagination
// under concurrent mutation, individual rows may be skipped or repeated, but
// the walk must still terminate cleanly and never error. (The reorder here even
// triggers the store's sort_order normalization, the harshest form of the
// scenario.)
func TestListChecksSortByGroupReorderMidWalk(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, _ := groupSortFixtureSQLite(t, s)

	// Page 1 (limit 2) = [a2, a1] — all of group A (sort_order 0).
	page1, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{Limit: 2, SortByGroup: true})
	r.NoError(err)
	r.Len(page1, 3, "limit+1 peek row expected")
	page1 = page1[:2]
	r.Equal("gs-a2", *page1[0].Slug)
	r.Equal("gs-a1", *page1[1].Slug)

	cursorRow := page1[1]
	key := cursorRow.GroupSortKey
	created := cursorRow.CreatedAt
	uid := cursorRow.UID

	// Move group A to the end by bumping its sort_order beyond every other group
	// (this also normalizes all groups' sort_order).
	groupA, err := s.GetCheckGroupBySlug(ctx, orgUID, "grp-a")
	r.NoError(err)
	newOrder := int16(30)
	r.NoError(s.UpdateCheckGroup(ctx, orgUID, groupA.UID, &models.CheckGroupUpdate{SortOrder: &newOrder}))

	// Continue the walk from the now-stale cursor. The only guarantees are that
	// it terminates (never spins) and never errors.
	pages := 0
	cursorKey, cursorCreated, cursorUID := &key, &created, &uid
	for ; pages < 100; pages++ {
		checks, _, listErr := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{
			Limit:              2,
			SortByGroup:        true,
			CursorGroupSortKey: cursorKey,
			CursorCreatedAt:    cursorCreated,
			CursorUID:          cursorUID,
		})
		r.NoError(listErr)

		hasMore := len(checks) > 2
		if hasMore {
			checks = checks[:2]
		}
		if len(checks) == 0 || !hasMore {
			break
		}
		last := checks[len(checks)-1]
		k, ca, u := last.GroupSortKey, last.CreatedAt, last.UID
		cursorKey, cursorCreated, cursorUID = &k, &ca, &u
	}

	r.Less(pages, 99, "walk under concurrent reorder must terminate, not spin")
}

// TestListChecksDefaultOrderingUnchanged is the control: without sort=group the
// ordering stays created_at DESC / uid DESC across all checks, ignoring groups.
func TestListChecksDefaultOrderingUnchanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, _ := groupSortFixtureSQLite(t, s)

	checks, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{Limit: 100})
	r.NoError(err)

	got := make([]string, len(checks))
	for i, c := range checks {
		got[i] = *c.Slug
	}
	// created_at DESC: u1(400) b2(300) c1(250) a2(200) u2(150) a1(100) b1(50).
	r.Equal([]string{"gs-u1", "gs-b2", "gs-c1", "gs-a2", "gs-u2", "gs-a1", "gs-b1"}, got)

	// The scanonly group key stays zero when not sorting by group.
	for _, c := range checks {
		r.Equal(int64(0), c.GroupSortKey, "GroupSortKey must not be populated outside sort=group")
	}
}
