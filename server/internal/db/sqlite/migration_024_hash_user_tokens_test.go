package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// seededUserToken is a user_tokens row seeded with its plaintext value, the
// way every v0.32 database stores it.
type seededUserToken struct {
	uid, value, kind string
	deleted          bool
}

func hashUserTokensSeed() []seededUserToken {
	return []seededUserToken{
		{uid: "tok-refresh", value: "c2Vzc2lvbi1yZWZyZXNoLXRva2VuLXZhbHVlLTMyYg", kind: "refresh"},
		{uid: "tok-pat", value: "pat_0123456789abcdef0123456789abcdef01234567", kind: "pat"},
		{uid: "tok-grant", value: "b2F1dGgtcmVmcmVzaC1ncmFudC12YWx1ZS0zMmI", kind: "oauth_refresh"},
		// A soft-deleted row keeps its plaintext too: it must be hashed, and it
		// must stay unresolvable.
		{uid: "tok-revoked", value: "cmV2b2tlZC1zZXNzaW9uLXRva2VuLXZhbHVlLTMyYg", kind: "refresh", deleted: true},
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))

	return hex.EncodeToString(sum[:])
}

// openPre024Database returns an in-memory database migrated to v0.32, with
// one org, one user and the plaintext tokens of hashUserTokensSeed.
func openPre024Database(ctx context.Context, t *testing.T) *sql.DB {
	t.Helper()
	r := require.New(t)

	database, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	for _, name := range migrationsBefore024() {
		execMigrationFile(ctx, t, database, name)
	}

	for _, stmt := range []string{
		`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`,
		`insert into users (uid, email) values ('user-1', 'alice@acme.com')`,
	} {
		_, err = database.ExecContext(ctx, stmt)
		r.NoError(err, stmt)
	}

	for _, tok := range hashUserTokensSeed() {
		var deletedAt any
		if tok.deleted {
			deletedAt = "2026-09-20 10:00:00"
		}

		_, err = database.ExecContext(ctx,
			`insert into user_tokens (uid, user_uid, organization_uid, token, type, deleted_at)
			 values (?, 'user-1', 'org-1', ?, ?, ?)`, tok.uid, tok.value, tok.kind, deletedAt)
		r.NoError(err)
	}

	return database
}

func userTokensColumnCount(ctx context.Context, t *testing.T, database *sql.DB, column string) int {
	t.Helper()

	var count int
	require.NoError(t, database.QueryRowContext(ctx,
		`select count(*) from pragma_table_info('user_tokens') where name = ?`, column,
	).Scan(&count))

	return count
}

func storedTokenHashes(ctx context.Context, t *testing.T, database *sql.DB) map[string]string {
	t.Helper()

	rows, err := database.QueryContext(ctx, `select uid, token_hash from user_tokens`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	hashes := map[string]string{}

	for rows.Next() {
		var uid string

		var hash sql.NullString

		require.NoError(t, rows.Scan(&uid, &hash))
		hashes[uid] = hash.String
	}

	require.NoError(t, rows.Err())

	return hashes
}

// TestMigration024HashesUserTokens proves the hash-user-tokens section of 024
// plus its Go data half (spec 2026-09-25-23) on a real v0.32 database holding
// plaintext tokens: every row ends up as the 64-char hex SHA-256 of its value,
// the plaintext column is gone, the original tokens still resolve, a revoked
// one still does not, and a second run changes nothing.
func TestMigration024HashesUserTokens(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database := openPre024Database(ctx, t)
	execMigrationFile(ctx, t, database, "024_v0_33_0.up.sql")

	r.Equal(1, userTokensColumnCount(ctx, t, database, "token"),
		"the SQL half keeps the plaintext column: only Go may drop it, after hashing")

	bunDB := bun.NewDB(database, sqlitedialect.New())

	hashed, err := db.HashPlaintextUserTokens(ctx, bunDB)
	r.NoError(err)
	r.Equal(len(hashUserTokensSeed()), hashed, "every row is hashed, soft-deleted ones included")

	r.Zero(userTokensColumnCount(ctx, t, database, "token"), "the plaintext column is dropped")

	hashes := storedTokenHashes(ctx, t, database)
	for _, tok := range hashUserTokensSeed() {
		r.Regexp(hexSHA256, hashes[tok.uid])
		r.Equal(sha256Hex(tok.value), hashes[tok.uid], "row %s holds sha256(token)", tok.uid)
		r.NotContains(hashes[tok.uid], tok.value)
	}

	svc := &Service{db: bunDB}

	for _, tok := range hashUserTokensSeed() {
		row, lookupErr := svc.GetUserTokenByToken(ctx, tok.value)
		if tok.deleted {
			r.ErrorIs(lookupErr, sql.ErrNoRows, "a revoked token stays revoked")

			continue
		}

		r.NoError(lookupErr, "the original %s token still authenticates", tok.kind)
		r.Equal(tok.uid, row.UID)
		r.Equal(sha256Hex(tok.value), row.TokenHash)
	}

	_, err = svc.GetUserTokenByToken(ctx, sha256Hex(hashUserTokensSeed()[0].value))
	r.ErrorIs(err, sql.ErrNoRows, "the stored hash is not itself a credential")

	again, err := db.HashPlaintextUserTokens(ctx, bunDB)
	r.NoError(err)
	r.Zero(again, "a second run has nothing to do")
	r.Equal(hashes, storedTokenHashes(ctx, t, database), "and never re-hashes a hashed row")
}

// TestMigration024HashNeverRehashesAMarkedRow pins the marker: a row whose
// token_hash is already set is left alone even though its plaintext column is
// still present, so a 64-char hex value is never hashed a second time.
func TestMigration024HashNeverRehashesAMarkedRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database := openPre024Database(ctx, t)
	execMigrationFile(ctx, t, database, "024_v0_33_0.up.sql")

	patValue := hashUserTokensSeed()[1].value
	_, err := database.ExecContext(ctx,
		`update user_tokens set token_hash = ?, token = ? where uid = 'tok-pat'`,
		sha256Hex(patValue), sha256Hex(patValue))
	r.NoError(err)

	hashed, err := db.HashPlaintextUserTokens(ctx, bun.NewDB(database, sqlitedialect.New()))
	r.NoError(err)
	r.Equal(len(hashUserTokensSeed())-1, hashed, "the marked row is skipped")
	r.Equal(sha256Hex(patValue), storedTokenHashes(ctx, t, database)["tok-pat"])
}

// TestHashPlaintextUserTokensRefusesAnEarlierDraft: a database that applied a
// 024 without this section has `token` but no token_hash. Booting on it must
// fail loudly, not serve logins that can never match.
func TestHashPlaintextUserTokensRefusesAnEarlierDraft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	database := openPre024Database(ctx, t)

	_, err := db.HashPlaintextUserTokens(ctx, bun.NewDB(database, sqlitedialect.New()))
	require.ErrorContains(t, err, "token_hash")
}

// TestMigration024HashUserTokensDown proves the down half from both states the
// up half can leave: right after the SQL (plaintext column still there) and
// after the Go step dropped it. Either way every row is deleted (the hashes
// cannot be reversed: a downgrade signs everyone out) and the v0.32 shape is
// back, plaintext column and all.
func TestMigration024HashUserTokensDown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		goStep bool
	}{
		{name: "after the SQL half only", goStep: false},
		{name: "after the Go step", goStep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := context.Background()

			database := openPre024Database(ctx, t)
			execMigrationFile(ctx, t, database, "024_v0_33_0.up.sql")

			if tc.goStep {
				_, err := db.HashPlaintextUserTokens(ctx, bun.NewDB(database, sqlitedialect.New()))
				r.NoError(err)
			}

			execMigrationFile(ctx, t, database, "024_v0_33_0.down.sql")

			r.Equal(1, userTokensColumnCount(ctx, t, database, "token"))
			r.Zero(userTokensColumnCount(ctx, t, database, "token_hash"))

			var rows int
			r.NoError(database.QueryRowContext(ctx, `select count(*) from user_tokens`).Scan(&rows))
			r.Zero(rows, "no plaintext can be restored, so no row survives")

			_, err := database.ExecContext(ctx,
				`insert into user_tokens (uid, user_uid, token, type) values ('t-new', 'user-1', 'v0-32', 'pat')`)
			r.NoError(err, "the v0.32 code path can write again")

			_, err = database.ExecContext(ctx,
				`insert into user_tokens (uid, user_uid, token, type) values ('t-dup', 'user-1', 'v0-32', 'pat')`)
			r.Error(err, "the unique index on token is back")
		})
	}
}
