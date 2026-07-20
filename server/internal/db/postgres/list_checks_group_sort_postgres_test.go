package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portGroupSort is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portGroupSort = 15455

// ungroupedSortKeySentinel mirrors the COALESCE sentinel baked into
// groupSortKeyExpr — ungrouped checks sort strictly last.
const ungroupedSortKeySentinel = int64(2147483647)

// groupSortFixturePG mirrors the SQLite fixture: three groups (A sortOrder 0,
// B 10, C 20) plus two ungrouped checks, controlled created_at so the
// within-bucket order (created_at DESC / uid DESC) is deterministic. Returns
// the org UID and the slugs in the exact order sort=group must produce.
func groupSortFixturePG(t *testing.T, s *Service) (orgUID string, wantOrder []string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("group-sort-pg-org", "Group Sort PG Org")
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

	mkCheck("gs-a1", &groupA, 100)
	mkCheck("gs-a2", &groupA, 200)
	mkCheck("gs-b1", &groupB, 50)
	mkCheck("gs-b2", &groupB, 300)
	mkCheck("gs-c1", &groupC, 250)
	mkCheck("gs-u1", nil, 400)
	mkCheck("gs-u2", nil, 150)

	return org.UID, []string{"gs-a2", "gs-a1", "gs-b2", "gs-b1", "gs-c1", "gs-u1", "gs-u2"}
}

// walkGroupSortedPG pages through the whole org with sort=group at the given
// page size, threading the composite cursor exactly as the service does, and
// returns the concatenated slug order.
func walkGroupSortedPG(t *testing.T, s *Service, orgUID string, limit int) []string {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	var order []string
	var cursorKey *int64
	var cursorCreated *time.Time
	var cursorUID *string

	for pages := 0; pages < 100; pages++ {
		checks, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{
			Limit:              limit,
			SortByGroup:        true,
			CursorGroupSortKey: cursorKey,
			CursorCreatedAt:    cursorCreated,
			CursorUID:          cursorUID,
		})
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
		key, created, uid := last.GroupSortKey, last.CreatedAt, last.UID
		cursorKey, cursorCreated, cursorUID = &key, &created, &uid
	}

	return order
}

func newGroupSortPG(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portGroupSort, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return s
}

// TestListChecksSortByGroupOrdering_Postgres is the Postgres counterpart to the
// SQLite ordering test: the correlated COALESCE sentinel and composite cursor
// must behave identically on real Postgres (the store's production backend).
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestListChecksSortByGroupOrdering_Postgres(t *testing.T) {
	s := newGroupSortPG(t)
	r := require.New(t)
	ctx := t.Context()

	orgUID, want := groupSortFixturePG(t, s)

	checks, total, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{Limit: 100, SortByGroup: true})
	r.NoError(err)
	r.Equal(int64(7), total)

	got := make([]string, len(checks))
	keyBySlug := make(map[string]int64, len(checks))
	for i, c := range checks {
		got[i] = *c.Slug
		keyBySlug[*c.Slug] = c.GroupSortKey
	}
	r.Equal(want, got, "sort=group ordering must match on Postgres")

	r.Equal(int64(0), keyBySlug["gs-a1"])
	r.Equal(int64(10), keyBySlug["gs-b2"])
	r.Equal(int64(20), keyBySlug["gs-c1"])
	r.Equal(ungroupedSortKeySentinel, keyBySlug["gs-u1"], "ungrouped sorts last via the COALESCE sentinel")
}

// TestListChecksSortByGroupCursorWalk_Postgres walks the composite cursor at
// several page sizes and asserts each reconstructs the full order — the same
// boundary coverage as the SQLite twin, on real Postgres.
//
//nolint:paralleltest // shares dev-machine resources with its siblings
func TestListChecksSortByGroupCursorWalk_Postgres(t *testing.T) {
	s := newGroupSortPG(t)
	r := require.New(t)

	orgUID, want := groupSortFixturePG(t, s)

	for _, limit := range []int{1, 2, 3, 5, 7, 100} {
		got := walkGroupSortedPG(t, s, orgUID, limit)
		r.Equalf(want, got, "cursor page-walk at limit=%d must reconstruct the full sort=group order", limit)
	}
}

// TestListChecksDefaultOrderingUnchanged_Postgres is the control on Postgres:
// without sort=group the ordering stays created_at DESC / uid DESC.
//
//nolint:paralleltest // shares dev-machine resources with its siblings
func TestListChecksDefaultOrderingUnchanged_Postgres(t *testing.T) {
	s := newGroupSortPG(t)
	r := require.New(t)
	ctx := t.Context()

	orgUID, _ := groupSortFixturePG(t, s)

	checks, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{Limit: 100})
	r.NoError(err)

	got := make([]string, len(checks))
	for i, c := range checks {
		got[i] = *c.Slug
		r.Equal(int64(0), c.GroupSortKey, "GroupSortKey must not be populated outside sort=group")
	}
	r.Equal([]string{"gs-u1", "gs-b2", "gs-c1", "gs-a2", "gs-u2", "gs-a1", "gs-b1"}, got)
}
