package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portAbandonedStatusMigration is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portAbandonedStatusMigration = 15478

// TestAbandonedStatusMigrationConvertsReapedRowsOnly_Postgres is the Postgres
// twin of the SQLite migration test. The two dialects do the job completely
// differently — a DO block dropping the auto-named CHECK plus an UPDATE here,
// a whole *_new table rebuild there — so each needs its own proof.
//
// Both halves are required: a row 015's reaper wrote
// (`status = 6, abandoned = true`) must come out as ResultStatusAbandoned (9),
// and a GENUINE `status = 6` row must be left exactly as it was. A migration
// that converted every error row would pass the first assertion alone while
// silently erasing real downtime from every customer's history.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestAbandonedStatusMigrationConvertsReapedRowsOnly_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portAbandonedStatusMigration, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	migration, err := migrationsFS.ReadFile("migrations/016_v0_17_0.up.sql")
	r.NoError(err)

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

	// Rewind to the 015 shape: Initialize() has already applied 016, so the
	// column has to come back before the shipped file can run as it would on a
	// database that stopped at 015.
	exec(`alter table results add column abandoned boolean not null default false`)
	r.Equal(1, count(`select count(*) from information_schema.columns
		where table_name = 'results' and column_name = 'abandoned'`),
		"precondition: the test rebuilt the pre-016 shape")

	org := models.NewOrganization("abandon-mig-pg", "Abandon Mig PG")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "abandon-mig-check", "http")
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
	exec(`update results set abandoned = true where uid = ?`, reaped.UID)

	rowsBefore := count(`select count(*) from results`)

	_, err = s.DB().ExecContext(ctx, string(migration))
	r.NoError(err)

	// 1. The reaped row is now the dedicated abandoned status.
	r.Equal(int(models.ResultStatusAbandoned), count(`select status from results where uid = ?`, reaped.UID),
		"the row 015's reaper wrote must become ResultStatusAbandoned")

	// 2. Negative control: a genuine error is untouched.
	r.Equal(int(models.ResultStatusError), count(`select status from results where uid = ?`, genuineErr.UID),
		"a genuine error must never be swept into abandoned — that would erase real downtime")
	r.Equal(int(models.ResultStatusUp), count(`select status from results where uid = ?`, up.UID))

	// 3. The column is gone, and nothing was lost.
	r.Equal(0, count(`select count(*) from information_schema.columns
		where table_name = 'results' and column_name = 'abandoned'`),
		"the abandoned column must be dropped")
	r.Equal(rowsBefore, count(`select count(*) from results`))

	// 4. The CHECK domain was actually widened rather than silently left in
	//    place by a name mismatch in the DO block — without this the reaper
	//    would fail at runtime instead of at migration time.
	fresh := models.NewResult(org.UID, check.UID, models.ResultStatusAbandoned, 0)
	r.NoError(s.CreateResult(ctx, fresh))

	// ...and it still rejects a value outside the domain, so the DO block did
	// not simply drop the constraint and forget to re-add it.
	_, err = s.DB().ExecContext(ctx,
		`update results set status = 42 where uid = ?`, up.UID)
	r.Error(err, "the status CHECK constraint must still be enforced after the migration")
}
