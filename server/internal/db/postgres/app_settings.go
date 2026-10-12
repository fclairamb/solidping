package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

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

// CreateAppSettingsIfAbsent inserts every pair in one transaction, leaving
// existing keys untouched (ON CONFLICT DO NOTHING).
func (s *Service) CreateAppSettingsIfAbsent(ctx context.Context, settings map[string]string) error {
	return s.writeAppSettings(ctx, settings, false)
}

// SetAppSettings upserts every pair in one transaction.
func (s *Service) SetAppSettings(ctx context.Context, settings map[string]string) error {
	return s.writeAppSettings(ctx, settings, true)
}

func (s *Service) writeAppSettings(ctx context.Context, settings map[string]string, overwrite bool) error {
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for key, value := range settings {
			setting := &models.AppSetting{Key: key, Value: value, UpdatedAt: time.Now()}

			query := tx.NewInsert().Model(setting)
			if overwrite {
				query = query.On("CONFLICT (key) DO UPDATE").
					Set("value = EXCLUDED.value").
					Set("updated_at = EXCLUDED.updated_at")
			} else {
				query = query.On("CONFLICT (key) DO NOTHING")
			}

			if _, err := query.Exec(ctx); err != nil {
				return fmt.Errorf("write app setting %q: %w", key, err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("write app settings: %w", err)
	}

	return nil
}
