package common

import (
	"sync"
	"time"
)

// cacheItem is a cached value with its expiration time.
type cacheItem struct {
	value     any
	expiresAt time.Time
}

// TTLCache is a thread-safe in-memory cache with per-entry TTLs. Expired
// entries are deleted lazily on Get — a short-lived CLI process doesn't need
// a background janitor goroutine. It is the single cache implementation
// shared by the service packages (cluster and nodegroup previously kept
// near-identical copies).
type TTLCache struct {
	mu         sync.RWMutex
	items      map[string]cacheItem
	defaultTTL time.Duration
}

// NewTTLCache creates a cache. defaultTTL is used by SetDefault; pass 0 if
// every Set call provides its own TTL.
func NewTTLCache(defaultTTL time.Duration) *TTLCache {
	return &TTLCache{
		items:      make(map[string]cacheItem),
		defaultTTL: defaultTTL,
	}
}

// Get retrieves a value from the cache, lazily deleting expired entries.
func (c *TTLCache) Get(key string) (any, bool) {
	c.mu.RLock()
	item, exists := c.items[key]
	c.mu.RUnlock()
	if !exists {
		return nil, false
	}

	if time.Now().After(item.expiresAt) {
		c.mu.Lock()
		// Re-check under the write lock: another goroutine may have replaced
		// the entry with a fresh value in the meantime.
		if current, ok := c.items[key]; ok && time.Now().After(current.expiresAt) {
			delete(c.items, key)
		}
		c.mu.Unlock()
		return nil, false
	}

	return item.value, true
}

// Set stores a value with the given TTL.
func (c *TTLCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheItem{value: value, expiresAt: time.Now().Add(ttl)}
}

// SetDefault stores a value with the cache's default TTL.
func (c *TTLCache) SetDefault(key string, value any) {
	c.Set(key, value, c.defaultTTL)
}

// Delete removes a value from the cache.
func (c *TTLCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Clear removes all items from the cache.
func (c *TTLCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheItem)
}

// Stats returns cache statistics.
func (c *TTLCache) Stats() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	expired := 0
	active := 0
	now := time.Now()

	for _, item := range c.items {
		if now.After(item.expiresAt) {
			expired++
		} else {
			active++
		}
	}

	return map[string]any{
		"total":   len(c.items),
		"active":  active,
		"expired": expired,
	}
}
