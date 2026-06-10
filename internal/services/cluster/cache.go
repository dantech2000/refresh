package cluster

import (
	"time"

	"github.com/dantech2000/refresh/internal/services/common"
)

// Cache is the cluster service's TTL cache, backed by the shared
// common.TTLCache implementation.
type Cache = common.TTLCache

// NewCache creates a new cache instance with the given default TTL.
func NewCache(defaultTTL time.Duration) *Cache {
	return common.NewTTLCache(defaultTTL)
}
