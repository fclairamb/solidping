package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateAuthHandoffCode stores a freshly issued handoff code (spec 2026-09-25-12).
func (s *Service) CreateAuthHandoffCode(ctx context.Context, code *models.AuthHandoffCode) error {
	if _, err := s.db.NewInsert().Model(code).Exec(ctx); err != nil {
		return fmt.Errorf("create auth handoff code: %w", err)
	}

	return nil
}

// ConsumeAuthHandoffCode deletes the row keyed by codeHash and returns it, in a
// single DELETE ... RETURNING: of two concurrent exchanges of the same code,
// exactly one gets the row and the other gets sql.ErrNoRows. Expiry is left to
// the caller, which checks it on the row it now owns (an expired row is
// consumed all the same, which is what it deserves).
func (s *Service) ConsumeAuthHandoffCode(ctx context.Context, codeHash string) (*models.AuthHandoffCode, error) {
	row := new(models.AuthHandoffCode)

	err := s.db.NewDelete().Model(row).
		Where("code_hash = ?", codeHash).
		Returning("*").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("consume auth handoff code: %w", err)
	}

	return row, nil
}

// DeleteExpiredAuthHandoffCodes removes codes that expired before `before`.
func (s *Service) DeleteExpiredAuthHandoffCodes(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.NewDelete().Model((*models.AuthHandoffCode)(nil)).
		Where("expires_at <= ?", before).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete expired auth handoff codes: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired auth handoff codes rows: %w", err)
	}

	return rows, nil
}
