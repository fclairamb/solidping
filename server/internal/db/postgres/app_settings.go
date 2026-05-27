package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GetAppSetting returns the value for the given key.
// Returns sql.ErrNoRows if the key does not exist.
func (s *Service) GetAppSetting(ctx context.Context, key string) (string, error) {
	var setting models.AppSetting

	err := s.db.NewSelect().
		Model(&setting).
		Where("key = ?", key).
		Limit(1).
		Scan(ctx)
	if err != nil {
		return "", fmt.Errorf("get app setting %q: %w", key, err)
	}

	return setting.Value, nil
}

// SetAppSetting creates or updates a key/value pair (upsert).
func (s *Service) SetAppSetting(ctx context.Context, key, value string) error {
	setting := &models.AppSetting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now(),
	}

	_, err := s.db.NewInsert().
		Model(setting).
		On("CONFLICT (key) DO UPDATE").
		Set("value = EXCLUDED.value").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("set app setting %q: %w", key, err)
	}

	return nil
}
