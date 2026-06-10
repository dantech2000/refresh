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

// NewCache creates a new cache instance. Expired entries are evicted
// opportunistically on Set; there is no background goroutine to manage.
func NewCache(defaultTTL time.Duration) *Cache {
	return &Cache{
		items:      make(map[string]CacheItem),
		defaultTTL: defaultTTL,
	}
}

// Get retrieves a value from the cache
func (c *Cache) Get(key string) (any, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Now().After(item.ExpiresAt) {
		// Do not mutate under read lock; treat as miss and let cleanup remove it
		return nil, false
	}

	return item.Value, true
}

// Set stores a value in the cache with specified TTL. It also evicts any
// expired entries while holding the write lock — the cache holds at most a
// handful of keys in this short-lived CLI, so the sweep is cheap and replaces
// the previous background cleanup goroutine (which could never be stopped).
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()
	for k, item := range c.items {
		if now.After(item.ExpiresAt) {
			delete(c.items, k)
		}
	}

	c.items[key] = CacheItem{
		Value:     value,
		ExpiresAt: now.Add(ttl),
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
