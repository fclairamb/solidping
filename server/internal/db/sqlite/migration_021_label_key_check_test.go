package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// migrationsBefore021 is every SQLite migration up to (and excluding) 021 —
// the pre-021 schema, whose labels.key CHECK is the lax
// `length(key) between 1 and 50`.
func migrationsBefore021() []string {
	return []string{
		"001_v0_1_0.up.sql", "002_v0_2_0.up.sql", "003_v0_2_1.up.sql",
		"004_v0_3_0.up.sql", "005_v0_4_0.up.sql", "006_v0_5_0.up.sql",
		"007_v0_6_0.up.sql", "008_v0_7_0.up.sql", "009_v0_8_0.up.sql",
		"010_v0_10_0.up.sql", "011_v0_14_0.up.sql", "012_v0_15_0.up.sql",
		"013_v0_16_0.up.sql", "014_v0_17_0.up.sql", "015_v0_18_0.up.sql",
		"016_v0_19_0.up.sql", "017_v0_21_0.up.sql", "018_v0_24_0.up.sql",
		"019_v0_25_0.up.sql", "020_v0_26_0.up.sql",
	}
}

// TestMigration021CarriesLabelDataOver is the test that matters for migration
// 021 (spec 2026-09-10-01): every OTHER test in the repo runs against a
// freshly-migrated database, where the rename at the top of the migration
// matches zero rows and the delete right after it has nothing to eat. This one
// seeds a real pre-021 database first.
//
// Three cases, because each is a different way to get it wrong:
//
//	a. `solidping.io/managed` — /apply's own ownership label, which the new
//	   CHECK refuses. It must be RENAMED to solidping-managed and survive with
//	   its check_labels intact; losing it would silently drop an existing
//	   SQLite deployment's whole managed scope out of reconcile, and the very
//	   next apply would delete-by-absence nothing and adopt nothing.
//	b. A collision: `solidping-managed` already exists with the same
//	   (organization, value), so the rename must SKIP that row (the unique
//	   index would refuse it) and the leftover is dropped instead — which is
//	   correct only because the surviving row carries the identical meaning.
//	c. A genuinely non-conformant key, which is deleted and must not be
//	   insertable afterwards.
func TestMigration021CarriesLabelDataOver(t *testing.T) {
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

	// Two orgs, so the rename's per-organization collision test is exercised
	// rather than assumed: org-2 holds the collision, org-1 does not.
	for _, org := range []struct{ uid, slug string }{
		{"org-1", "acme-one"}, {"org-2", "acme-two"},
	} {
		_, err = database.ExecContext(ctx,
			`insert into organizations (uid, slug, name) values (?, ?, ?)`,
			org.uid, org.slug, "Acme "+org.slug)
		r.NoError(err)
	}

	_, err = database.ExecContext(ctx, `insert into checks (uid, organization_uid, slug, type, config)
		values ('chk-1','org-1','carried','http','{"url":"https://example.com"}'),
		       ('chk-2','org-2','collided','http','{"url":"https://example.com"}')`)
	r.NoError(err)

	// The pre-021 CHECK really does accept all of these — which is the
	// divergence 021 closes, and the reason this seeding works at all.
	_, err = database.ExecContext(ctx, `insert into labels (uid, organization_uid, key, value) values
		('lbl-managed-1','org-1','solidping.io/managed','acme-one'),
		('lbl-managed-2','org-2','solidping.io/managed','acme-two'),
		('lbl-managed-new-2','org-2','solidping-managed','acme-two'),
		('lbl-managed-other','org-2','solidping.io/managed','other-manifest'),
		('lbl-bad','org-1','os','linux'),
		('lbl-good','org-1','environment','prod')`)
	r.NoError(err, "the pre-021 schema must accept these, or the fixture proves nothing")

	// Attach the labels to checks, so the cascade side of the migration is
	// exercised too: a dropped label must take its check_labels with it, and a
	// renamed one must keep them.
	_, err = database.ExecContext(ctx, `insert into check_labels (uid, check_uid, label_uid) values
		('cl-1','chk-1','lbl-managed-1'),
		('cl-2','chk-2','lbl-managed-2'),
		('cl-3','chk-1','lbl-bad'),
		('cl-4','chk-1','lbl-good')`)
	r.NoError(err)

	execMigrationFile(ctx, t, database, "021_v0_28_0.up.sql")

	// (a) org-1's managed label was renamed in place — same uid, same value,
	// still attached to its check.
	var key, value string
	r.NoError(database.QueryRowContext(ctx,
		`select key, value from labels where uid = 'lbl-managed-1'`).Scan(&key, &value))
	r.Equal("solidping-managed", key, "the managed label must be renamed, not dropped")
	r.Equal("acme-one", value)

	var attached int
	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from check_labels where label_uid = 'lbl-managed-1'`).Scan(&attached))
	r.Equal(1, attached, "a renamed label keeps its check_labels")

	// (b) org-2's legacy row collided with an existing solidping-managed row
	// of the same value, so it was skipped by the rename and then deleted —
	// and the pre-existing row (which means exactly the same thing) survives.
	var surviving int
	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from labels where uid = 'lbl-managed-2'`).Scan(&surviving))
	r.Zero(surviving, "the colliding legacy row must not survive the rebuild")

	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from check_labels where label_uid = 'lbl-managed-2'`).Scan(&attached))
	r.Zero(attached, "its check_labels go with it, by cascade")

	r.NoError(database.QueryRowContext(ctx,
		`select key, value from labels where uid = 'lbl-managed-new-2'`).Scan(&key, &value))
	r.Equal("solidping-managed", key)
	r.Equal("acme-two", value)

	// A legacy row in the SAME org whose value does NOT collide is renamed,
	// not dropped — the skip must be as narrow as the unique index is.
	r.NoError(database.QueryRowContext(ctx,
		`select key, value from labels where uid = 'lbl-managed-other'`).Scan(&key, &value))
	r.Equal("solidping-managed", key, "a non-colliding legacy row in the same org is still renamed")
	r.Equal("other-manifest", value)

	// (c) the genuinely non-conformant key is gone, with its check_labels.
	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from labels where uid = 'lbl-bad'`).Scan(&surviving))
	r.Zero(surviving)

	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from check_labels where label_uid = 'lbl-bad'`).Scan(&attached))
	r.Zero(attached)

	// The conformant label is untouched — a migration that deleted everything
	// would satisfy every assertion above.
	r.NoError(database.QueryRowContext(ctx,
		`select key, value from labels where uid = 'lbl-good'`).Scan(&key, &value))
	r.Equal("environment", key)
	r.Equal("prod", value)

	r.NoError(database.QueryRowContext(ctx,
		`select count(*) from check_labels where label_uid = 'lbl-good'`).Scan(&attached))
	r.Equal(1, attached)

	// Exactly the four expected rows remain, so nothing was invented either.
	remaining := queryStrings(ctx, t, database, `select uid from labels order by uid`)
	sort.Strings(remaining)
	r.Equal([]string{"lbl-good", "lbl-managed-1", "lbl-managed-new-2", "lbl-managed-other"}, remaining)

	// The new CHECK is live: what the migration deleted cannot come back.
	for _, badKey := range []string{"os", "1abc", "k8s.cluster", "solidping.io/managed"} {
		_, err = database.ExecContext(ctx,
			`insert into labels (uid, organization_uid, key, value) values (?, 'org-1', ?, 'v')`,
			"post-"+badKey, badKey)
		r.Error(err, "key %q must be refused after 021", badKey)
	}

	// Positive control on that: a conformant key still inserts, so the CHECK
	// is a rule and not a wall.
	_, err = database.ExecContext(ctx,
		`insert into labels (uid, organization_uid, key, value) values ('post-ok','org-1','team','platform')`)
	r.NoError(err)

	// Foreign keys are back on and nothing dangles.
	var fkEnabled int
	r.NoError(database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fkEnabled))
	r.Equal(1, fkEnabled, "foreign_keys must be re-enabled at the end of the migration")

	r.Empty(queryStrings(ctx, t, database, "select \"table\" from pragma_foreign_key_check"),
		"no dangling foreign keys after the rebuild")
}

// queryStrings runs a single-column query and returns every row as a string.
func queryStrings(ctx context.Context, t *testing.T, database *sql.DB, query string) []string {
	t.Helper()
	r := require.New(t)

	rows, err := database.QueryContext(ctx, query)
	r.NoError(err)

	defer func() { r.NoError(rows.Close()) }()

	var out []string

	for rows.Next() {
		var value string
		r.NoError(rows.Scan(&value))
		out = append(out, value)
	}

	r.NoError(rows.Err())

	return out
}
