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

// GetAppSettings reads several keys in one statement. Missing keys are absent
// from the returned map.
func (s *Service) GetAppSettings(ctx context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return values, nil
	}

	var settings []models.AppSetting

	if err := s.db.NewSelect().
		Model(&settings).
		Where("key IN (?)", bun.List(keys)).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("get app settings: %w", err)
	}

	for i := range settings {
		values[settings[i].Key] = settings[i].Value
	}

	return values, nil
}

// CreateAppSettingsIfAbsent inserts every pair in one statement and keeps the
// keys that already exist.
func (s *Service) CreateAppSettingsIfAbsent(ctx context.Context, settings map[string]string) error {
	rows := models.AppSettingRows(settings, time.Now())
	if len(rows) == 0 {
		return nil
	}

	if _, err := s.db.NewInsert().
		Model(&rows).
		On("CONFLICT (key) DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("create app settings: %w", err)
	}

	return nil
}

// SetAppSettings upserts every pair in one statement.
func (s *Service) SetAppSettings(ctx context.Context, settings map[string]string) error {
	rows := models.AppSettingRows(settings, time.Now())
	if len(rows) == 0 {
		return nil
	}

	if _, err := s.db.NewInsert().
		Model(&rows).
		On("CONFLICT (key) DO UPDATE").
		Set("value = EXCLUDED.value").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx); err != nil {
		return fmt.Errorf("set app settings: %w", err)
	}

	return nil
}
