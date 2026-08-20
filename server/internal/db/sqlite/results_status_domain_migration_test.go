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

// lifecyclePendingIndexDDL is the exact text SQLite stores for the reaper's
// partial index, spelled out here rather than read back from the migration so
// a silent edit to the file (dropping the WHERE clause, widening it to include
// status=2) fails the test instead of redefining what it asserts.
const lifecyclePendingIndexDDL = "CREATE INDEX idx_results_lifecycle_pending on results (period_start)\n" +
	"  where period_type = 'raw' and status = 1"

// resultsStatusDomainMigrationSQL is the results-status-domain SECTION of the
// consolidated v0.17.0 migration, read straight out of the embedded FS so this
// test can never drift from the SQL that actually ships. Only that section is
// replayed: 014 is the whole release folded into one file, and its later
// sections (worker-version, incident-publications, slo-reporting) add columns
// with no IF NOT EXISTS and would fail on a second pass.
func resultsStatusDomainMigrationSQL(t *testing.T) string {
	t.Helper()

	return migrationSection(t, "results-status-domain")
}

// TestResultsStatusDomainMigration covers what 014's results-status-domain
// section actually guarantees on SQLite: the `results` status CHECK domain now admits
// ResultStatusAbandoned (9) and still rejects everything outside the domain,
// the reaper's partial index exists with its exact definition, and — the
// load-bearing part — the table rebuild 014 needs in order to widen an inline
// CHECK preserved every OTHER index on the table byte-for-byte.
//
// There is no `abandoned` column anywhere in this release and therefore no
// data conversion to assert: the v0.17.0 cycle briefly shipped the flag across
// two migrations that undid each other, and the consolidated migration never
// creates it. The absence is asserted explicitly so a botched port cannot
// quietly bring it back.
func TestResultsStatusDomainMigration(t *testing.T) {
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

	// 1. No `abandoned` column is ever created by this release.
	r.Equal(0, count(`select count(*) from pragma_table_info('results') where name = 'abandoned'`),
		"014 must never create an abandoned column — the flag was consolidated away before release")

	// 2. Every index the table carried before 014 is back with its DEFINITION
	//    intact, not merely its name, plus exactly one new index: the reaper's.
	//    The reference is a throwaway database replaying 001..013, because
	//    comparing before/after inside THIS database would be circular —
	//    Initialize() has already run 014, so a rebuild that recreated a wrong
	//    index would simply produce the same wrong index twice.
	//
	//    This assertion exists because an early cut of the rebuild recreated
	//    the indexes from 001's create-table block, which silently reverted
	//    results_aggregated_unique_idx to its pre-006 form and reopened the
	//    aggregation poison-pill loop (spec 2026-07-11-16) on the largest table
	//    in the system. A name-only check passed straight through that.
	before := resultsIndexDefinitionsAt013(ctx, t)
	after := resultsIndexDefinitions(ctx, t, s)

	r.NotContains(before, "idx_results_lifecycle_pending",
		"precondition: the reaper index must be what 014 adds, not something 013 already had")
	r.Equal(lifecyclePendingIndexDDL, after["idx_results_lifecycle_pending"],
		"the reaper's partial index must cover status=1 only — status=2 (running) is used by heartbeat checks")

	preserved := make(map[string]string, len(after))

	for name, ddl := range after {
		if name != "idx_results_lifecycle_pending" {
			preserved[name] = ddl
		}
	}

	r.Equal(before, preserved,
		"014 must leave every pre-existing index on results byte-identical to what 001..013 produced")

	org := models.NewOrganization("status-domain-org", "Status Domain Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "status-domain-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	base := time.Now().UTC().Add(-3 * time.Hour)

	up := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
	up.PeriodStart = base
	r.NoError(s.CreateResult(ctx, up))

	// 3a. The widened CHECK domain accepts 9 — the reaper's write path depends
	//     on it, and a rebuild that forgot to widen it would leave the reaper
	//     failing at runtime instead of at migration time.
	reaped := models.NewResult(org.UID, check.UID, models.ResultStatusAbandoned, 0)
	reaped.PeriodStart = base.Add(time.Minute)
	r.NoError(s.CreateResult(ctx, reaped))

	// 3b. ...and it still REJECTS a value outside the domain, so the rebuild
	//     did not simply drop the constraint on the floor. Both halves are
	//     required: a table with no CHECK at all passes 3a alone.
	_, err = s.DB().ExecContext(ctx, `update results set status = 42 where uid = ?`, up.UID)
	r.Error(err, "the status CHECK constraint must still be enforced after the migration")

	// 4. The invariant the index regression turned off, asserted behaviorally
	//    rather than by reading DDL: SQLite treats every NULL as distinct, so
	//    without coalesce(region, '') the unique index stops constraining
	//    region-less rollups entirely and the aggregation job can duplicate
	//    `hour` rows without bound.
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

	// 5. Replay the shipped section over a POPULATED table. The SQLite half of
	//    this migration is a positional INSERT ... SELECT table rebuild, so a
	//    column-order slip would surface here as scrambled counters — and on a
	//    freshly-initialized database there would be no rows to scramble.
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

	exec(resultsStatusDomainMigrationSQL(t))

	r.Equal(rowsBefore, count(`select count(*) from results`), "the rebuild must not lose or duplicate rows")
	r.Equal(int(models.ResultStatusAbandoned), count(`select status from results where uid = ?`, reaped.UID))
	r.Equal(int(models.ResultStatusUp), count(`select status from results where uid = ?`, up.UID))
	r.Equal(total, count(`select total_checks from results where uid = ?`, hour.UID))
	r.Equal(successful, count(`select successful_checks from results where uid = ?`, hour.UID))

	var gotRegion, gotPeriodType string
	r.NoError(s.DB().QueryRowContext(ctx,
		`select region, period_type from results where uid = ?`, hour.UID).Scan(&gotRegion, &gotPeriodType))
	r.Equal(region, gotRegion)
	r.Equal(models.PeriodTypeHour, gotPeriodType)

	r.Equal(after, resultsIndexDefinitions(ctx, t, s), "the rebuild must be index-stable when replayed")
	r.Equal(0, count(`select count(*) from pragma_table_info('results') where name = 'abandoned'`))
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

// resultsIndexDefinitionsAt013 builds a throwaway database by replaying every
// up migration BEFORE 014 — that is, everything through the last RELEASED
// migration — and returns its `results` indexes. That is the ground truth a
// 014 upgrade has to preserve, and unlike the live dev database it cannot have
// drifted from the files that actually ship.
//
// Statements are split on a lone `--bun:split` LINE, exactly as bun's migrator
// splits them, so PRAGMA statements land in autocommit the way the real
// migration run executes them. Matching the bare substring instead would cut
// several migrations in half mid-comment — their prose explains why the marker
// is there.
func resultsIndexDefinitionsAt013(ctx context.Context, t *testing.T) map[string]string {
	t.Helper()

	// New() opens the database without migrating; Initialize() is what would
	// apply 014, so it is deliberately not called here.
	ref, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ref.Close() })

	entries, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".up.sql") && name < "014" {
			names = append(names, name)
		}
	}

	sort.Strings(names)
	require.NotEmpty(t, names, "no pre-014 migrations found — the filter is wrong")

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
