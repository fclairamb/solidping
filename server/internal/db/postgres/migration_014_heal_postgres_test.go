package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbcaptest"
	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

// portMigration014Heal is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portMigration014Heal = 15474

// workersCapabilitiesShape describes the schema 013 is supposed to leave
// behind, read back from the catalog rather than assumed.
type workersCapabilitiesShape struct {
	ColumnType     string
	ConstraintExpr string
	Comment        string
}

func readCapabilitiesShape(ctx context.Context, t *testing.T, svc *Service) workersCapabilitiesShape {
	t.Helper()

	var shape workersCapabilitiesShape

	err := svc.DB().NewRaw(`
		SELECT coalesce(format_type(a.atttypid, a.atttypmod), '') AS column_type
		FROM pg_attribute a
		WHERE a.attrelid = 'workers'::regclass AND a.attname = 'capabilities' AND NOT a.attisdropped
	`).Scan(ctx, &shape.ColumnType)
	if err != nil {
		// No column at all: leave the zero value, the caller asserts on it.
		return shape
	}

	// pg_get_constraintdef renders the CHECK canonically, so two databases that
	// enforce the same rule produce the same string regardless of how it was
	// written.
	_ = svc.DB().NewRaw(`
		SELECT coalesce(pg_get_constraintdef(c.oid), '')
		FROM pg_constraint c
		WHERE c.conrelid = 'workers'::regclass AND c.conname = 'workers_capabilities_shape'
	`).Scan(ctx, &shape.ConstraintExpr)

	_ = svc.DB().NewRaw(`
		SELECT coalesce(col_description(a.attrelid, a.attnum), '')
		FROM pg_attribute a
		WHERE a.attrelid = 'workers'::regclass AND a.attname = 'capabilities'
	`).Scan(ctx, &shape.Comment)

	return shape
}

// TestMigration014PostgresHealAndGuard covers, on one embedded instance, the
// two halves of spec 2026-08-18-02 on the Postgres side: 014 heals a database
// that silently skipped 013's rewritten content, and the startup guard refuses
// to boot once an applied migration's content changes.
//
// A Postgres dev database migrated mid-batch desyncs exactly the same way a
// SQLite one does — bun keys on the numeric prefix in both dialects — so the
// heal is not SQLite-only housekeeping.
func TestMigration014PostgresHealAndGuard(t *testing.T) {
	t.Parallel()

	svc := newCapPG(t, portMigration014Heal)
	ctx := t.Context()
	r := require.New(t)

	healthy := readCapabilitiesShape(ctx, t, svc)
	r.Equal("text[]", healthy.ColumnType, "a correctly-migrated database has the column")
	r.NotEmpty(healthy.ConstraintExpr, "a correctly-migrated database has the shape constraint")
	r.NotEmpty(healthy.Comment, "a correctly-migrated database has the column comment")

	// --- 014 heals a database that skipped 013 ---
	//
	// The phases below run in sequence on ONE embedded instance and each
	// depends on the state the previous one left: they are deliberately not
	// subtests, because parallel subtests over a shared database would race
	// and a skipped phase would prove nothing.
	{
		// Reproduce the incident: the DDL is gone, bun_migrations still claims
		// 013 was applied, and 014 has not been seen yet.
		for _, stmt := range []string{
			"alter table workers drop constraint workers_capabilities_shape",
			"alter table workers drop column capabilities",
			"delete from bun_migrations where name = '014'",
			"delete from migration_checksums where name = '014'",
		} {
			_, err := svc.DB().ExecContext(ctx, stmt)
			r.NoError(err, "desync step %q", stmt)
		}

		// Negative control: the reproduction is genuinely broken.
		broken := readCapabilitiesShape(ctx, t, svc)
		r.Empty(broken.ColumnType, "the reproduction must actually be missing the column")

		r.NoError(svc.Initialize(ctx), "the fixed server must boot against a desynced database")

		r.Equal(healthy, readCapabilitiesShape(ctx, t, svc),
			"after 014 a healed database and a correctly-migrated one must be schema-identical")

		var applied int
		r.NoError(svc.DB().NewRaw(
			"SELECT count(*) FROM bun_migrations WHERE name = '014'").Scan(ctx, &applied))
		r.Equal(1, applied)
	}

	// --- the healed column enforces the shape rules, not just its existence ---
	{
		cases := dbcaptest.SharedCases()
		r.NotEmpty(cases)

		for i, tc := range cases {
			slug := fmt.Sprintf("pg-healed-%d", i)
			_, err := svc.DB().ExecContext(ctx, fmt.Sprintf(
				"INSERT INTO workers (uid, slug, name, capabilities) VALUES (gen_random_uuid(), '%s', '%s', %s)",
				slug, slug, tc.PostgresLiteral,
			))

			if tc.Accepted {
				r.NoError(err, "case %q must be accepted after the heal", tc.Name)
			} else {
				r.Error(err, "case %q must still be rejected after the heal", tc.Name)
			}
		}
	}

	// --- positive control: 014 is a no-op on a healthy database ---
	{
		before := readCapabilitiesShape(ctx, t, svc)

		_, err := svc.DB().ExecContext(ctx, "delete from bun_migrations where name = '014'")
		r.NoError(err)
		_, err = svc.DB().ExecContext(ctx, "delete from migration_checksums where name = '014'")
		r.NoError(err)

		r.NoError(svc.Initialize(ctx), "014 must be a no-op on a healthy database")
		r.Equal(before, readCapabilitiesShape(ctx, t, svc))
	}

	// --- the guard fails the boot once an applied migration's content changes ---
	{
		// Positive control: unchanged, it boots.
		r.NoError(svc.Initialize(ctx))

		var original string
		r.NoError(svc.DB().NewRaw(
			"SELECT checksum FROM migration_checksums WHERE name = '013'").Scan(ctx, &original))
		r.NotEmpty(original)

		_, err := svc.DB().ExecContext(ctx,
			"UPDATE migration_checksums SET checksum = 'deadbeef' WHERE name = '013'")
		r.NoError(err)

		err = svc.Initialize(ctx)
		r.Error(err)
		r.ErrorIs(err, migrationguard.ErrChecksumMismatch)
		r.Contains(err.Error(), "013_v0_16_0")

		// Restore so the shared instance stays usable.
		_, err = svc.DB().ExecContext(ctx,
			"UPDATE migration_checksums SET checksum = ? WHERE name = '013'", original)
		r.NoError(err)
		r.NoError(svc.Initialize(ctx))
	}
}
