package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestMigration024DropsTheDegradedDryRun proves the drop-degraded-dry-run
// section of 024 (spec 2026-09-24-08) on a real v0.32 database, where 023 had
// added degraded_would_fire_at and a check may carry a stamp:
//
//	chk-off-stamped:  off, stamped, with an open degraded incident → the stamp
//	                  column is gone and the incident closes as 'disabled'
//	chk-on:           on, with an open degraded incident → untouched (positive
//	                  control: the evaluator still owns it)
//	chk-off-outage:   off, with an open OUTAGE → untouched (only degraded
//	                  incidents are this section's business)
//
// A row inserted before 023 ran keeps degraded_enabled = false (the rollout
// rule): the pre-existing checks stay off.
func TestMigration024DropsTheDegradedDryRun(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := database.ExecContext(ctx, query, args...)
		r.NoError(execErr, query)
	}

	// Up to 022, then a check that predates degraded detection, then 023.
	for _, name := range append(migrationsBefore021(), "021_v0_28_0.up.sql", "022_v0_30_0.up.sql") {
		execMigrationFile(ctx, t, database, name)
	}

	exec(`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`)
	exec(`insert into checks (uid, organization_uid, slug, type, config, period)
	      values ('chk-legacy', 'org-1', 'legacy', 'http', '{}', '00:01:00')`)

	execMigrationFile(ctx, t, database, "023_v0_32_0.up.sql")

	for _, c := range []struct {
		uid, slug string
		enabled   bool
		stamped   bool
	}{
		{"chk-off-stamped", "off-stamped", false, true},
		{"chk-on", "on-enabled", true, false},
		{"chk-off-outage", "off-outage", false, false},
	} {
		var stamp any
		if c.stamped {
			stamp = "2026-09-24 14:37:00"
		}

		exec(`insert into checks (uid, organization_uid, slug, type, config, period, degraded_enabled,
		        degraded_would_fire_at)
		      values (?, 'org-1', ?, 'http', '{}', '00:01:00', ?, ?)`, c.uid, c.slug, c.enabled, stamp)
	}

	for i, inc := range []struct{ uid, checkUID, kind string }{
		{"inc-off-degraded", "chk-off-stamped", models.IncidentKindDegraded},
		{"inc-on-degraded", "chk-on", models.IncidentKindDegraded},
		{"inc-off-outage", "chk-off-outage", models.IncidentKindCheck},
	} {
		exec(`insert into incidents (uid, organization_uid, check_uid, state, started_at, number, kind)
		      values (?, 'org-1', ?, 1, '2026-09-24 14:00:00', ?, ?)`, inc.uid, inc.checkUID, i+1, inc.kind)
	}

	execMigrationFile(ctx, t, database, "024_v0_33_0.up.sql")

	var stampColumns int
	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from pragma_table_info('checks') where name = 'degraded_would_fire_at'`,
	).Scan(&stampColumns))
	r.Zero(stampColumns, "the dry-run stamp column is dropped")

	var legacyEnabled bool
	r.NoError(database.QueryRowContext(ctx,
		`select degraded_enabled from checks where uid = 'chk-legacy'`).Scan(&legacyEnabled))
	r.False(legacyEnabled, "a check that predates degraded detection stays off")

	type incidentRow struct {
		state          int
		resolvedAt     sql.NullString
		resolutionType sql.NullString
	}

	incident := func(uid string) incidentRow {
		var row incidentRow
		r.NoError(database.QueryRowContext(ctx,
			`select state, resolved_at, resolution_type from incidents where uid = ?`, uid,
		).Scan(&row.state, &row.resolvedAt, &row.resolutionType))

		return row
	}

	closed := incident("inc-off-degraded")
	r.Equal(int(models.IncidentStateResolved), closed.state,
		"the new sweep never reads a disabled check, so its open degraded incident closes here")
	r.True(closed.resolvedAt.Valid)
	r.Equal(models.ResolutionTypeDisabled, closed.resolutionType.String)

	r.Equal(int(models.IncidentStateActive), incident("inc-on-degraded").state,
		"a degraded incident on an enabled check is still the evaluator's to close")
	r.Equal(int(models.IncidentStateActive), incident("inc-off-outage").state,
		"an outage is never touched")

	var indexSQL string
	r.NoError(database.QueryRowContext(ctx,
		`select sql from sqlite_master where type = 'index' and name = 'idx_checks_degraded_eval'`,
	).Scan(&indexSQL))
	r.Contains(indexSQL, "degraded_enabled", "the sweep index carries the new filter")
}

// TestMigration024DegradedDryRunDownRestoresTheColumn proves the down half:
// the column comes back (empty) so the previous schema reads the table.
func TestMigration024DegradedDryRunDownRestoresTheColumn(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	for _, name := range append(migrationsBefore024(), "024_v0_33_0.up.sql") {
		execMigrationFile(ctx, t, database, name)
	}

	execMigrationFile(ctx, t, database, "024_v0_33_0.down.sql")

	var stampColumns int
	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from pragma_table_info('checks') where name = 'degraded_would_fire_at'`,
	).Scan(&stampColumns))
	r.Equal(1, stampColumns)
}

// TestDegradedSweepSkipsDisabledChecks pins the sweep's work queue on SQLite:
// a check with degraded detection off is not listed, an enabled one is.
func TestDegradedSweepSkipsDisabledChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("degraded-sweep", "Degraded Sweep")
	r.NoError(svc.CreateOrganization(ctx, org))

	on := models.NewCheck(org.UID, "ds-on", "http")
	r.NoError(svc.CreateCheck(ctx, on))

	off := models.NewCheck(org.UID, "ds-off", "http")
	off.DegradedEnabled = false
	r.NoError(svc.CreateCheck(ctx, off))

	queue, err := svc.ListChecksForDegradedEval(ctx, 0)
	r.NoError(err)
	r.Len(queue, 1)
	r.Equal(on.UID, queue[0].UID)
}
