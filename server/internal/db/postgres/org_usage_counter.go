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
	counter := &models.OrgUsageCounter{
		OrganizationUID: orgUID, Kind: kind, PeriodStart: periodStart, Count: 1,
	}

	res, err := s.db.NewInsert().
		Model(counter).
		On("CONFLICT (organization_uid, kind, period_start) DO UPDATE").
		Set("count = org_usage_counters.count + 1").
		Where("org_usage_counters.count < ?", limit).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("reserve monthly usage: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
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
