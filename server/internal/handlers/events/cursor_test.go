package events_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func TestListEventsAscendingWalksEveryEventOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	f := newFixture(t)

	base := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
	f.svc.WithClock(clock.NewFake(base.Add(time.Hour)))

	// Seven events on one instant (page size 3 is smaller than the tie group),
	// plus two earlier ones.
	want := map[string]bool{}

	for i := 0; i < 9; i++ {
		at := base
		if i < 2 {
			at = base.Add(-time.Minute)
		}

		want[f.seed(ctx, t, models.EventTypeCheckCreated, nil, at, "").UID] = true
	}

	var (
		order  []string
		cursor string
		prev   time.Time
	)

	for page := 0; page < 10; page++ {
		resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
			Ascending: true, Size: 3, Cursor: cursor, Caller: events.Caller{UserUID: f.admin.UID},
		})
		r.NoError(err)
		r.NotEmpty(resp.Pagination.Cursor, "asc always carries a cursor, even on the last page")

		if len(resp.Data) == 0 {
			r.Equal(cursor, resp.Pagination.Cursor, "an empty page returns the cursor it was given")

			break
		}

		for _, item := range resp.Data {
			r.False(item.CreatedAt.Before(prev), "oldest first")
			prev = item.CreatedAt
			order = append(order, item.UID)
		}

		cursor = resp.Pagination.Cursor
	}

	r.Len(order, 9)

	for _, uid := range order {
		r.True(want[uid])
		delete(want, uid)
	}

	r.Empty(want, "every event exactly once")

	// Resume from the stored cursor after new events: only the new ones.
	fresh := f.seed(ctx, t, models.EventTypeCheckCreated, nil, base.Add(time.Minute), "")
	resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Ascending: true, Size: 10, Cursor: cursor, Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(fresh.UID, resp.Data[0].UID)
}

func TestListEventsAscendingSettleWindow(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	f := newFixture(t)

	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(now)
	f.svc.WithClock(clk)

	old := f.seed(ctx, t, models.EventTypeCheckCreated, nil, now.Add(-time.Minute), "")
	recent := f.seed(ctx, t, models.EventTypeCheckCreated, nil, now.Add(-2*time.Second), "")

	list := func(cursor string, order bool) *events.ListEventsResponse {
		resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
			Ascending: order, Size: 10, Cursor: cursor, Caller: events.Caller{UserUID: f.admin.UID},
		})
		r.NoError(err)

		return resp
	}

	first := list("", true)
	r.Len(first.Data, 1, "an event inside the settle window is held back")
	r.Equal(old.UID, first.Data[0].UID)

	clk.Advance(10 * time.Second)

	second := list(first.Pagination.Cursor, true)
	r.Len(second.Data, 1, "returned by the next call once the window has passed")
	r.Equal(recent.UID, second.Data[0].UID)

	// desc is unchanged: no settle window, newest first.
	desc := list("", false)
	r.Len(desc.Data, 2)
	r.Equal(recent.UID, desc.Data[0].UID)
}
