package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portPlacementMigration is distinct from every other _postgres_test.go
// embedded port in the repo.
const portPlacementMigration = 15541

// placementMigrationSection returns the data statement of the
// auto-region-placement section of 024_v0_33_0.up.sql (the last statement of
// the section, the defaulted-checks conversion), read from the file itself.
func placementMigrationSection(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("migrations", "024_v0_33_0.up.sql"))
	require.NoError(t, err)

	const banner = "-- SECTION: auto-region-placement  (spec"

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

	last := statements[len(statements)-1]
	require.True(t, strings.HasPrefix(last, "update checks c"), "the conversion is the section's last statement")

	return last
}

// TestMigration024DefaultedChecksBecomeAuto_Postgres is the Postgres twin of
// the SQLite placement migration test (spec 2026-09-25-06, the "67 checks on
// gravelines" case): only a check whose regions are exactly the system
// default_regions, as a set, becomes auto — region_count = its region count,
// empty pool, regions untouched. Initialize() already ran the section on an
// empty database; the conversion is re-run here on seeded rows.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestMigration024DefaultedChecksBecomeAuto_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portPlacementMigration, RunMode: runModeTest})
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

	const orgUID = "00000000-0000-0000-0000-00000000b000"

	exec(`insert into organizations (uid, slug, name) values (?, 'acme-placement', 'Acme')`, orgUID)
	exec(`delete from parameters where organization_uid is null and key = 'default_regions'`)
	exec(`insert into parameters (uid, organization_uid, key, value)
	      values ('00000000-0000-0000-0000-00000000b0ff', null, 'default_regions', '{"value": ["gravelines"]}')`)

	cases := []struct {
		uid, slug, checkType, regions string
		deleted                       bool
		wantAuto                      bool
		// wantCount is the region_count an "auto" case ends up with (ignored
		// for a pinned case).
		wantCount int
	}{
		{
			uid: "00000000-0000-0000-0000-00000000b001", slug: "on-default", checkType: "http",
			regions: "{gravelines}", wantAuto: true, wantCount: 1,
		},
		{
			uid: "00000000-0000-0000-0000-00000000b002", slug: "elsewhere", checkType: "http",
			regions: "{paris}", wantAuto: false,
		},
		{
			uid: "00000000-0000-0000-0000-00000000b003", slug: "superset", checkType: "http",
			regions: "{gravelines,paris}", wantAuto: false,
		},
		{
			uid: "00000000-0000-0000-0000-00000000b004", slug: "heartbeat", checkType: "heartbeat",
			regions: "{gravelines}", wantAuto: false,
		},
		// Negative controls (spec 2026-09-25-06 Part A3, HIGH PRIORITY: this
		// migration rewrites real production rows): a soft-deleted check
		// sitting exactly on the default regions must NOT be converted — it
		// is gone, not a candidate for failover — and neither must a
		// private-location check, which the migration's own type exclusion
		// list names alongside heartbeat/email.
		{
			uid: "00000000-0000-0000-0000-00000000b005", slug: "soft-deleted", checkType: "http",
			regions: "{gravelines}", deleted: true, wantAuto: false,
		},
		{
			uid: "00000000-0000-0000-0000-00000000b006", slug: "private-location", checkType: "private-location",
			regions: "{gravelines}", wantAuto: false,
		},
	}

	for _, c := range cases {
		exec(`insert into checks (uid, organization_uid, slug, type, config, period, regions)
		      values (?, ?, ?, ?, '{}', interval '1 minute', ?::text[])`, c.uid, orgUID, c.slug, c.checkType, c.regions)

		if c.deleted {
			exec(`update checks set deleted_at = now() where uid = ?`, c.uid)
		}
	}

	// Seed real check_jobs rows for the checks the migration touches (one per
	// region) and for a control it must leave pinned, so the assertions below
	// can prove the conversion changes ONLY placement/region_count/region_pool
	// — never the check's regions or its jobs (spec: "its cost and its normal
	// region stay the same").
	jobSlugs := []string{"on-default", "superset"}
	type seededJob struct {
		uid, region string
		scheduledAt string
	}

	var seededJobs []seededJob

	for _, c := range cases {
		if !slices.Contains(jobSlugs, c.slug) {
			continue
		}

		for i, region := range strings.Split(strings.Trim(c.regions, "{}"), ",") {
			jobUID := fmt.Sprintf("00000000-0000-0000-0000-00000000c%03d", len(seededJobs))
			scheduledAt := fmt.Sprintf("2026-09-2%d 10:00:00+00", i+1)

			exec(`insert into check_jobs (uid, organization_uid, check_uid, region, type, period, scheduled_at)
			      values (?, ?, ?, ?, ?, interval '1 minute', ?)`,
				jobUID, orgUID, c.uid, region, c.checkType, scheduledAt)

			seededJobs = append(seededJobs, seededJob{uid: jobUID, region: region, scheduledAt: scheduledAt})
		}
	}

	exec(placementMigrationSection(t))

	for _, c := range cases {
		var row struct {
			Placement   string   `bun:"placement"`
			RegionCount *int     `bun:"region_count"`
			RegionPool  []string `bun:"region_pool,array"`
			Regions     []string `bun:"regions,array"`
		}

		r.NoError(svc.DB().NewSelect().
			TableExpr("checks").
			ColumnExpr("placement, region_count, region_pool, regions").
			Where("uid = ?", c.uid).
			Scan(ctx, &row))

		if c.wantAuto {
			r.Equalf("auto", row.Placement, "%s sits on the system default and becomes auto", c.slug)
			r.NotNil(row.RegionCount)
			r.Equal(c.wantCount, *row.RegionCount)
			r.Empty(row.RegionPool)
		} else {
			r.Equalf("pinned", row.Placement, "%s stays pinned", c.slug)
			r.Nil(row.RegionCount)
		}
	}

	// The migration's one exception rewrites placement/region_count/region_pool
	// and nothing else: regions and check_jobs are byte-for-byte unchanged,
	// for BOTH the converted check (on-default) and the pinned control
	// (superset) that happens to also carry jobs.
	for _, c := range cases {
		if !slices.Contains(jobSlugs, c.slug) {
			continue
		}

		var row struct {
			Regions []string `bun:"regions,array"`
		}

		r.NoError(svc.DB().NewSelect().
			TableExpr("checks").ColumnExpr("regions").Where("uid = ?", c.uid).Scan(ctx, &row))
		r.Equalf(strings.Split(strings.Trim(c.regions, "{}"), ","), row.Regions, "%s keeps its regions untouched", c.slug)
	}

	for _, job := range seededJobs {
		var row struct {
			Region      string `bun:"region"`
			ScheduledAt string `bun:"scheduled_at"`
		}

		r.NoError(svc.DB().NewSelect().
			TableExpr("check_jobs").
			ColumnExpr("region, to_char(scheduled_at at time zone 'UTC', 'YYYY-MM-DD HH24:MI:SS') || '+00' as scheduled_at").
			Where("uid = ?", job.uid).
			Scan(ctx, &row))
		r.Equalf(job.region, row.Region, "job %s keeps its region", job.uid)
		r.Equalf(job.scheduledAt, row.ScheduledAt, "job %s keeps its schedule", job.uid)
	}

	jobCount, err := svc.DB().NewSelect().TableExpr("check_jobs").Count(ctx)
	r.NoError(err)
	r.Equalf(len(seededJobs), jobCount, "the migration writes no new job and deletes none")
}
