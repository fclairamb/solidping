package postgres

import (
	"context"
	"fmt"
)

// HeartbeatCounterKey builds the state_entries key holding a check's last
// accepted SP2 replay counter.
//
// Slash-namespaced, per the documented convention for that table's `key`
// column (see the column comment in 001_v0_1_0.up.sql).
func HeartbeatCounterKey(checkUID string) string {
	return "heartbeat_counter/" + checkUID
}

// advanceHeartbeatCounterQuery is the whole replay guard, in one statement.
//
// The strictly-greater test lives in the WHERE clause of the upsert rather
// than in Go on purpose: a read-then-write would let two concurrent beats — a
// device retrying the same datagram is the normal case, not the exotic one —
// both observe the old value and both be accepted, which is precisely the
// replay this counter exists to prevent. That is also why this does NOT go
// through the generic SetStateEntry, which is an unconditional write.
//
// Details that are load-bearing rather than incidental:
//
//   - `expires_at` is left out of the INSERT (so NULL) and forced back to NULL
//     on every update. DeleteExpiredStateEntries only sweeps rows with a
//     non-null expires_at, so a counter can never be garbage-collected out
//     from under a live device.
//   - `deleted_at` is cleared on a winning advance. The unique constraint
//     carries no deleted_at predicate, so a soft-deleted row still owns the
//     slot and still gates this comparison — which is the fail-safe direction
//     (an old counter can never be replayed by deleting the entry first). A
//     row that just accepted a beat is live state, so it is un-deleted rather
//     than left in a state the read path would have to lie about.
//   - COALESCE(..., -1) keeps a row whose value lost its lastCounter from
//     wedging the check forever: without it the comparison is NULL, the update
//     never fires, and every future beat is rejected. -1 rather than 0 because
//     a device's first counter may legitimately be 0.
//   - The conflict target matches on organization_uid, and Postgres treats
//     NULLs as distinct there. That is fine because a check always belongs to
//     an org, so this key is never written with a NULL org.
const advanceHeartbeatCounterQuery = `
INSERT INTO state_entries
  (uid, organization_uid, key, value, created_at, updated_at)
VALUES (gen_random_uuid(), ?, ?, jsonb_build_object('lastCounter', ?), now(), now())
ON CONFLICT (organization_uid, key)
DO UPDATE SET value = excluded.value,
              expires_at = NULL,
              deleted_at = NULL,
              updated_at = excluded.updated_at
WHERE COALESCE((state_entries.value->>'lastCounter')::bigint, -1)
    < (excluded.value->>'lastCounter')::bigint`

// TryAdvanceHeartbeatCounter atomically stores counter as the check's last
// accepted SP2 replay counter, but ONLY when it is strictly greater than the
// stored value (or no value is stored yet). Returns true when the beat may be
// accepted.
//
// See advanceHeartbeatCounterQuery for why this is one statement.
func (s *Service) TryAdvanceHeartbeatCounter(
	ctx context.Context, orgUID, checkUID string, counter int64,
) (bool, error) {
	res, err := s.db.NewRaw(advanceHeartbeatCounterQuery, orgUID, HeartbeatCounterKey(checkUID), counter).Exec(ctx)
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
	const query = `SELECT (value->>'lastCounter')::bigint
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
