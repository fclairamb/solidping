package models

import (
	"slices"
	"time"
)

// AppSetting is a generic key/value store for server-level configuration
// that must survive restarts (e.g., VAPID keys for Web Push).
type AppSetting struct {
	Key       string    `bun:"key,pk,type:text"`
	Value     string    `bun:"value,notnull,type:text"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// AppSettingRows turns a key/value map into rows sorted by key. The stable
// order matters for multi-row writes: two Postgres transactions inserting the
// same keys always lock them in the same order, so they never deadlock.
func AppSettingRows(settings map[string]string, updatedAt time.Time) []AppSetting {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	rows := make([]AppSetting, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, AppSetting{Key: key, Value: settings[key], UpdatedAt: updatedAt})
	}

	return rows
}
