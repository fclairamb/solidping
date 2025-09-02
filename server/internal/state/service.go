// Package state provides a higher-level state storage service with prefix support.
package state

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service provides state storage operations with optional key prefixing.
type Service interface {
	// WithPrefix returns a new Service that automatically prepends the given prefix to all keys.
	// Prefixes are cumulative (calling WithPrefix("a").WithPrefix("b") results in prefix "a:b:").
	WithPrefix(prefix string) Service

	// Get retrieves a state entry by key. Returns nil if not found.
	// orgUID can be nil for global entries.
	Get(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error)

	// Set creates or updates a state entry. TTL is optional (nil = never expires).
	// orgUID can be nil for global entries.
	Set(ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration) error

	// Delete soft-deletes a state entry.
	Delete(ctx context.Context, orgUID *string, key string) error

	// List returns all entries matching the key pattern within the current prefix scope.
	// Pattern uses SQL LIKE syntax (% for any characters, _ for single character).
	// Empty pattern returns all entries under the prefix.
	List(ctx context.Context, orgUID *string, pattern string) ([]*models.StateEntry, error)

	// GetOrCreate returns existing entry or creates new one.
	// Returns (entry, created, error) where created is true if a new entry was created.
	GetOrCreate(
		ctx context.Context, orgUID *string, key string, defaultValue *models.JSONMap, ttl *time.Duration,
	) (*models.StateEntry, bool, error)

	// SetIfNotExists creates entry only if key doesn't exist.
	// Returns (created, error) where created is true if entry was created.
	SetIfNotExists(
		ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
	) (bool, error)

	// DeleteExpired removes entries past their expires_at. Returns count deleted.
	DeleteExpired(ctx context.Context) (int64, error)
}

// service implements Service by wrapping db.Service.
type service struct {
	db     db.Service
	prefix string
}

// New creates a new state Service wrapping the given db.Service.
func New(dbService db.Service) Service {
	return &service{
		db:     dbService,
		prefix: "",
	}
}

// WithPrefix returns a new Service with the given prefix added.
func (s *service) WithPrefix(prefix string) Service {
	newPrefix := prefix
	if s.prefix != "" {
		newPrefix = s.prefix + ":" + prefix
	}

	return &service{
		db:     s.db,
		prefix: newPrefix,
	}
}

// prefixKey adds the service prefix to a key.
func (s *service) prefixKey(key string) string {
	if s.prefix == "" {
		return key
	}

	return s.prefix + ":" + key
}

// Get retrieves a state entry by key.
func (s *service) Get(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error) {
	return s.db.GetStateEntry(ctx, orgUID, s.prefixKey(key))
}

// Set creates or updates a state entry.
func (s *service) Set(
	ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
) error {
	return s.db.SetStateEntry(ctx, orgUID, s.prefixKey(key), value, ttl)
}

// Delete soft-deletes a state entry.
func (s *service) Delete(ctx context.Context, orgUID *string, key string) error {
	return s.db.DeleteStateEntry(ctx, orgUID, s.prefixKey(key))
}

// List returns all entries matching the key pattern.
func (s *service) List(ctx context.Context, orgUID *string, pattern string) ([]*models.StateEntry, error) {
	keyPrefix := s.prefix
	if pattern != "" {
		if keyPrefix != "" {
			keyPrefix = keyPrefix + ":" + pattern
		} else {
			keyPrefix = pattern
		}
	}

	return s.db.ListStateEntries(ctx, orgUID, keyPrefix)
}

// GetOrCreate returns existing entry or creates new one.
func (s *service) GetOrCreate(
	ctx context.Context, orgUID *string, key string, defaultValue *models.JSONMap, ttl *time.Duration,
) (*models.StateEntry, bool, error) {
	return s.db.GetOrCreateStateEntry(ctx, orgUID, s.prefixKey(key), defaultValue, ttl)
}

// SetIfNotExists creates entry only if key doesn't exist.
func (s *service) SetIfNotExists(
	ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
) (bool, error) {
	return s.db.SetStateEntryIfNotExists(ctx, orgUID, s.prefixKey(key), value, ttl)
}

// DeleteExpired removes entries past their expires_at.
func (s *service) DeleteExpired(ctx context.Context) (int64, error) {
	return s.db.DeleteExpiredStateEntries(ctx)
}
