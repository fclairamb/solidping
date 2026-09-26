package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// migrationsBefore024 is every SQLite migration up to (and excluding) 024.
func migrationsBefore024() []string {
	return append(migrationsBefore021(), "021_v0_28_0.up.sql", "022_v0_30_0.up.sql", "023_v0_32_0.up.sql")
}

// TestMigration024PassiveChecksLoseTheirRegions proves the
// passive-checks-no-regions section of 024 (spec 2026-09-25-04) on a real
// pre-024 database: every other test runs against a fresh schema, where the
// section matches nothing.
//
// Cases, one per way the fold can go wrong:
//
//	hb-two:     regions and one job per region, no NULL job → one NULL job
//	            (the lowest-uid survivor), regions emptied
//	em-mixed:   a NULL job AND a regional one → the regional one is deleted
//	hb-leased:  a leased regional job → converted, lease cleared
//	http-two:   an active check → untouched (positive control)
func TestMigration024PassiveChecksLoseTheirRegions(t *testing.T) {
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
	exec(`insert into workers (uid, slug, name) values ('wrk-1', 'eu-worker', 'EU worker')`)

	checks := []struct{ uid, slug, checkType, regions string }{
		{"chk-hb-two", "hb-two", "heartbeat", `["eu","us"]`},
		{"chk-em-mixed", "em-mixed", "email", `["eu"]`},
		{"chk-hb-leased", "hb-leased", "heartbeat", `["eu"]`},
		{"chk-http-two", "http-two", "http", `["eu","us"]`},
	}
	for _, c := range checks {
		exec(`insert into checks (uid, organization_uid, slug, type, config, period, regions)
		      values (?, 'org-1', ?, ?, '{}', '00:01:00', ?)`, c.uid, c.slug, c.checkType, c.regions)
	}

	jobs := []struct {
		uid, checkUID, checkType string
		region                   any
		leased                   bool
	}{
		{"job-hb-two-b", "chk-hb-two", "heartbeat", "us", false},
		{"job-hb-two-a", "chk-hb-two", "heartbeat", "eu", false},
		{"job-em-mixed-null", "chk-em-mixed", "email", nil, false},
		{"job-em-mixed-eu", "chk-em-mixed", "email", "eu", false},
		{"job-hb-leased", "chk-hb-leased", "heartbeat", "eu", true},
		{"job-http-eu", "chk-http-two", "http", "eu", false},
		{"job-http-us", "chk-http-two", "http", "us", false},
	}
	for _, j := range jobs {
		var lease any
		if j.leased {
			lease = "wrk-1"
		}

		exec(`insert into check_jobs (uid, organization_uid, check_uid, region, type, period, scheduled_at,
		        lease_worker_uid, lease_expires_at, lease_starts)
		      values (?, 'org-1', ?, ?, ?, '00:01:00', '2026-09-25 10:00:00', ?, ?, ?)`,
			j.uid, j.checkUID, j.region, j.checkType, lease,
			map[bool]any{true: "2026-09-25 10:02:00", false: nil}[j.leased],
			map[bool]int{true: 1, false: 0}[j.leased])
	}

	execMigrationFile(ctx, t, database, "024_v0_33_0.up.sql")

	regionsOf := func(checkUID string) string {
		var regions string
		r.NoError(database.QueryRowContext(ctx, `select regions from checks where uid = ?`, checkUID).Scan(&regions))

		return regions
	}

	type jobRow struct {
		uid    string
		region sql.NullString
		lease  sql.NullString
		starts int
	}

	jobsOf := func(checkUID string) []jobRow {
		rows, qErr := database.QueryContext(ctx,
			`select uid, region, lease_worker_uid, lease_starts from check_jobs where check_uid = ? order by uid`,
			checkUID)
		r.NoError(qErr)

		defer func() { _ = rows.Close() }()

		var out []jobRow

		for rows.Next() {
			var row jobRow
			r.NoError(rows.Scan(&row.uid, &row.region, &row.lease, &row.starts))
			out = append(out, row)
		}

		r.NoError(rows.Err())

		return out
	}

	for _, passive := range []string{"chk-hb-two", "chk-em-mixed", "chk-hb-leased"} {
		r.Equalf("[]", regionsOf(passive), "%s must lose its regions", passive)

		got := jobsOf(passive)
		r.Lenf(got, 1, "%s must keep exactly one job", passive)
		r.Falsef(got[0].region.Valid, "%s's job must have no region", passive)
		r.Falsef(got[0].lease.Valid, "%s's job must carry no lease", passive)
		r.Zero(got[0].starts)
	}

	r.Equal("job-hb-two-a", jobsOf("chk-hb-two")[0].uid, "the lowest-uid regional job is the survivor")
	r.Equal("job-em-mixed-null", jobsOf("chk-em-mixed")[0].uid, "an existing NULL job wins over regional ones")

	r.Equal(`["eu","us"]`, regionsOf("chk-http-two"), "an active check keeps its regions")
	r.Len(jobsOf("chk-http-two"), 2, "an active check keeps its regional jobs")
}
