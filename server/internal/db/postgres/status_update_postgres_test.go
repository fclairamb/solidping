package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portPublicStatusUpdates is distinct from every other _postgres_test.go
// file's embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go): 15434/15437 are costdist/checkjobsvc,
// 15438-15444 are headroom/idle-time, 15446/15447 are last_result, 15447 is
// incident_notification. This one gets its own instance with no collision.
const portPublicStatusUpdates = 15448

// TestListPublicStatusUpdates_WindowAndSoftDelete_Postgres is the Postgres
// regression guard for spec 2026-07-10-16: ListPublicStatusUpdates built its
// query with a Postgres `$1` placeholder and ran it through bun.DB.QueryContext,
// which only substitutes bun-style `?` — so the literal `$1` reached Postgres
// unbound and every call 500'd with "there is no parameter $1" (SQLSTATE 42P02).
// Nothing previously exercised this query against a real Postgres, which is why
// the mismatch shipped. This proves the placeholder now binds and the window /
// soft-delete predicates work end to end.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with the other embedded-Postgres tests in this package
func TestListPublicStatusUpdates_WindowAndSoftDelete_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portPublicStatusUpdates,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// FK chain: status_updates → organizations, status_pages, users(author).
	org := models.NewOrganization("sp-feed-org", "Status Page Feed Org")
	r.NoError(s.CreateOrganization(ctx, org))

	author := models.NewUser("status-updates-author@example.com")
	r.NoError(s.CreateUser(ctx, author))

	page := models.NewStatusPage(org.UID, "Public Page", "public-page")
	r.NoError(s.CreateStatusPage(ctx, page))

	// createUpdate seeds one status_updates row with the given title, published
	// time, and (optional) soft-delete time.
	createUpdate := func(title string, publishedAt time.Time, deletedAt *time.Time) *models.StatusUpdate {
		upd := models.NewStatusUpdate(org.UID, page.UID, author.UID)
		upd.Title = title
		upd.BodyMarkdown = "body of " + title
		upd.Kind = models.StatusUpdateKindInfo
		upd.PublishedAt = publishedAt
		upd.DeletedAt = deletedAt
		r.NoError(s.CreateStatusUpdate(ctx, upd))

		return upd
	}

	now := time.Now()

	// (1) Inside the 30-day window, not deleted → must be returned.
	inWindow := createUpdate("in-window", now.Add(-2*24*time.Hour), nil)
	// (2) Outside the 30-day window → must be excluded.
	createUpdate("out-of-window", now.Add(-100*24*time.Hour), nil)
	// (3) Inside the window but soft-deleted → must be excluded.
	deletedAt := now.Add(-30 * time.Minute)
	createUpdate("soft-deleted", now.Add(-1*24*time.Hour), &deletedAt)

	updates, err := s.ListPublicStatusUpdates(ctx, page.UID, 30)
	r.NoError(err, "the $1 placeholder must bind through bun (no SQLSTATE 42P02)")
	r.Len(updates, 1, "only the in-window, non-deleted row is returned")
	r.Equal(inWindow.UID, updates[0].UID)
	r.Equal("in-window", updates[0].Title)
	r.Equal("info", updates[0].Kind)

	// A page with no rows in its window returns an empty slice, not an error.
	otherPage := models.NewStatusPage(org.UID, "Empty Page", "empty-page")
	r.NoError(s.CreateStatusPage(ctx, otherPage))

	empty, err := s.ListPublicStatusUpdates(ctx, otherPage.UID, 30)
	r.NoError(err)
	r.Empty(empty)
}
