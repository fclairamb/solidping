package postgres

import (
	"os"
	"path/filepath"
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
		wantAuto                      bool
	}{
		{"00000000-0000-0000-0000-00000000b001", "on-default", "http", "{gravelines}", true},
		{"00000000-0000-0000-0000-00000000b002", "elsewhere", "http", "{paris}", false},
		{"00000000-0000-0000-0000-00000000b003", "superset", "http", "{gravelines,paris}", false},
		{"00000000-0000-0000-0000-00000000b004", "heartbeat", "heartbeat", "{gravelines}", false},
	}

	for _, c := range cases {
		exec(`insert into checks (uid, organization_uid, slug, type, config, period, regions)
		      values (?, ?, ?, ?, '{}', interval '1 minute', ?::text[])`, c.uid, orgUID, c.slug, c.checkType, c.regions)
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
			r.Equal(1, *row.RegionCount)
			r.Empty(row.RegionPool)
		} else {
			r.Equalf("pinned", row.Placement, "%s stays pinned", c.slug)
			r.Nil(row.RegionCount)
		}
	}
}
