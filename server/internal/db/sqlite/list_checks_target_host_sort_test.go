package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// targetHostSortFixtureSQLite seeds checks across the host/url/target/none
// config shapes, plus a same-host pair to prove the tiebreaker (name
// ascending). It returns the org UID and the slugs in the exact order
// sort=targetHost must produce.
func targetHostSortFixtureSQLite(t *testing.T, s *Service) (string, []string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("target-host-sort-org", "Target Host Sort Org")
	r.NoError(s.CreateOrganization(ctx, org))

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mkCheck := func(slug, name, checkType string, config models.JSONMap, offsetSec int) {
		c := models.NewCheck(org.UID, slug, checkType)
		c.Name = &name
		c.Config = config
		c.CreatedAt = base.Add(time.Duration(offsetSec) * time.Second)
		r.NoError(s.CreateCheck(ctx, c))
	}

	// Two checks on the same host (config-wise "a.example.com") to prove the
	// name tiebreaker: "Alpha" < "Bravo".
	mkCheck("ths-a-bravo", "Bravo", "tcp", models.JSONMap{"host": "a.example.com", "port": float64(443)}, 100)
	mkCheck("ths-a-alpha", "Alpha", "ssl", models.JSONMap{"host": "a.example.com"}, 200)
	// b.example.com via url (http-shaped).
	mkCheck("ths-b-url", "B via URL", "http", models.JSONMap{"url": "https://b.example.com/health"}, 300)
	// dnsbl-shaped target.
	mkCheck("ths-target", "DNSBL target", "dnsbl", models.JSONMap{"target": "9.9.9.9"}, 50)
	// heartbeat: no host/url/target at all → must sort last.
	mkCheck("ths-none-2", "Zzz heartbeat", "heartbeat", models.JSONMap{"token": "tok2"}, 150)
	mkCheck("ths-none-1", "Aaa heartbeat", "heartbeat", models.JSONMap{"token": "tok1"}, 400)

	// Expected: lexicographic by targetHostSortKey ("9.9.9.9" < "a.example.com"
	// < "b.example.com" < sentinel), then name ascending within a tie
	// (a.example.com: Alpha before Bravo), then name ascending among the
	// none-of-host/url/target bucket (Aaa before Zzz).
	return org.UID, []string{
		"ths-target",
		"ths-a-alpha", "ths-a-bravo",
		"ths-b-url",
		"ths-none-1", "ths-none-2",
	}
}

// walkTargetHostSortedSQLite pages through the whole org with sort=targetHost
// at the given page size, threading the composite cursor exactly as the
// service does, and returns the concatenated slug order across all pages.
func walkTargetHostSortedSQLite(t *testing.T, s *Service, orgUID string, limit int) []string {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	var order []string
	var cursorKey *string
	var cursorName *string
	var cursorUID *string

	for pages := 0; pages < 100; pages++ {
		filter := &models.ListChecksFilter{
			Limit:                limit,
			SortByTargetHost:     true,
			CursorTargetHostKey:  cursorKey,
			CursorTargetHostName: cursorName,
			CursorUID:            cursorUID,
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

// TestListChecksSortByTargetHostOrdering pins the single-page ordering:
// targetHost sort key ascending, none-of-host/url/target last, name ascending
// as the tiebreaker.
func TestListChecksSortByTargetHostOrdering(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, want := targetHostSortFixtureSQLite(t, s)

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
	r.Equal(want, got, "sort=targetHost must order by host/url/target ascending, none-last, name ascending")
}

// TestListChecksSortByTargetHostCursorWalk walks the composite cursor at
// several page sizes and asserts each reconstructs the full single-page order
// with no gaps or repeats.
func TestListChecksSortByTargetHostCursorWalk(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, want := targetHostSortFixtureSQLite(t, s)

	for _, limit := range []int{1, 2, 3, 5, 6, 100} {
		got := walkTargetHostSortedSQLite(t, s, orgUID, limit)
		r.Equalf(want, got, "cursor page-walk at limit=%d must reconstruct the full sort=targetHost order", limit)
	}
}

// TestListChecksSortByTargetHostNoneLast isolates the none-of-host/url/target-
// last property: every check with a derivable host precedes every check
// without one, independent of created_at.
func TestListChecksSortByTargetHostNoneLast(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	orgUID, _ := targetHostSortFixtureSQLite(t, s)

	checks, _, err := s.ListChecks(ctx, orgUID, &models.ListChecksFilter{Limit: 100, SortByTargetHost: true})
	r.NoError(err)

	seenNone := false
	for _, c := range checks {
		none := c.Type == "heartbeat"
		if none {
			seenNone = true
		} else {
			r.Falsef(seenNone, "check %q with a derivable host appeared after a none-of-host/url/target check", *c.Slug)
		}
	}
}
