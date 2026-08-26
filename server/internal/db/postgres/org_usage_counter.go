package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ReserveMonthlyUsage atomically claims one unit of the monthly counter when
// the current count is below limit, via a conditional upsert. Returns true when
// a unit was reserved.
func (s *Service) ReserveMonthlyUsage(
	ctx context.Context, orgUID, kind, periodStart string, limit int,
) (bool, error) {
	// Raw SQL (no bun table alias) so the DO UPDATE can reference the existing
	// row as org_usage_counters.count unambiguously — a bare `count` is
	// ambiguous on Postgres (existing vs EXCLUDED), and bun's builder aliases
	// the INSERT target, which breaks a real-name reference.
	const query = `INSERT INTO org_usage_counters (organization_uid, kind, period_start, count)
VALUES (?, ?, ?, 1)
ON CONFLICT (organization_uid, kind, period_start)
DO UPDATE SET count = org_usage_counters.count + 1
WHERE org_usage_counters.count < ?`

	res, err := s.db.NewRaw(query, orgUID, kind, periodStart, limit).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("reserve monthly usage: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// IncrementUsageCounter adds one to the (orgUID, kind, periodStart) counter,
// creating the row when absent. Unconditional by design: it records an event
// that already happened, so unlike ReserveMonthlyUsage there is no cap to lose
// the race against.
func (s *Service) IncrementUsageCounter(
	ctx context.Context, orgUID, kind, periodStart string,
) error {
	// Raw SQL for the same reason as ReserveMonthlyUsage: a bare `count` in the
	// DO UPDATE is ambiguous on Postgres, and bun aliases the INSERT target.
	const query = `INSERT INTO org_usage_counters (organization_uid, kind, period_start, count)
VALUES (?, ?, ?, 1)
ON CONFLICT (organization_uid, kind, period_start)
DO UPDATE SET count = org_usage_counters.count + 1`

	if _, err := s.db.NewRaw(query, orgUID, kind, periodStart).Exec(ctx); err != nil {
		return fmt.Errorf("increment usage counter: %w", err)
	}

	return nil
}

// GetMonthlyUsage returns the current counter value, or 0 when no row exists.
func (s *Service) GetMonthlyUsage(
	ctx context.Context, orgUID, kind, periodStart string,
) (int, error) {
	var counter models.OrgUsageCounter

	err := s.db.NewSelect().
		Model(&counter).
		Where("organization_uid = ?", orgUID).
		Where("kind = ?", kind).
		Where("period_start = ?", periodStart).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get monthly usage: %w", err)
	}

	return counter.Count, nil
}
