package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portDegradedDryRunMigration is distinct from every other _postgres_test.go
// embedded port in the repo.
const portDegradedDryRunMigration = 15543

// degradedDryRunSection returns the statements of the drop-degraded-dry-run
// section of 024_v0_33_0.up.sql, read from the file itself so the test can
// never drift from what ships.
func degradedDryRunSection(t *testing.T) []string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("migrations", "024_v0_33_0.up.sql"))
	require.NoError(t, err)

	const banner = "-- SECTION: drop-degraded-dry-run  (spec"

	idx := strings.Index(string(content), banner)
	require.GreaterOrEqual(t, idx, 0, "the section banner must be present")

	section := string(content)[idx:]
	if next := strings.Index(section[len(banner):], "\n-- SECTION: "); next >= 0 {
		section = section[:len(banner)+next]
	}

	var statements []string

	for _, chunk := range strings.Split(section, "--bun:split") {
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

	return statements
}

// TestMigration024DropsTheDegradedDryRun_Postgres is the Postgres twin of the
// SQLite section test (spec 2026-09-24-08). Initialize() already ran the
// section on an empty database, so the stamp column must be gone and the sweep
// index must carry the new filter; the section is then re-run on seeded rows
// (every statement is idempotent) to prove the open-incident cleanup:
//
//	off check, open degraded incident → closed as 'disabled'
//	on check, open degraded incident  → untouched (positive control)
//	off check, open outage            → untouched
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestMigration024DropsTheDegradedDryRun_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portDegradedDryRunMigration, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	var stampColumns int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from information_schema.columns
		  where table_name = 'checks' and column_name = 'degraded_would_fire_at'`,
	).Scan(&stampColumns))
	r.Zero(stampColumns, "the dry-run stamp column is dropped")

	var indexDef string
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select indexdef from pg_indexes where indexname = 'idx_checks_degraded_eval'`,
	).Scan(&indexDef))
	r.Contains(indexDef, "degraded_enabled", "the sweep index carries the new filter")

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr, query)
	}

	const (
		orgUID       = "00000000-0000-0000-0000-00000000d000"
		checkOff     = "00000000-0000-0000-0000-00000000d001"
		checkOn      = "00000000-0000-0000-0000-00000000d002"
		checkOutage  = "00000000-0000-0000-0000-00000000d003"
		incOff       = "00000000-0000-0000-0000-00000000e001"
		incOn        = "00000000-0000-0000-0000-00000000e002"
		incOffOutage = "00000000-0000-0000-0000-00000000e003"
	)

	exec(`insert into organizations (uid, slug, name) values (?, 'acme-degraded-dry', 'Acme')`, orgUID)

	for _, c := range []struct {
		uid, slug string
		enabled   bool
	}{
		{checkOff, "dd-off", false},
		{checkOn, "dd-on", true},
		{checkOutage, "dd-off-outage", false},
	} {
		exec(`insert into checks (uid, organization_uid, slug, type, config, period, degraded_enabled)
		      values (?, ?, ?, 'http', '{}', interval '1 minute', ?)`, c.uid, orgUID, c.slug, c.enabled)
	}

	for i, inc := range []struct{ uid, checkUID, kind string }{
		{incOff, checkOff, models.IncidentKindDegraded},
		{incOn, checkOn, models.IncidentKindDegraded},
		{incOffOutage, checkOutage, models.IncidentKindCheck},
	} {
		exec(`insert into incidents (uid, organization_uid, check_uid, state, started_at, number, kind)
		      values (?, ?, ?, 1, now() - interval '1 hour', ?, ?)`, inc.uid, orgUID, inc.checkUID, i+1, inc.kind)
	}

	for _, statement := range degradedDryRunSection(t) {
		exec(statement)
	}

	type incidentRow struct {
		state          int
		resolved       bool
		resolutionType *string
	}

	incident := func(uid string) incidentRow {
		var row incidentRow
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select state, resolved_at is not null, resolution_type from incidents where uid = ?`, uid,
		).Scan(&row.state, &row.resolved, &row.resolutionType))

		return row
	}

	closed := incident(incOff)
	r.Equal(int(models.IncidentStateResolved), closed.state)
	r.True(closed.resolved)
	r.NotNil(closed.resolutionType)
	r.Equal(models.ResolutionTypeDisabled, *closed.resolutionType)

	r.Equal(int(models.IncidentStateActive), incident(incOn).state,
		"a degraded incident on an enabled check is still the evaluator's to close")
	r.Equal(int(models.IncidentStateActive), incident(incOffOutage).state, "an outage is never touched")
}
