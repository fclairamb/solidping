package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

type cursorFixture struct {
	svc  *incidents.Service
	clk  *clock.Fake
	org  *models.Organization
	base time.Time
	list func(cursor string, asc bool, size int) *incidents.ListIncidentsResponse
	add  func(at time.Time) string
}

func newCursorFixture(t *testing.T) *cursorFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	base := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
	clk := clock.NewFake(base.Add(time.Hour))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization("cursor-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "acme-api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &cursorFixture{
		svc: svc, clk: clk, org: org, base: base,
		list: func(cursor string, asc bool, size int) *incidents.ListIncidentsResponse {
			resp, listErr := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{
				Cursor: cursor, Ascending: asc, Size: size,
			})
			r.NoError(listErr)

			return resp
		},
		add: func(at time.Time) string {
			inc := models.NewIncident(org.UID, check.UID, at, "outage")
			r.NoError(dbSvc.CreateIncident(ctx, inc))

			return inc.UID
		},
	}
}

func walkIncidents(f *cursorFixture, asc bool) []string {
	var seen []string

	cursor := ""

	for page := 0; page < 20; page++ {
		resp := f.list(cursor, asc, 3)
		for _, item := range resp.Data {
			seen = append(seen, item.UID)
		}

		if resp.Pagination.Cursor == "" || len(resp.Data) == 0 {
			break
		}

		cursor = resp.Pagination.Cursor
		if len(resp.Data) < 3 && !asc {
			break
		}
	}

	return seen
}

func TestListIncidentsCursorVisitsEveryIncidentOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newCursorFixture(t)

	want := map[string]bool{}

	// Eight incidents on one started_at (page size 3 < tie group) plus two others.
	for i := 0; i < 10; i++ {
		at := f.base
		if i >= 8 {
			at = f.base.Add(time.Duration(i) * time.Minute)
		}

		want[f.add(at)] = true
	}

	for _, asc := range []bool{false, true} {
		seen := walkIncidents(f, asc)
		r.Len(seen, 10)

		got := map[string]bool{}
		for _, uid := range seen {
			got[uid] = true
		}

		r.Equal(want, got, "every incident exactly once (asc=%v)", asc)
	}

	asc := walkIncidents(f, true)
	desc := walkIncidents(f, false)
	r.Equal(asc[9], desc[0], "asc is oldest first, desc newest first")
}

func TestListIncidentsLegacyBareUIDCursorStartsFromFirstPage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newCursorFixture(t)

	newest := f.add(f.base.Add(time.Minute))
	f.add(f.base)

	resp := f.list(newest, false, 10)
	r.Len(resp.Data, 2)
	r.Equal(newest, resp.Data[0].UID)
}

func TestListIncidentsAscendingResumeAndSettleWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newCursorFixture(t)
	now := f.clk.Now()

	first := f.add(now.Add(-time.Minute))
	recent := f.add(now.Add(-2 * time.Second))

	page := f.list("", true, 10)
	r.Len(page.Data, 1, "an incident inside the settle window is held back")
	r.Equal(first, page.Data[0].UID)
	r.NotEmpty(page.Pagination.Cursor)

	f.clk.Advance(10 * time.Second)

	page2 := f.list(page.Pagination.Cursor, true, 10)
	r.Len(page2.Data, 1)
	r.Equal(recent, page2.Data[0].UID, "only incidents after the cursor")

	empty := f.list(page2.Pagination.Cursor, true, 10)
	r.Empty(empty.Data)
	r.Equal(page2.Pagination.Cursor, empty.Pagination.Cursor, "an empty page keeps the cursor")

	older := f.add(now.Add(-30 * time.Second))
	_ = older // started before the cursor: not listed, only newer starts are

	later := f.add(now.Add(5 * time.Second))
	f.clk.Advance(time.Minute)

	page3 := f.list(empty.Pagination.Cursor, true, 10)
	r.Len(page3.Data, 1)
	r.Equal(later, page3.Data[0].UID)
}
