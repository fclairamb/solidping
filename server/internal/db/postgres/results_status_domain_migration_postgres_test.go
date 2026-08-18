package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portResultsStatusDomainMigration is distinct from every other
// _postgres_test.go embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portResultsStatusDomainMigration = 15478

// TestResultsStatusDomainMigration_Postgres is the Postgres twin of the SQLite
// migration test. The two dialects do the job completely differently — a DO
// block that drops 001's auto-named CHECK and re-adds a named one here, a whole
// *_new table rebuild there — so each needs its own proof.
//
// What 014 has to guarantee on a freshly-migrated database:
//   - the `results` status CHECK domain admits ResultStatusAbandoned (9);
//   - it still rejects a value outside the domain, i.e. exactly ONE check
//     constraint on status survives — the DO block neither missed 001's
//     unnamed constraint (which would leave the old 0..8 domain ANDed on top,
//     rejecting 9) nor dropped it without re-adding one;
//   - the reaper's partial index exists, covering status=1 only;
//   - no `abandoned` column exists. The v0.17.0 cycle briefly shipped that
//     flag across two migrations that undid each other; the consolidated
//     migration never creates it, and there is consequently no data
//     conversion to assert.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestResultsStatusDomainMigration_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portResultsStatusDomainMigration, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(s.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	// 1. No `abandoned` column is ever created by this release.
	r.Equal(0, count(`select count(*) from information_schema.columns
		where table_name = 'results' and column_name = 'abandoned'`),
		"014 must never create an abandoned column — the flag was consolidated away before release")

	// 2. Exactly one CHECK constraint on `results` mentions status, and it is
	//    the named one 014 adds. If the DO block had failed to find 001's
	//    auto-named constraint there would be two, and the widened domain would
	//    be dead letter behind the old one.
	r.Equal(1, count(`select count(*)
		from pg_constraint c
		join pg_class t on t.oid = c.conrelid
		join pg_namespace n on n.oid = t.relnamespace
		where t.relname = 'results' and n.nspname = current_schema()
		  and c.contype = 'c' and pg_get_constraintdef(c.oid) ilike '%status%'`),
		"exactly one status CHECK must survive — the DO block has to drop 001's auto-named one")
	r.Equal(1, count(`select count(*) from pg_constraint where conname = 'results_status_check'`),
		"the surviving constraint must be the named one 014 adds")

	// 3. The reaper's partial index exists and covers status=1 only —
	//    status=2 (running) is excluded on purpose, because heartbeat checks
	//    write legitimate long-lived running rows this index must not carry.
	var indexDef string
	r.NoError(s.DB().QueryRowContext(ctx,
		`select indexdef from pg_indexes where tablename = 'results' and indexname = 'idx_results_lifecycle_pending'`).
		Scan(&indexDef))
	r.Contains(indexDef, "period_start")
	r.Contains(indexDef, "status = 1")
	r.NotContains(indexDef, "status = 2")

	org := models.NewOrganization("status-domain-pg", "Status Domain PG")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "status-domain-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	up := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
	r.NoError(s.CreateResult(ctx, up))

	// 4a. The widened domain accepts 9 — the reaper's write path depends on it,
	//     and a migration that forgot to widen it would leave the reaper
	//     failing at runtime instead of at migration time.
	reaped := models.NewResult(org.UID, check.UID, models.ResultStatusAbandoned, 0)
	r.NoError(s.CreateResult(ctx, reaped))

	// 4b. ...and it still rejects a value outside the domain. Both halves are
	//     required: a table with no CHECK at all passes 4a alone.
	_, err = s.DB().ExecContext(ctx, `update results set status = 42 where uid = ?`, up.UID)
	r.Error(err, "the status CHECK constraint must still be enforced after the migration")
}
