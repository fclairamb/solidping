package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errUserTokensMissingHashColumn is returned when user_tokens still has the
// plaintext column but not token_hash: the database applied an earlier draft
// of 024_v0_33_0 that predates the hash-user-tokens section. bun never re-runs
// an applied migration, so the only fix is the reset the migration header
// already asks for.
var errUserTokensMissingHashColumn = errors.New(
	"user_tokens has no token_hash column: this database applied an earlier draft of " +
		"migration 024_v0_33_0; reset it (see the migration header)")

// HashPlaintextUserTokens is the data half of the hash-user-tokens section of
// 024_v0_33_0 (spec 2026-09-25-23). The SQL section adds user_tokens.token_hash;
// this hashes every row whose token_hash is still NULL from its plaintext
// `token` (models.HashUserToken), then drops `token`, in one transaction. It
// returns how many rows it hashed.
//
// Why Go and not SQL: SQLite has no sha256(), and doing it here keeps one
// hashing code path for both engines, byte-for-byte the one the lookups use.
//
// Why on every boot rather than inside the migration: bun records a migration
// as applied BEFORE running it, so a process killed between the SQL and this
// step would never come back to it. Running it after every Migrate makes it
// self-healing instead, and it costs one catalog read once the `token` column
// is gone. It is idempotent and never guesses: a row is hashed exactly when
// token_hash is set, and the plaintext column only disappears in the same
// transaction as the last hash, so a 64-char hex token can never be re-hashed.
//
// It must run before the server serves a request (the model no longer maps
// `token`, whose NOT NULL would refuse every insert): both engines' Initialize
// call it right after the migrator.
func HashPlaintextUserTokens(ctx context.Context, bunDB *bun.DB) (int, error) {
	hasPlaintext, err := userTokensHasColumn(ctx, bunDB, "token")
	if err != nil {
		return 0, err
	}

	if !hasPlaintext {
		return 0, nil
	}

	hasHash, err := userTokensHasColumn(ctx, bunDB, "token_hash")
	if err != nil {
		return 0, err
	}

	if !hasHash {
		return 0, errUserTokensMissingHashColumn
	}

	hashed := 0

	err = bunDB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		pending, readErr := plaintextUserTokens(ctx, tx)
		if readErr != nil {
			return readErr
		}

		for _, row := range pending {
			if _, execErr := tx.ExecContext(ctx,
				"UPDATE user_tokens SET token_hash = ? WHERE uid = ? AND token_hash IS NULL",
				models.HashUserToken(row.token), row.uid,
			); execErr != nil {
				return fmt.Errorf("failed to hash user token %s: %w", row.uid, execErr)
			}
		}

		// Same statement on both engines (SQLite >= 3.35). The SQL section
		// already dropped the unique index on `token`, which SQLite requires.
		if _, execErr := tx.ExecContext(ctx, "ALTER TABLE user_tokens DROP COLUMN token"); execErr != nil {
			return fmt.Errorf("failed to drop user_tokens.token: %w", execErr)
		}

		hashed = len(pending)

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to hash plaintext user tokens: %w", err)
	}

	slog.InfoContext(ctx, "Hashed plaintext user tokens at rest", "rows", hashed)

	return hashed, nil
}

type plaintextUserToken struct {
	uid   string
	token string
}

// plaintextUserTokens reads every row still waiting for its hash, soft-deleted
// ones included (their plaintext must go too). The rows are fully read before
// any update: SQLite may run the whole transaction on a single connection.
func plaintextUserTokens(ctx context.Context, tx bun.Tx) ([]plaintextUserToken, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT uid, token FROM user_tokens WHERE token_hash IS NULL AND token IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("failed to list plaintext user tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var pending []plaintextUserToken

	for rows.Next() {
		var row plaintextUserToken
		if scanErr := rows.Scan(&row.uid, &row.token); scanErr != nil {
			return nil, fmt.Errorf("failed to read plaintext user token: %w", scanErr)
		}

		pending = append(pending, row)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("failed to list plaintext user tokens: %w", rowsErr)
	}

	return pending, nil
}

// userTokensHasColumn reports whether user_tokens has the named column.
func userTokensHasColumn(ctx context.Context, bunDB *bun.DB, column string) (bool, error) {
	var query string

	switch bunDB.Dialect().Name() {
	case dialect.PG:
		query = `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'user_tokens' AND column_name = ?`
	case dialect.SQLite:
		query = `SELECT count(*) FROM pragma_table_info('user_tokens') WHERE name = ?`
	default:
		return false, fmt.Errorf("%w: %s", errUnsupportedDialect, bunDB.Dialect().Name())
	}

	var count int
	if err := bunDB.QueryRowContext(ctx, query, column).Scan(&count); err != nil {
		return false, fmt.Errorf("failed to inspect user_tokens columns: %w", err)
	}

	return count > 0, nil
}

var errUnsupportedDialect = errors.New("unsupported database dialect")
