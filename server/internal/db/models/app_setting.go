package models

import "time"

// AppSetting is a generic key/value store for server-level configuration
// that must survive restarts (e.g., VAPID keys for Web Push).
type AppSetting struct {
	Key       string    `bun:"key,pk,type:text"`
	Value     string    `bun:"value,notnull,type:text"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}
