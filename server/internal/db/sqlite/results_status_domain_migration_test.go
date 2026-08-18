package sqlite

import (
	"context"
	"regexp"
	"sort"
	"strings"
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

	// 5. Every index the table carried is back with its DEFINITION intact, not
	//    merely its name. This compares against a reference database built by
	//    replaying 001..015, because comparing before/after inside THIS database
	//    would be circular: Initialize() has already run 016, so a rebuild that
	//    recreates a wrong index would simply produce the same wrong index
	//    twice and compare equal.
	//
	//    This assertion exists because the first cut of this migration
	//    recreated the indexes from 001's create-table block, which silently
	//    reverted results_aggregated_unique_idx to its pre-006 form and
	//    reopened the aggregation poison-pill loop (spec 2026-07-11-16) on the
	//    largest table in the system. A name-only check passed straight through
	//    that.
	r.Equal(resultsIndexDefinitionsAt015(ctx, t), resultsIndexDefinitions(ctx, t, s),
		"016 must leave every index on results byte-identical to what 001..015 produced")

	// 5b. The invariant that regression turned off, asserted behaviorally
	//     rather than by reading DDL: SQLite treats every NULL as distinct, so
	//     without coalesce(region, '') the unique index stops constraining
	//     region-less rollups entirely and the aggregation job can duplicate
	//     `hour` rows without bound.
	regionless := func(uid string) error {
		_, execErr := s.DB().ExecContext(ctx,
			`insert into results (uid, organization_uid, check_uid, period_type, period_start, region, total_checks)
			 values (?, ?, ?, 'hour', '2026-08-01T00:00:00Z', null, 1)`, uid, org.UID, check.UID)

		return execErr
	}
	r.NoError(regionless("00000000-0000-7000-8000-0000000000a1"),
		"positive control: the first region-less hour rollup must insert")
	r.Error(regionless("00000000-0000-7000-8000-0000000000a2"),
		"a second region-less hour rollup for the same bucket must collide — "+
			"results_aggregated_unique_idx has to keep its coalesce(region, '') form")

	// 6. The widened CHECK domain actually accepts 9 — the reaper's write path
	//    depends on it, and a rebuild that forgot to widen it would leave the
	//    reaper failing at runtime instead of at migration time.
	fresh := models.NewResult(org.UID, check.UID, models.ResultStatusAbandoned, 0)
	r.NoError(s.CreateResult(ctx, fresh))
}

// bunSplitRE matches the statement separator bun's migrator recognizes: a line
// containing nothing but `--bun:split`. Several migrations mention the marker
// inside ordinary comments, so a plain substring split would slice them apart
// mid-sentence and feed the remainder to SQLite as SQL.
var bunSplitRE = regexp.MustCompile(`(?m)^[ \t]*--bun:split[ \t]*$`)

// resultsIndexDefinitions snapshots every index on `results` as name -> the
// exact DDL SQLite stored for it. sqlite_autoindex_* rows carry a NULL sql and
// coalesce to "" — they are kept in the map deliberately, so a rebuild that
// lost the primary key would show up here too.
func resultsIndexDefinitions(ctx context.Context, t *testing.T, s *Service) map[string]string {
	t.Helper()

	rows, err := s.DB().QueryContext(ctx,
		`select name, coalesce(sql, '') from sqlite_master
		 where type = 'index' and tbl_name = 'results' order by name`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	out := make(map[string]string)

	for rows.Next() {
		var name, ddl string

		require.NoError(t, rows.Scan(&name, &ddl))

		out[name] = ddl
	}

	require.NoError(t, rows.Err())
	require.NotEmpty(t, out)

	return out
}

// resultsIndexDefinitionsAt015 builds a throwaway database by replaying every
// up migration BEFORE 016 and returns its `results` indexes. That is the
// ground truth a 016 upgrade has to preserve — and, unlike the live dev
// database, it cannot have drifted from the files that actually ship.
//
// Statements are split on a lone `--bun:split` LINE, exactly as bun's migrator
// splits them, so PRAGMA statements land in autocommit the way the real
// migration run executes them. Matching the bare substring instead would cut
// several migrations in half mid-comment — their prose explains why the marker
// is there.
func resultsIndexDefinitionsAt015(ctx context.Context, t *testing.T) map[string]string {
	t.Helper()

	// New() opens the database without migrating; Initialize() is what would
	// apply 016, so it is deliberately not called here.
	ref, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ref.Close() })

	entries, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".up.sql") && name < "016" {
			names = append(names, name)
		}
	}

	sort.Strings(names)
	require.NotEmpty(t, names, "no pre-016 migrations found — the filter is wrong")

	for _, name := range names {
		body, readErr := migrationsFS.ReadFile("migrations/" + name)
		require.NoError(t, readErr)

		for _, stmt := range bunSplitRE.Split(string(body), -1) {
			if strings.TrimSpace(stmt) == "" {
				continue
			}

			_, execErr := ref.DB().ExecContext(ctx, stmt)
			require.NoError(t, execErr, "replaying %s", name)
		}
	}

	return resultsIndexDefinitions(ctx, t, ref)
}
