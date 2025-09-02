// Package cache provides a simple in-memory cache with optional TTL and weak reference support.
package cache

import (
	"sync"
	"time"
	"weak"
)

// Entry represents a cached value with optional expiration using weak references.
type Entry[T any] struct {
	Value     weak.Pointer[T]
	ExpiresAt *time.Time
}

// Cache is a simple in-memory cache with optional TTL support and weak references.
type Cache[T any] struct {
	mu      sync.RWMutex
	entries map[string]*Entry[T]
}

// New creates a new Cache instance.
func New[T any]() *Cache[T] {
	return &Cache[T]{
		entries: make(map[string]*Entry[T]),
	}
}

// Set stores a value with an optional TTL using weak references.
// If ttl is 0, the entry never expires (but can still be GC'd due to weak reference).
func (c *Cache[T]) Set(key string, value *T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := &Entry[T]{
		Value: weak.Make(value),
	}

	if ttl > 0 {
		expiresAt := time.Now().Add(ttl)
		entry.ExpiresAt = &expiresAt
	}

	c.entries[key] = entry
}

// Get retrieves a value from the cache.
// Returns the value and true if found, not expired, and not GC'd; nil and false otherwise.
func (c *Cache[T]) Get(key string) (*T, bool) {
	c.mu.RLock()
	entry, exists := c.entries[key]
	c.mu.RUnlock()

	if !exists {
		return nil, false
	}

	// Check if entry has expired
	if entry.ExpiresAt != nil && time.Now().After(*entry.ExpiresAt) {
		// Entry expired, remove it
		c.Delete(key)
		return nil, false
	}

	// Try to get the value from weak pointer
	value := entry.Value.Value()
	if value == nil {
		// Value was garbage collected, remove entry
		c.Delete(key)
		return nil, false
	}

	return value, true
}

// Delete removes a value from the cache.
func (c *Cache[T]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
}

// Clear removes all entries from the cache.
func (c *Cache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*Entry[T])
}

// Cleanup removes all expired entries and entries with GC'd values.
// This can be called periodically to free memory.
func (c *Cache[T]) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0

	for key, entry := range c.entries {
		// Check if expired
		if (entry.ExpiresAt != nil && now.After(*entry.ExpiresAt)) || entry.Value.Value() == nil {
			delete(c.entries, key)

			removed++
		}
	}

	return removed
}

// Len returns the number of entries in the cache.
func (c *Cache[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// StartCleanupTimer starts a periodic cleanup of expired and GC'd entries.
// Returns a function to stop the cleanup timer.
func (c *Cache[T]) StartCleanupTimer(interval time.Duration) func() {
	ticker := time.NewTicker(interval)
	done := make(chan bool)

	go func() {
		for {
			select {
			case <-ticker.C:
				c.Cleanup()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	return func() {
		done <- true

		close(done)
	}
}
