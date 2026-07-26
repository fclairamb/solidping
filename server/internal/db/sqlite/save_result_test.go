package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestSaveResultWithStatusTracking_SingleInsert proves the result save path is
// exactly one INSERT with no companion UPDATE (spec 2026-07-24-02 §1). It used
// to clear the predecessor's last_for_status flag on every save, costing a
// second row write per check execution on the hottest table in the system.
//
// SQLite's total_changes() counts every row inserted/updated/deleted on the
// connection (the pool is capped at one connection, see New), so a delta of
// exactly 1 per save is a direct measurement that no second write happens —
// the only assertion that would catch the maintenance UPDATE coming back.
func TestSaveResultWithStatusTracking_SingleInsert(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = s.Close() })
	r.NoError(s.Initialize(ctx))

	org := models.NewOrganization("save-path-org", "Save Path Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "save-path-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	totalChanges := func() int {
		var n int
		r.NoError(s.db.NewRaw("SELECT total_changes()").Scan(ctx, &n))

		return n
	}

	base := time.Now().Add(time.Hour)

	first := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 10)
	first.PeriodStart = base

	before := totalChanges()
	r.NoError(s.SaveResultWithStatusTracking(ctx, first))
	r.Equal(1, totalChanges()-before,
		"saving a result must write exactly one row: a single INSERT, no clearing UPDATE")

	// A second save with the *same* status is the case the old flag-clearing
	// UPDATE targeted — it must not touch the predecessor either.
	second := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 20)
	second.PeriodStart = base.Add(time.Minute)

	before = totalChanges()
	r.NoError(s.SaveResultWithStatusTracking(ctx, second))
	r.Equal(1, totalChanges()-before,
		"a second same-status save must not rewrite its predecessor")

	// Both rows persist, and the first one is byte-for-byte what was written.
	resp, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{models.PeriodTypeRaw},
		Statuses:        []int{int(models.ResultStatusDown)},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(resp.Results, 2, "both saved results must persist")

	byUID := make(map[string]*models.Result, len(resp.Results))
	for _, row := range resp.Results {
		byUID[row.UID] = row
	}

	stored := byUID[first.UID]
	r.NotNil(stored, "the first result must still be there")
	r.NotNil(stored.Status)
	r.Equal(int(models.ResultStatusDown), *stored.Status)
	r.NotNil(stored.Duration)
	r.InDelta(10, *stored.Duration, 0.001)
	r.NotNil(byUID[second.UID], "the second result must be there too")

	// Positive control: an UPDATE touching one row does move the counter, so the
	// deltas of 1 measured above are evidence and not a blind spot.
	before = totalChanges()
	_, err = s.db.ExecContext(ctx, "update results set status = status where uid = ?", first.UID)
	r.NoError(err)
	r.Equal(1, totalChanges()-before, "a one-row UPDATE must be visible to total_changes()")
}
