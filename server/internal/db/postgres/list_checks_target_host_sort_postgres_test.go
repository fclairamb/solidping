package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portTargetHostSort is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portTargetHostSort = 15466

// targetHostSortFixturePG mirrors the SQLite fixture: checks across the
// host/url/target/none config shapes, plus a same-host pair to prove the
// tiebreaker (name ascending). Returns the org UID and the slugs in the exact
// order sort=targetHost must produce.
func targetHostSortFixturePG(t *testing.T, s *Service) (string, []string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("target-host-pg-org", "Target Host Sort PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mkCheck := func(slug, name, checkType string, config models.JSONMap, offsetSec int) {
		c := models.NewCheck(org.UID, slug, checkType)
		c.Name = &name
		c.Config = config
		c.CreatedAt = base.Add(time.Duration(offsetSec) * time.Second)
		r.NoError(s.CreateCheck(ctx, c))
	}

	mkCheck("ths-a-bravo", "Bravo", "tcp", models.JSONMap{"host": "a.example.com", "port": float64(443)}, 100)
	mkCheck("ths-a-alpha", "Alpha", "ssl", models.JSONMap{"host": "a.example.com"}, 200)
	mkCheck("ths-b-url", "B via URL", "http", models.JSONMap{"url": "https://b.example.com/health"}, 300)
	mkCheck("ths-target", "DNSBL target", "dnsbl", models.JSONMap{"target": "9.9.9.9"}, 50)
	mkCheck("ths-none-2", "Zzz heartbeat", "heartbeat", models.JSONMap{"token": "tok2"}, 150)
	mkCheck("ths-none-1", "Aaa heartbeat", "heartbeat", models.JSONMap{"token": "tok1"}, 400)

	return org.UID, []string{
		"ths-target",
		"ths-a-alpha", "ths-a-bravo",
		"ths-b-url",
		"ths-none-1", "ths-none-2",
	}
}

// walkTargetHostSortedPG pages through the whole org with sort=targetHost at
// the given page size, threading the composite cursor exactly as the service
// does, and returns the concatenated slug order.
func walkTargetHostSortedPG(t *testing.T, s *Service, orgUID string, limit int) []string {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	var order []string
	var cursorKey *string
	var cursorName *string
	var cursorUID *string

	for pages := 0; pages < 100; pages++ {
		checks, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{
			Limit:                limit,
			SortByTargetHost:     true,
			CursorTargetHostKey:  cursorKey,
			CursorTargetHostName: cursorName,
			CursorUID:            cursorUID,
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
		key := last.TargetHostSortKey
		name := ""
		if last.Name != nil {
			name = *last.Name
		}
		uid := last.UID
		cursorKey, cursorName, cursorUID = &key, &name, &uid
	}

	return order
}

func newTargetHostSortPG(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portTargetHostSort, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return s
}

// TestListChecksSortByTargetHostOrdering_Postgres is the Postgres counterpart
// to the SQLite ordering test: the COALESCE sentinel and composite cursor must
// behave identically on real Postgres (the store's production backend).
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestListChecksSortByTargetHostOrdering_Postgres(t *testing.T) {
	s := newTargetHostSortPG(t)
	r := require.New(t)
	ctx := t.Context()

	orgUID, want := targetHostSortFixturePG(t, s)

	checks, total, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{
		Limit:            100,
		SortByTargetHost: true,
	})
	r.NoError(err)
	r.Equal(int64(6), total)

	got := make([]string, len(checks))
	for i, c := range checks {
		got[i] = *c.Slug
	}
	r.Equal(want, got, "sort=targetHost ordering must match on Postgres")
}

// TestListChecksSortByTargetHostCursorWalk_Postgres walks the composite cursor
// at several page sizes and asserts each reconstructs the full order — the
// same boundary coverage as the SQLite twin, on real Postgres.
//
//nolint:paralleltest // shares dev-machine resources with its siblings
func TestListChecksSortByTargetHostCursorWalk_Postgres(t *testing.T) {
	s := newTargetHostSortPG(t)
	r := require.New(t)

	orgUID, want := targetHostSortFixturePG(t, s)

	for _, limit := range []int{1, 2, 3, 5, 6, 100} {
		got := walkTargetHostSortedPG(t, s, orgUID, limit)
		r.Equalf(want, got, "cursor page-walk at limit=%d must reconstruct the full sort=targetHost order", limit)
	}
}
