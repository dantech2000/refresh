package cluster

import (
	"sync"
	"time"
)

// CacheItem represents a cached item with expiration
type CacheItem struct {
	Value     interface{}
	ExpiresAt time.Time
}

// Cache provides thread-safe caching with TTL
type Cache struct {
	items      map[string]CacheItem
	mutex      sync.RWMutex
	defaultTTL time.Duration
}

// NewCache creates a new cache instance
func NewCache(defaultTTL time.Duration) *Cache {
	cache := &Cache{
		items:      make(map[string]CacheItem),
		defaultTTL: defaultTTL,
	}

	// Start cleanup goroutine
	go cache.cleanup()

	return cache
}

// Get retrieves a value from the cache
func (c *Cache) Get(key string) (interface{}, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Now().After(item.ExpiresAt) {
		// Remove expired item
		delete(c.items, key)
		return nil, false
	}

	return item.Value, true
}

// Set stores a value in the cache with specified TTL
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items[key] = CacheItem{
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// SetDefault stores a value in the cache with default TTL
func (c *Cache) SetDefault(key string, value interface{}) {
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

// cleanup periodically removes expired items
func (c *Cache) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mutex.Lock()
		now := time.Now()
		for key, item := range c.items {
			if now.After(item.ExpiresAt) {
				delete(c.items, key)
			}
		}
		c.mutex.Unlock()
	}
}

// Stats returns cache statistics
func (c *Cache) Stats() map[string]interface{} {
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

	return map[string]interface{}{
		"total":   len(c.items),
		"active":  active,
		"expired": expired,
	}
}
