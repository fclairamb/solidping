package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TLS asset storage (spec 2026-07-26-01) — the database side of the
// certmagic.Storage implementation in internal/tlsedge. Kept in step with the
// PostgreSQL twin (internal/db/postgres/tls_storage.go); the conditional upsert
// is portable across both engines.
//
// Prefix queries here use a half-open key RANGE (key >= prefix AND key <
// prefix⁺) rather than the LIKE 'prefix%' the PostgreSQL twin uses. This is a
// deliberate divergence, not drift: SQLite compares TEXT with the byte-exact
// BINARY collation, so the range is exactly right (and index-friendly), whereas
// SQLite's LIKE is ASCII-case-insensitive by default and would conflate keys
// differing only in case — certmagic embeds ACME account e-mails in keys. On
// Postgres the reverse holds: < / >= there honor the database collation, and
// every common non-C collation primary-ignores '/', which makes the range match
// zero rows (see the twin's header).
//
// SECURITY: these rows hold ACME account keys and certificate PRIVATE KEYS.
// Only internal/tlsedge may call these methods; never expose them via an API,
// export, or debug surface.

// tlsStoragePrefixEnd is the TLS-asset spelling of prefixRangeEnd.
func tlsStoragePrefixEnd(prefix string) (string, bool) {
	return prefixRangeEnd(prefix)
}

// prefixRangeEnd returns the exclusive upper bound of the key range that
// starts with prefix, or ok=false when no bound exists (an empty prefix, which
// means "every key").
//
// Shared by the TLS-asset key queries below and the attachment-topic prefix
// reaper in sqlite.go — both want SQLite's byte-exact BINARY collation rather
// than its ASCII-case-insensitive LIKE.
func prefixRangeEnd(prefix string) (string, bool) {
	if prefix == "" {
		return "", false
	}

	buf := []byte(prefix)
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] < 0xFF {
			buf[i]++

			return string(buf[:i+1]), true
		}
	}

	return "", false
}

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
	query := s.db.NewDelete().Model((*models.TLSStorageEntry)(nil)).Where("key = ?", key)

	if end, ok := tlsStoragePrefixEnd(key + "/"); ok {
		query = query.WhereOr("key >= ? AND key < ?", key+"/", end)
	}

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
		if end, ok := tlsStoragePrefixEnd(prefix + "/"); ok {
			query = query.Where("key = ? OR (key >= ? AND key < ?)", prefix, prefix+"/", end)
		} else {
			query = query.Where("key = ?", prefix)
		}
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
