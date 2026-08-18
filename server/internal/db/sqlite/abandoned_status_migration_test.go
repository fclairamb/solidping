package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// abandonedStatusMigrationSQL is migration 016 read straight out of the
// embedded FS, so this test can never drift from the file that actually ships.
func abandonedStatusMigrationSQL(t *testing.T) string {
	t.Helper()

	body, err := migrationsFS.ReadFile("migrations/016_v0_17_0.up.sql")
	require.NoError(t, err)

	return string(body)
}

// TestAbandonedStatusMigrationConvertsReapedRowsOnly is the data-migration half
// of spec 2026-08-18-10: a pre-existing row written by 015's reaper
// (`status = 6, abandoned = 1`) must come out as ResultStatusAbandoned (9),
// the `abandoned` column must be gone, and — the load-bearing negative control
// — a GENUINE `status = 6` row that was never abandoned must be left exactly
// as it was. Both halves are required: a migration that converted every error
// row would pass the first assertion on its own while silently erasing real
// downtime from every customer's history.
//
// Initialize() has already applied 016, so the pre-016 shape is reconstructed
// by adding the column back; from there the shipped file runs byte-for-byte as
// it would on a database that stopped at 015.
func TestAbandonedStatusMigrationConvertsReapedRowsOnly(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = s.Close() })
	r.NoError(s.Initialize(ctx))

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := s.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(s.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	// Rewind to the 015 shape.
	exec(`alter table results add column abandoned integer not null default 0`)
	r.Equal(1, count(`select count(*) from pragma_table_info('results') where name = 'abandoned'`),
		"precondition: the test rebuilt the pre-016 shape")

	org := models.NewOrganization("abandon-mig-org", "Abandon Mig Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "abandoned-migration-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	base := time.Now().UTC().Add(-3 * time.Hour)

	up := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
	up.PeriodStart = base
	r.NoError(s.CreateResult(ctx, up))

	genuineErr := models.NewResult(org.UID, check.UID, models.ResultStatusError, 7)
	genuineErr.PeriodStart = base.Add(time.Minute)
	r.NoError(s.CreateResult(ctx, genuineErr))

	reaped := models.NewResult(org.UID, check.UID, models.ResultStatusError, 0)
	reaped.PeriodStart = base.Add(2 * time.Minute)
	r.NoError(s.CreateResult(ctx, reaped))
	exec(`update results set abandoned = 1 where uid = ?`, reaped.UID)

	// An aggregated rollup row: it has no status at all, and the SQLite half of
	// this migration is a positional INSERT ... SELECT table rebuild, so a
	// column-order slip would surface here as scrambled counters rather than as
	// a wrong status.
	total, successful := 17, 16
	region := "eu-west-1"
	hourEnd := base.Add(time.Hour)
	hour := &models.Result{
		UID:              "00000000-0000-7000-8000-00000000abcd",
		OrganizationUID:  org.UID,
		CheckUID:         check.UID,
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      base.Truncate(time.Hour),
		PeriodEnd:        &hourEnd,
		Region:           &region,
		TotalChecks:      &total,
		SuccessfulChecks: &successful,
	}
	_, err = s.DB().NewInsert().Model(hour).Exec(ctx)
	r.NoError(err)

	rowsBefore := count(`select count(*) from results`)

	exec(abandonedStatusMigrationSQL(t))

	// 1. The reaped row is now the dedicated abandoned status.
	r.Equal(int(models.ResultStatusAbandoned),
		count(`select status from results where uid = ?`, reaped.UID),
		"the row 015's reaper wrote must become ResultStatusAbandoned")

	// 2. Negative control: a genuine error is untouched. Without this half, a
	//    migration that converted every status=6 row would look correct.
	r.Equal(int(models.ResultStatusError),
		count(`select status from results where uid = ?`, genuineErr.UID),
		"a genuine error must never be swept into abandoned — that would erase real downtime")
	r.Equal(int(models.ResultStatusUp), count(`select status from results where uid = ?`, up.UID))

	// 3. The column is gone.
	r.Equal(0, count(`select count(*) from pragma_table_info('results') where name = 'abandoned'`),
		"the abandoned column must be dropped")

	// 4. Nothing was lost or duplicated in the rebuild, and the aggregated row
	//    came across with its counters in the right columns.
	r.Equal(rowsBefore, count(`select count(*) from results`))
	r.Equal(total, count(`select total_checks from results where uid = ?`, hour.UID))
	r.Equal(successful, count(`select successful_checks from results where uid = ?`, hour.UID))

	var gotRegion, gotPeriodType string
	r.NoError(s.DB().QueryRowContext(ctx,
		`select region, period_type from results where uid = ?`, hour.UID).Scan(&gotRegion, &gotPeriodType))
	r.Equal(region, gotRegion)
	r.Equal(models.PeriodTypeHour, gotPeriodType)

	// 5. Every index the table carried is back, including 015's reaper index.
	r.Equal(4, count(`select count(*) from sqlite_master where type = 'index' and name in (
		'results_raw_idx', 'results_aggregated_idx', 'results_aggregated_unique_idx',
		'idx_results_lifecycle_pending')`),
		"the rebuild must recreate every index on results")

	// 6. The widened CHECK domain actually accepts 9 — the reaper's write path
	//    depends on it, and a rebuild that forgot to widen it would leave the
	//    reaper failing at runtime instead of at migration time.
	fresh := models.NewResult(org.UID, check.UID, models.ResultStatusAbandoned, 0)
	r.NoError(s.CreateResult(ctx, fresh))
}
