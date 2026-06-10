package cluster

import (
	"sync"
	"time"
)

// CacheItem represents a cached item with expiration
type CacheItem struct {
	Value     any
	ExpiresAt time.Time
}

// Cache provides thread-safe caching with TTL
type Cache struct {
	items      map[string]CacheItem
	mutex      sync.RWMutex
	defaultTTL time.Duration
}

// NewCache creates a new cache instance. Expired entries are deleted lazily
// on Get — a short-lived CLI process doesn't need a background janitor
// goroutine (which would leak: forRegion creates one service per region).
func NewCache(defaultTTL time.Duration) *Cache {
	return &Cache{
		items:      make(map[string]CacheItem),
		defaultTTL: defaultTTL,
	}
}

// Get retrieves a value from the cache, lazily deleting expired entries.
func (c *Cache) Get(key string) (any, bool) {
	c.mutex.RLock()
	item, exists := c.items[key]
	c.mutex.RUnlock()
	if !exists {
		return nil, false
	}

	if time.Now().After(item.ExpiresAt) {
		c.mutex.Lock()
		// Re-check under the write lock: another goroutine may have replaced
		// the entry with a fresh value in the meantime.
		if current, ok := c.items[key]; ok && time.Now().After(current.ExpiresAt) {
			delete(c.items, key)
		}
		c.mutex.Unlock()
		return nil, false
	}

	return item.Value, true
}

// Set stores a value in the cache with specified TTL
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items[key] = CacheItem{
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// SetDefault stores a value in the cache with default TTL
func (c *Cache) SetDefault(key string, value any) {
	c.Set(key, value, c.defaultTTL)
}

// Delete removes a value from the cache
func (c *Cache) Delete(key string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	delete(c.items, key)
}

// Clear removes all items from the cache
func (c *Cache) Clear() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items = make(map[string]CacheItem)
}

// Stats returns cache statistics
func (c *Cache) Stats() map[string]any {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	expired := 0
	active := 0
	now := time.Now()

	for _, item := range c.items {
		if now.After(item.ExpiresAt) {
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
