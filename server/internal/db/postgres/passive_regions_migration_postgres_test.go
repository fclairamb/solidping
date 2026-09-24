package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portPassiveRegions is distinct from every other _postgres_test.go embedded
// port in the repo.
const portPassiveRegions = 15518

// passiveRegionsSection returns the statements of the
// passive-checks-no-regions section of 024_v0_33_0.up.sql, read from the file
// itself so the test can never drift from what ships.
func passiveRegionsSection(t *testing.T) []string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("migrations", "024_v0_33_0.up.sql"))
	require.NoError(t, err)

	const banner = "-- SECTION: passive-checks-no-regions  (spec"

	idx := strings.Index(string(content), banner)
	require.GreaterOrEqual(t, idx, 0, "the section banner must be present")

	var statements []string

	for _, chunk := range strings.Split(string(content)[idx:], "--bun:split") {
		var kept []string

		for _, line := range strings.Split(chunk, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				kept = append(kept, line)
			}
		}

		if statement := strings.TrimSpace(strings.Join(kept, "\n")); statement != "" {
			statements = append(statements, statement)
		}
	}

	require.Len(t, statements, 4, "regions, two deletes, the conversion")

	return statements
}

// TestMigration024PassiveChecksLoseTheirRegions_Postgres is the Postgres twin
// of the SQLite section test (spec 2026-09-25-04): native text[] regions and
// uuid uids (min() over uuid needs the ::text cast) are exactly where the two
// dialects differ. Initialize() already ran the section once against an empty
// database; it is re-run here on seeded legacy rows.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestMigration024PassiveChecksLoseTheirRegions_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portPassiveRegions, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr, query)
	}

	const (
		orgUID    = "00000000-0000-0000-0000-00000000a000"
		workerUID = "00000000-0000-0000-0000-00000000a001"
		hbTwo     = "00000000-0000-0000-0000-00000000c001"
		emMixed   = "00000000-0000-0000-0000-00000000c002"
		hbLeased  = "00000000-0000-0000-0000-00000000c003"
		httpTwo   = "00000000-0000-0000-0000-00000000c004"
	)

	exec(`insert into organizations (uid, slug, name) values (?, 'acme-passive', 'Acme')`, orgUID)
	exec(`insert into workers (uid, slug, name) values (?, 'eu-worker', 'EU worker')`, workerUID)

	for _, c := range []struct{ uid, slug, checkType, regions string }{
		{hbTwo, "hb-two", "heartbeat", "{eu,us}"},
		{emMixed, "em-mixed", "email", "{eu}"},
		{hbLeased, "hb-leased", "heartbeat", "{eu}"},
		{httpTwo, "http-two", "http", "{eu,us}"},
	} {
		exec(`insert into checks (uid, organization_uid, slug, type, config, period, regions)
		      values (?, ?, ?, ?, '{}', interval '1 minute', ?::text[])`, c.uid, orgUID, c.slug, c.checkType, c.regions)
	}

	for _, j := range []struct {
		uid, checkUID, checkType string
		region                   any
		leased                   bool
	}{
		{"00000000-0000-0000-0000-00000000d0b2", hbTwo, "heartbeat", "us", false},
		{"00000000-0000-0000-0000-00000000d0b1", hbTwo, "heartbeat", "eu", false},
		{"00000000-0000-0000-0000-00000000d0c0", emMixed, "email", nil, false},
		{"00000000-0000-0000-0000-00000000d0c1", emMixed, "email", "eu", false},
		{"00000000-0000-0000-0000-00000000d0d1", hbLeased, "heartbeat", "eu", true},
		{"00000000-0000-0000-0000-00000000d0e1", httpTwo, "http", "eu", false},
		{"00000000-0000-0000-0000-00000000d0e2", httpTwo, "http", "us", false},
	} {
		var lease any
		if j.leased {
			lease = workerUID
		}

		exec(`insert into check_jobs (uid, organization_uid, check_uid, region, type, period, scheduled_at,
		        lease_worker_uid, lease_expires_at, lease_starts)
		      values (?, ?, ?, ?, ?, interval '1 minute', now(), ?,
		              case when ?::uuid is null then null else now() + interval '2 minutes' end, ?)`,
			j.uid, orgUID, j.checkUID, j.region, j.checkType, lease, lease, map[bool]int{true: 1, false: 0}[j.leased])
	}

	for _, statement := range passiveRegionsSection(t) {
		exec(statement)
	}

	regionCount := func(checkUID string) int {
		var n int
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select coalesce(array_length(regions, 1), 0) from checks where uid = ?`, checkUID).Scan(&n))

		return n
	}

	type jobRow struct {
		UID    string  `bun:"uid"`
		Region *string `bun:"region"`
		Lease  *string `bun:"lease_worker_uid"`
		Starts int     `bun:"lease_starts"`
	}

	jobsOf := func(checkUID string) []jobRow {
		var rows []jobRow
		r.NoError(svc.DB().NewSelect().
			TableExpr("check_jobs").
			ColumnExpr("uid::text AS uid, region, lease_worker_uid::text AS lease_worker_uid, lease_starts").
			Where("check_uid = ?", checkUID).
			OrderExpr("uid").
			Scan(ctx, &rows))

		return rows
	}

	for _, passive := range []string{hbTwo, emMixed, hbLeased} {
		r.Zerof(regionCount(passive), "%s must lose its regions", passive)

		got := jobsOf(passive)
		r.Lenf(got, 1, "%s must keep exactly one job", passive)
		r.Nilf(got[0].Region, "%s's job must have no region", passive)
		r.Nilf(got[0].Lease, "%s's job must carry no lease", passive)
		r.Zero(got[0].Starts)
	}

	r.Equal("00000000-0000-0000-0000-00000000d0b1", jobsOf(hbTwo)[0].UID, "the lowest-uid regional job survives")
	r.Equal("00000000-0000-0000-0000-00000000d0c0", jobsOf(emMixed)[0].UID, "an existing NULL job wins")

	r.Equal(2, regionCount(httpTwo), "an active check keeps its regions")
	r.Len(jobsOf(httpTwo), 2, "an active check keeps its regional jobs")
}
