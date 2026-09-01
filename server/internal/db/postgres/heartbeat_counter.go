package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TryAdvanceHeartbeatCounter atomically stores counter as the check's last
// accepted SP2 replay counter, but ONLY when it is strictly greater than the
// stored value (or no value is stored yet). Returns true when the beat may be
// accepted.
//
// The strictly-greater test lives in the WHERE clause of the upsert rather
// than in Go on purpose: a read-then-write would let two concurrent beats — a
// device retrying the same datagram is the normal case, not the exotic one —
// both observe the old value and both be accepted, which is precisely the
// replay this counter exists to prevent.
func (s *Service) TryAdvanceHeartbeatCounter(
	ctx context.Context, checkUID string, counter int64,
) (bool, error) {
	// Raw SQL (no bun table alias) so DO UPDATE can reference the existing row
	// as heartbeat_counters.last_counter unambiguously — a bare column name is
	// ambiguous on Postgres (existing vs EXCLUDED), and bun's builder aliases
	// the INSERT target.
	const query = `INSERT INTO heartbeat_counters (check_uid, last_counter, updated_at)
VALUES (?, ?, now())
ON CONFLICT (check_uid)
DO UPDATE SET last_counter = excluded.last_counter, updated_at = excluded.updated_at
WHERE heartbeat_counters.last_counter < excluded.last_counter`

	res, err := s.db.NewRaw(query, checkUID, counter).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("advance heartbeat counter: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// GetHeartbeatCounter returns the last accepted SP2 counter for a check, and
// false when the check has never accepted a signed beat.
func (s *Service) GetHeartbeatCounter(ctx context.Context, checkUID string) (int64, bool, error) {
	var row models.HeartbeatCounter

	err := s.db.NewSelect().
		Model(&row).
		Where("check_uid = ?", checkUID).
		Limit(1).
		Scan(ctx)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("get heartbeat counter: %w", err)
	}

	return row.LastCounter, true, nil
}
