package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestMigration024DefaultedChecksBecomeAuto proves the auto-region-placement
// section of 024 (spec 2026-09-25-06) on a real pre-024 database — the "67
// checks pinned to gravelines" case. Only a check whose regions are exactly
// the system default_regions, as a set, becomes auto with region_count = its
// region count and an empty pool; its regions and jobs are untouched. Every
// other check stays pinned with identical jobs.
func TestMigration024DefaultedChecksBecomeAuto(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	for _, name := range migrationsBefore024() {
		execMigrationFile(ctx, t, database, name)
	}

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := database.ExecContext(ctx, query, args...)
		r.NoError(execErr, query)
	}

	exec(`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`)
	exec(`insert into parameters (uid, organization_uid, key, value)
	      values ('param-1', null, 'default_regions', '{"value":["gravelines","paris"]}')`)

	cases := []struct {
		uid, checkType, regions string
		wantAuto                bool
		wantCount               int
	}{
		{"chk-default", "http", `["gravelines","paris"]`, true, 2},
		{"chk-reordered", "http", `["paris","gravelines"]`, true, 2},
		{"chk-subset", "http", `["gravelines"]`, false, 0},
		{"chk-superset", "http", `["gravelines","paris","tokyo"]`, false, 0},
		{"chk-private", "http", `["gravelines","@office"]`, false, 0},
		{"chk-passive", "heartbeat", `["gravelines","paris"]`, false, 0},
		{"chk-dup", "http", `["gravelines","gravelines","paris"]`, false, 0},
	}

	for _, c := range cases {
		exec(`insert into checks (uid, organization_uid, slug, type, config, period, regions)
		      values (?, 'org-1', ?, ?, '{}', '00:01:00', ?)`, c.uid, c.uid, c.checkType, c.regions)
		exec(`insert into check_jobs (uid, organization_uid, check_uid, region, type, period, scheduled_at)
		      values (?, 'org-1', ?, 'gravelines', ?, '00:01:00', '2026-09-25 10:00:00')`,
			"job-"+c.uid, c.uid, c.checkType)
	}

	execMigrationFile(ctx, t, database, "024_v0_33_0.up.sql")

	for _, c := range cases {
		var (
			placement string
			count     sql.NullInt64
			pool      sql.NullString
			regions   string
		)

		r.NoError(database.QueryRowContext(ctx,
			`select placement, region_count, region_pool, regions from checks where uid = ?`, c.uid,
		).Scan(&placement, &count, &pool, &regions))

		if c.wantAuto {
			r.Equalf("auto", placement, "%s equals the system default and becomes auto", c.uid)
			r.True(count.Valid)
			r.Equal(int64(c.wantCount), count.Int64)
			r.False(pool.Valid, "an empty pool")
		} else {
			r.Equalf("pinned", placement, "%s stays pinned", c.uid)
			r.False(count.Valid)
		}

		if c.checkType != "http" {
			// A passive check's regions and jobs are the passive section's
			// business (spec 2026-09-25-04).
			continue
		}

		r.Equalf(c.regions, regions, "%s keeps its regions untouched", c.uid)

		var jobs int
		r.NoError(database.QueryRowContext(ctx,
			`select count(*) from check_jobs where check_uid = ? and region = 'gravelines'`, c.uid).Scan(&jobs))
		r.Equalf(1, jobs, "%s keeps its job", c.uid)
	}
}
