package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TLS asset storage (spec 2026-07-26-01) — the database side of the
// certmagic.Storage implementation in internal/tlsedge.
//
// Prefix queries use LIKE 'prefix%' ESCAPE '\', NOT a half-open key range
// (key >= prefix AND key < prefix⁺). Postgres' < / >= on text honor the
// database COLLATION, and every common non-C collation (en_US.utf8 — the
// default of the official postgres image and of most distro installs) treats
// '/' as primary-ignorable: the range's upper bound 'certificates0' then sorts
// BELOW real keys such as 'certificates/ca/example.com', so the range matches
// ZERO rows and List/Delete silently do nothing. LIKE compares
// character-by-character regardless of collation, and it is what the
// text_pattern_ops index in migration 008_v0_7_0 was created to serve.
//
// The SQLite twin (internal/db/sqlite/tls_storage.go) deliberately keeps the
// key range: SQLite compares TEXT with the byte-exact BINARY collation, and its
// LIKE is ASCII-case-insensitive by default — which would wrongly conflate keys
// differing only in case (certmagic embeds ACME account e-mails in keys).
//
// SECURITY: these rows hold ACME account keys and certificate PRIVATE KEYS.
// Only internal/tlsedge may call these methods; never expose them via an API,
// export, or debug surface.

// likePrefixPattern returns the LIKE pattern matching every value that starts
// with prefix. LIKE metacharacters the prefix itself may contain (% _ \ — a
// CA-derived issuer path could grow one) are escaped with a backslash, so the
// pattern must always be used with ESCAPE '\'.
//
// Shared by the TLS-asset key queries below and the attachment-topic prefix
// reaper in postgres.go: escaping a prefix before it becomes a pattern is the
// kind of thing that must exist exactly once.
func likePrefixPattern(prefix string) string {
	escaper := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

	return escaper.Replace(prefix) + "%"
}

// tlsStoragePrefixLike is the TLS-asset spelling of likePrefixPattern.
func tlsStoragePrefixLike(prefix string) string {
	return likePrefixPattern(prefix)
}

// tlsStorageKeyLikeExpr is the WHERE fragment every prefix query uses; its
// single placeholder takes a tlsStoragePrefixLike pattern. Postgres runs with
// standard_conforming_strings on, so '\' here is one literal backslash.
const tlsStorageKeyLikeExpr = `key LIKE ? ESCAPE '\'`

// TLSStorageStore upserts an asset, refreshing its modification time.
func (s *Service) TLSStorageStore(ctx context.Context, key string, value []byte) error {
	entry := &models.TLSStorageEntry{Key: key, Value: value, ModifiedAt: time.Now()}

	_, err := s.db.NewInsert().
		Model(entry).
		On("CONFLICT (key) DO UPDATE").
		Set("value = EXCLUDED.value").
		Set("modified_at = EXCLUDED.modified_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("tls storage store %q: %w", key, err)
	}

	return nil
}

// TLSStorageLoad returns the stored bytes for key, or sql.ErrNoRows (wrapped)
// when the key does not exist.
func (s *Service) TLSStorageLoad(ctx context.Context, key string) ([]byte, error) {
	var entry models.TLSStorageEntry

	err := s.db.NewSelect().
		Model(&entry).
		Where("key = ?", key).
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("tls storage load %q: %w", key, err)
	}

	return entry.Value, nil
}

// TLSStorageDelete removes the key and, since certmagic keys are path-like,
// every key nested under it ("<key>/..."). Deleting a missing key is not an
// error — certmagic only requires that the key be gone afterwards.
func (s *Service) TLSStorageDelete(ctx context.Context, key string) error {
	query := s.db.NewDelete().
		Model((*models.TLSStorageEntry)(nil)).
		Where("key = ?", key).
		WhereOr(tlsStorageKeyLikeExpr, tlsStoragePrefixLike(key+"/"))

	if _, err := query.Exec(ctx); err != nil {
		return fmt.Errorf("tls storage delete %q: %w", key, err)
	}

	return nil
}

// TLSStorageExists reports whether the key exists as a stored value.
func (s *Service) TLSStorageExists(ctx context.Context, key string) (bool, error) {
	exists, err := s.db.NewSelect().
		Model((*models.TLSStorageEntry)(nil)).
		Where("key = ?", key).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("tls storage exists %q: %w", key, err)
	}

	return exists, nil
}

// TLSStorageList returns metadata (never the value bytes) for the key itself
// and every key nested under it, sorted by key. An empty prefix lists
// everything.
func (s *Service) TLSStorageList(ctx context.Context, prefix string) ([]models.TLSStorageKeyInfo, error) {
	var rows []tlsStorageKeyRow

	// No Model(): this is a deliberate value-free projection, and binding the
	// anonymous row struct as a model would make bun invent a table name.
	query := s.db.NewSelect().
		TableExpr("tls_storage").
		Column("key", "modified_at").
		ColumnExpr("length(value) AS value_len").
		Order("key")

	if prefix != "" {
		query = query.
			Where("key = ?", prefix).
			WhereOr(tlsStorageKeyLikeExpr, tlsStoragePrefixLike(prefix+"/"))
	}

	if err := query.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("tls storage list %q: %w", prefix, err)
	}

	out := make([]models.TLSStorageKeyInfo, len(rows))
	for i := range rows {
		out[i] = models.TLSStorageKeyInfo{
			Key: rows[i].Key, Size: rows[i].ValueLen, ModifiedAt: rows[i].ModifiedAt,
		}
	}

	return out, nil
}

// tlsStorageKeyRow is the value-free projection List/Stat scan into, so a
// listing never pulls certificate private keys into memory.
type tlsStorageKeyRow struct {
	Key        string    `bun:"key"`
	ModifiedAt time.Time `bun:"modified_at"`
	ValueLen   int64     `bun:"value_len"`
}

// TLSStorageStat returns metadata for one key, or sql.ErrNoRows (wrapped) when
// it does not exist.
func (s *Service) TLSStorageStat(ctx context.Context, key string) (models.TLSStorageKeyInfo, error) {
	var row tlsStorageKeyRow

	err := s.db.NewSelect().
		TableExpr("tls_storage").
		Column("key", "modified_at").
		ColumnExpr("length(value) AS value_len").
		Where("key = ?", key).
		Limit(1).
		Scan(ctx, &row)
	if err != nil {
		return models.TLSStorageKeyInfo{}, fmt.Errorf("tls storage stat %q: %w", key, err)
	}

	return models.TLSStorageKeyInfo{Key: row.Key, Size: row.ValueLen, ModifiedAt: row.ModifiedAt}, nil
}

// TLSStorageAcquireLock atomically claims the named lock for owner until
// expiresAt. It succeeds when the lock is free OR the current holder's lease
// has expired (a crashed issuance must not wedge renewals forever); it returns
// false — not an error — when a live holder still owns it.
func (s *Service) TLSStorageAcquireLock(
	ctx context.Context, key, owner string, expiresAt time.Time,
) (bool, error) {
	// Raw SQL so the DO UPDATE ... WHERE can name the existing row
	// unambiguously (bun's builder aliases the INSERT target).
	const query = `INSERT INTO tls_storage_locks (key, owner, expires_at)
VALUES (?, ?, ?)
ON CONFLICT (key)
DO UPDATE SET owner = EXCLUDED.owner, expires_at = EXCLUDED.expires_at
WHERE tls_storage_locks.expires_at < ?`

	res, err := s.db.NewRaw(query, key, owner, expiresAt, time.Now()).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("tls storage acquire lock %q: %w", key, err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// TLSStorageRefreshLock extends the lease of a lock this owner still holds.
// Returns false when the lock was lost (taken over or released), so the caller
// can stop refreshing.
func (s *Service) TLSStorageRefreshLock(
	ctx context.Context, key, owner string, expiresAt time.Time,
) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*models.TLSStorageLock)(nil)).
		Set("expires_at = ?", expiresAt).
		Where("key = ?", key).
		Where("owner = ?", owner).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("tls storage refresh lock %q: %w", key, err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// TLSStorageReleaseLock drops a lock this owner holds. Releasing a lock that is
// already gone (or was taken over) is a no-op, never an error.
func (s *Service) TLSStorageReleaseLock(ctx context.Context, key, owner string) error {
	_, err := s.db.NewDelete().
		Model((*models.TLSStorageLock)(nil)).
		Where("key = ?", key).
		Where("owner = ?", owner).
		Exec(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("tls storage release lock %q: %w", key, err)
	}

	return nil
}
