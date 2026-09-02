package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// HeartbeatCounterKey builds the state_entries key holding a check's last
// accepted SP2 replay counter.
//
// Slash-namespaced, per the documented convention for that table's `key`
// column (see the column comment in 001_v0_1_0.up.sql).
func HeartbeatCounterKey(checkUID string) string {
	return "heartbeat_counter/" + checkUID
}

// advanceHeartbeatCounterQuery is the SQLite half of the PostgreSQL statement
// of the same name — see postgres/heartbeat_counter.go for the full rationale.
// In short: the strictly-greater test stays in the WHERE clause of a single
// conditional upsert, because a read-then-write in Go would let two concurrent
// beats both observe the old value and both be accepted, which is exactly the
// replay this counter prevents.
//
// The two dialect differences: SQLite's ON CONFLICT DO UPDATE references the
// existing row through BARE column names (table-qualified names are rejected,
// unlike Postgres), and `uid` has no server-side default so it is generated in
// Go.
const advanceHeartbeatCounterQuery = `
INSERT INTO state_entries
  (uid, organization_uid, key, value, expires_at, created_at, updated_at)
VALUES (?, ?, ?, json_object('lastCounter', ?), ?, current_timestamp, current_timestamp)
ON CONFLICT (organization_uid, key)
DO UPDATE SET value = excluded.value,
              expires_at = excluded.expires_at,
              deleted_at = NULL,
              updated_at = current_timestamp
WHERE COALESCE(CAST(json_extract(value, '$.lastCounter') AS INTEGER), -1)
    < CAST(json_extract(excluded.value, '$.lastCounter') AS INTEGER)`

// TryAdvanceHeartbeatCounter atomically stores counter as the check's last
// accepted SP2 replay counter, but ONLY when it is strictly greater than the
// stored value (or no value is stored yet). Returns true when the beat may be
// accepted.
//
// See advanceHeartbeatCounterQuery for why this is one statement.
func (s *Service) TryAdvanceHeartbeatCounter(
	ctx context.Context, orgUID, checkUID string, counter int64,
) (bool, error) {
	expiresAt := time.Now().Add(models.HeartbeatCounterTTL)

	res, err := s.db.NewRaw(
		advanceHeartbeatCounterQuery,
		uuid.New().String(), orgUID, HeartbeatCounterKey(checkUID), counter, expiresAt,
	).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("advance heartbeat counter: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// GetHeartbeatCounter returns the last accepted SP2 counter for a check, and
// false when the check has never accepted a signed beat.
//
// It deliberately does NOT filter on deleted_at, because it reports what the
// guard above actually enforces: a soft-deleted row keeps gating the advance,
// so hiding it here would claim a counter of 0 for a check that is rejecting
// beats at counter 5. Nor does it filter on expires_at — a counter row never
// carries one.
func (s *Service) GetHeartbeatCounter(ctx context.Context, orgUID, checkUID string) (int64, bool, error) {
	const query = `SELECT CAST(json_extract(value, '$.lastCounter') AS INTEGER)
FROM state_entries
WHERE organization_uid = ? AND key = ?`

	var found []int64

	if err := s.db.NewRaw(query, orgUID, HeartbeatCounterKey(checkUID)).Scan(ctx, &found); err != nil {
		return 0, false, fmt.Errorf("get heartbeat counter: %w", err)
	}

	if len(found) == 0 {
		return 0, false, nil
	}

	return found[0], true, nil
}
