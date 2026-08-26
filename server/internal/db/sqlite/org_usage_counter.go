package sqlite

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
	counter := &models.OrgUsageCounter{
		OrganizationUID: orgUID, Kind: kind, PeriodStart: periodStart, Count: 1,
	}

	// SQLite's ON CONFLICT DO UPDATE references the existing row via bare column
	// names (table-qualified names are rejected), unlike PostgreSQL.
	res, err := s.db.NewInsert().
		Model(counter).
		On("CONFLICT (organization_uid, kind, period_start) DO UPDATE").
		Set("count = count + 1").
		Where("count < ?", limit).
		Exec(ctx)
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
	counter := &models.OrgUsageCounter{
		OrganizationUID: orgUID, Kind: kind, PeriodStart: periodStart, Count: 1,
	}

	// SQLite's ON CONFLICT DO UPDATE references the existing row via bare
	// column names (table-qualified names are rejected), unlike PostgreSQL.
	if _, err := s.db.NewInsert().
		Model(counter).
		On("CONFLICT (organization_uid, kind, period_start) DO UPDATE").
		Set("count = count + 1").
		Exec(ctx); err != nil {
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
