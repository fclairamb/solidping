package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestMigration021BackfillsPeriodBelowFloor is the SQLite half of the
// period-below-floor-backfill section's proof (spec 2026-09-11-07): every
// OTHER test in the repo runs against a freshly-migrated database, where this
// section's WHERE clause matches nothing. This one seeds a real pre-021
// database with rows that exercise every branch of the backfill's predicate,
// then asserts only the intended rows moved.
func TestMigration021BackfillsPeriodBelowFloor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	for _, name := range migrationsBefore021() {
		execMigrationFile(ctx, t, database, name)
	}

	_, err = database.ExecContext(ctx,
		`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`)
	r.NoError(err)

	seeds := []struct {
		uid, slug, checkType, period string
	}{
		// The fingerprint: at the flat 1m default AND below the type's own
		// floor. Must be raised to the type's own DefaultPeriod.
		{"chk-ssl-flat", "ssl-flat", "ssl", "00:01:00"},
		{"chk-domain-flat", "domain-flat", "domain", "00:01:00"},
		{"chk-dnsbl-flat", "dnsbl-flat", "dnsbl", "00:01:00"},
		// Below its own floor, but NOT the 1m fingerprint — a value some
		// human typed through an earlier path. Grandfathered: left alone.
		{"chk-dnsbl-human", "dnsbl-human", "dnsbl", "00:05:00"},
		// At the flat 1m default, but 1m is not below THEIR OWN floor
		// (js: 30s, browser: 1m) — left alone.
		{"chk-js-flat", "js-flat", "js", "00:01:00"},
		{"chk-browser-flat", "browser-flat", "browser", "00:01:00"},
		// A type with no floor at all, at 1m — left alone.
		{"chk-http-flat", "http-flat", "http", "00:01:00"},
	}

	for _, seed := range seeds {
		_, err = database.ExecContext(ctx,
			`insert into checks (uid, organization_uid, slug, type, config, period)
			 values (?, 'org-1', ?, ?, '{}', ?)`,
			seed.uid, seed.slug, seed.checkType, seed.period)
		r.NoError(err)
	}

	execMigrationFile(ctx, t, database, "021_v0_28_0.up.sql")

	period := func(uid string) string {
		var p string
		r.NoError(database.QueryRowContext(ctx, `select period from checks where uid = ?`, uid).Scan(&p))

		return p
	}

	r.Equal("06:00:00", period("chk-ssl-flat"), "ssl at the 1m fingerprint must be raised to its 6h default")
	r.Equal("24:00:00", period("chk-domain-flat"), "domain at the 1m fingerprint must be raised to its 24h default")
	r.Equal("01:00:00", period("chk-dnsbl-flat"), "dnsbl at the 1m fingerprint must be raised to its 1h default")

	r.Equal("00:05:00", period("chk-dnsbl-human"), "a below-floor period NOT at the 1m fingerprint is grandfathered")
	r.Equal("00:01:00", period("chk-js-flat"), "js at 1m is not below its own 30s floor")
	r.Equal("00:01:00", period("chk-browser-flat"), "browser at 1m is not below its own 1m floor")
	r.Equal("00:01:00", period("chk-http-flat"), "http has no floor at all, so it is never touched")
}
