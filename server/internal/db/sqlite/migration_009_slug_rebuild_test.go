package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestMigration009SlugRebuildPreservesData proves that the *_new table
// rebuild in 009_v0_8_0.up.sql (which widens the check_groups/checks/
// status_pages/status_page_sections slug length CHECK constraints from
// 40/50 to 100 chars — spec 2026-08-04-01) does not scramble or drop
// existing data. `insert into X_new (...) select (...) from X` is
// positional: this test seeds a row in each of the four rebuilt tables
// (including the checks table's later-added columns — flapping_window_seconds,
// config_sealed, region_spread, etc. — the ones most likely to drift out of
// order) using only migrations 001-008, then runs 009 and asserts every
// field is byte-for-byte unchanged and the check_group_uid FK relationship
// survives.
//
// This runs the raw migration files directly (not through bun's migrator)
// so the test can stop the chain after 008 before seeding data. A single,
// dedicated connection is used throughout — matching sqlite.Service's own
// MaxOpenConns(1) — so the PRAGMA foreign_keys toggling inside 009's SQL
// takes effect exactly as it would in production.
func TestMigration009SlugRebuildPreservesData(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	db, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	// Apply every migration up to and including 008 (pre-slug-rebuild schema).
	for _, name := range []string{
		"001_v0_1_0.up.sql", "002_v0_2_0.up.sql", "003_v0_2_1.up.sql",
		"004_v0_3_0.up.sql", "005_v0_4_0.up.sql", "006_v0_5_0.up.sql",
		"007_v0_6_0.up.sql", "008_v0_7_0.up.sql",
	} {
		execMigrationFile(ctx, t, db, name)
	}

	// Seed one organization plus one row in each of the four tables the 009
	// slug-rebuild block touches, filling every column with a distinguishable,
	// non-default value so a positional column-order drift would corrupt (not
	// merely default) a field.
	_, err = db.ExecContext(ctx,
		`insert into organizations (uid, slug, name) values ('org-1','preserve-org','Preserve Org')`)
	r.NoError(err)

	_, err = db.ExecContext(ctx, `insert into check_groups
		(uid, organization_uid, name, slug, description, sort_order, created_at, updated_at)
		values ('cg-1','org-1','Group One','group-one','a group', 7, '2020-01-01T00:00:00Z', '2020-01-02T00:00:00Z')`)
	r.NoError(err)

	_, err = db.ExecContext(ctx, `insert into checks (
		uid, organization_uid, check_group_uid, name, slug, description, type, config, regions,
		enabled, internal, period, escalation_threshold, reopen_cooldown_multiplier, status,
		status_streak, status_changed_at, config_private, config_private_keys,
		confirmation_period_seconds, recovery_period_seconds, first_failure_at,
		first_success_since_failure_at, created_at, updated_at, flapping_window_seconds,
		flap_backoff_factor, max_recovery_multiplier, flap_count, last_outage_at, config_sealed,
		region_spread
	) values (
		'chk-1','org-1','cg-1','Check One','check-one','a check','http','{"url":"https://example.com"}','["eu"]',
		0, 1, '00:05:00', 42, 3, 2,
		9, '2020-01-03T00:00:00Z', 'ENCRYPTED_BLOB', '["token"]',
		30, 60, '2020-01-04T00:00:00Z',
		'2020-01-05T00:00:00Z', '2020-01-06T00:00:00Z', '2020-01-07T00:00:00Z', 12345,
		5, 99, 3, '2020-01-08T00:00:00Z', 'SEALED_BLOB',
		'{"eu":1}'
	)`)
	r.NoError(err)

	_, err = db.ExecContext(ctx, `insert into status_pages
		(uid, organization_uid, name, slug, description, is_default, history_days,
		 custom_domain, custom_css, created_at, updated_at)
		values ('sp-1','org-1','Page One','page-one','a page', 1, 30,
		        'status.example.com', 'body{color:red}',
		        '2020-02-01T00:00:00Z', '2020-02-02T00:00:00Z')`)
	r.NoError(err)

	_, err = db.ExecContext(ctx, `insert into status_page_sections
		(uid, status_page_uid, name, slug, position, created_at, updated_at)
		values ('sec-1','sp-1','Section One','section-one', 4, '2020-03-01T00:00:00Z', '2020-03-02T00:00:00Z')`)
	r.NoError(err)

	// Run the migration under test.
	execMigrationFile(ctx, t, db, "009_v0_8_0.up.sql")

	// check_groups: every field, byte for byte.
	var (
		cgName, cgSlug, cgDesc, cgCreated, cgUpdated string
		cgSortOrder                                  int
	)
	r.NoError(db.QueryRowContext(ctx,
		`select name, slug, description, sort_order, created_at, updated_at from check_groups where uid = 'cg-1'`,
	).Scan(&cgName, &cgSlug, &cgDesc, &cgSortOrder, &cgCreated, &cgUpdated))
	r.Equal("Group One", cgName)
	r.Equal("group-one", cgSlug)
	r.Equal("a group", cgDesc)
	r.Equal(7, cgSortOrder)
	r.Equal("2020-01-01T00:00:00Z", cgCreated)
	r.Equal("2020-01-02T00:00:00Z", cgUpdated)

	// checks: every field, including the FK to check_groups and the
	// later-added columns most at risk of positional drift.
	var (
		chkGroupUID, chkName, chkSlug, chkDesc, chkType, chkConfig, chkRegions string
		chkEnabled, chkInternal, chkStatus, chkStreak                          int
		chkPeriod                                                              string
		chkEscThreshold, chkReopenMult                                         int
		chkConfigPrivate, chkConfigPrivateKeys                                 string
		chkConfirmSec, chkRecoverySec                                          int
		chkFirstFailAt, chkFirstSuccessAt, chkCreated, chkUpdated              string
		chkFlapWindow, chkFlapBackoff, chkMaxRecoveryMult, chkFlapCount        int
		chkLastOutageAt, chkConfigSealed, chkRegionSpread                      string
	)
	r.NoError(db.QueryRowContext(ctx, `select
		check_group_uid, name, slug, description, type, config, regions,
		enabled, internal, period, escalation_threshold, reopen_cooldown_multiplier, status,
		status_streak, config_private, config_private_keys,
		confirmation_period_seconds, recovery_period_seconds, first_failure_at,
		first_success_since_failure_at, created_at, updated_at, flapping_window_seconds,
		flap_backoff_factor, max_recovery_multiplier, flap_count, last_outage_at, config_sealed,
		region_spread
		from checks where uid = 'chk-1'`,
	).Scan(
		&chkGroupUID, &chkName, &chkSlug, &chkDesc, &chkType, &chkConfig, &chkRegions,
		&chkEnabled, &chkInternal, &chkPeriod, &chkEscThreshold, &chkReopenMult, &chkStatus,
		&chkStreak, &chkConfigPrivate, &chkConfigPrivateKeys,
		&chkConfirmSec, &chkRecoverySec, &chkFirstFailAt,
		&chkFirstSuccessAt, &chkCreated, &chkUpdated, &chkFlapWindow,
		&chkFlapBackoff, &chkMaxRecoveryMult, &chkFlapCount, &chkLastOutageAt, &chkConfigSealed,
		&chkRegionSpread,
	))
	r.Equal("cg-1", chkGroupUID, "FK to check_groups must survive the check_groups rebuild")
	r.Equal("Check One", chkName)
	r.Equal("check-one", chkSlug)
	r.Equal("a check", chkDesc)
	r.Equal("http", chkType)
	r.JSONEq(`{"url":"https://example.com"}`, chkConfig)
	r.Equal(`["eu"]`, chkRegions)
	r.Equal(0, chkEnabled)
	r.Equal(1, chkInternal)
	r.Equal("00:05:00", chkPeriod)
	r.Equal(42, chkEscThreshold)
	r.Equal(3, chkReopenMult)
	r.Equal(2, chkStatus)
	r.Equal(9, chkStreak)
	r.Equal("ENCRYPTED_BLOB", chkConfigPrivate)
	r.Equal(`["token"]`, chkConfigPrivateKeys)
	r.Equal(30, chkConfirmSec)
	r.Equal(60, chkRecoverySec)
	r.Equal("2020-01-04T00:00:00Z", chkFirstFailAt)
	r.Equal("2020-01-05T00:00:00Z", chkFirstSuccessAt)
	r.Equal("2020-01-06T00:00:00Z", chkCreated)
	r.Equal("2020-01-07T00:00:00Z", chkUpdated)
	r.Equal(12345, chkFlapWindow)
	r.Equal(5, chkFlapBackoff)
	r.Equal(99, chkMaxRecoveryMult)
	r.Equal(3, chkFlapCount)
	r.Equal("2020-01-08T00:00:00Z", chkLastOutageAt)
	r.Equal("SEALED_BLOB", chkConfigSealed)
	r.Equal(`{"eu":1}`, chkRegionSpread)

	// status_pages: every field.
	var (
		spName, spSlug, spDesc, spCustomDomain, spCustomCSS, spCreated, spUpdated string
		spIsDefault, spHistoryDays                                                int
	)
	r.NoError(db.QueryRowContext(ctx,
		`select name, slug, description, is_default, history_days, custom_domain, custom_css, created_at, updated_at
		 from status_pages where uid = 'sp-1'`,
	).Scan(&spName, &spSlug, &spDesc, &spIsDefault, &spHistoryDays, &spCustomDomain, &spCustomCSS, &spCreated, &spUpdated))
	r.Equal("Page One", spName)
	r.Equal("page-one", spSlug)
	r.Equal("a page", spDesc)
	r.Equal(1, spIsDefault)
	r.Equal(30, spHistoryDays)
	r.Equal("status.example.com", spCustomDomain)
	r.Equal("body{color:red}", spCustomCSS)
	r.Equal("2020-02-01T00:00:00Z", spCreated)
	r.Equal("2020-02-02T00:00:00Z", spUpdated)

	// status_page_sections: every field, including the FK to status_pages.
	var (
		secPageUID, secName, secSlug, secCreated, secUpdated string
		secPosition                                          int
	)
	r.NoError(db.QueryRowContext(ctx,
		`select status_page_uid, name, slug, position, created_at, updated_at
		 from status_page_sections where uid = 'sec-1'`,
	).Scan(&secPageUID, &secName, &secSlug, &secPosition, &secCreated, &secUpdated))
	r.Equal("sp-1", secPageUID, "FK to status_pages must survive the status_pages rebuild")
	r.Equal("Section One", secName)
	r.Equal("section-one", secSlug)
	r.Equal(4, secPosition)
	r.Equal("2020-03-01T00:00:00Z", secCreated)
	r.Equal("2020-03-02T00:00:00Z", secUpdated)

	// Foreign key enforcement must be back on after the migration.
	var fkEnabled int
	r.NoError(db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fkEnabled))
	r.Equal(1, fkEnabled, "foreign_keys must be re-enabled at the end of the migration")

	var violations int
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	r.NoError(err)

	defer func() { r.NoError(rows.Close()) }()

	for rows.Next() {
		violations++
	}
	r.NoError(rows.Err())
	r.Zero(violations, "no dangling foreign keys after the rebuild")
}

// execMigrationFile execs one migration file's raw SQL content against db.
// Lines starting with "--bun:split" are bun's own multi-statement directive;
// they are plain SQL comments to a standard driver, so no special handling
// is needed here beyond reading and executing the whole file as one script.
func execMigrationFile(ctx context.Context, t *testing.T, db *sql.DB, name string) {
	t.Helper()
	r := require.New(t)

	content, err := os.ReadFile(filepath.Join("migrations", name))
	r.NoError(err)

	_, err = db.ExecContext(ctx, string(content))
	r.NoError(err, "migration file %s failed", name)
}
