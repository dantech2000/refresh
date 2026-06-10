package nodegroup

import (
	"github.com/dantech2000/refresh/internal/services/common"
)

// Cache is the nodegroup service's TTL cache, backed by the shared
// common.TTLCache implementation.
type Cache = common.TTLCache

// NewCache creates a new cache instance. Callers always pass explicit TTLs to
// Set, so no default TTL is configured.
func NewCache() *Cache {
	return common.NewTTLCache(0)
}
