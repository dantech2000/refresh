package common

import (
	"testing"
	"time"
)

func TestNewTTLCache_Empty(t *testing.T) {
	c := NewTTLCache(0)
	if _, ok := c.Get("any"); ok {
		t.Error("new cache should return miss for any key")
	}
}

func TestTTLCache_SetAndGet(t *testing.T) {
	c := NewTTLCache(0)
	c.Set("k", "v", time.Minute)
	val, ok := c.Get("k")
	if !ok {
		t.Fatal("Get after Set should return true")
	}
	if val != "v" {
		t.Errorf("Get = %v, want %q", val, "v")
	}
}

func TestTTLCache_Get_MissReturnsFalse(t *testing.T) {
	c := NewTTLCache(0)
	c.Set("k1", "v1", time.Minute)
	if _, ok := c.Get("k2"); ok {
		t.Error("Get for missing key should return false")
	}
}

func TestTTLCache_Get_ExpiredReturnsNil(t *testing.T) {
	c := NewTTLCache(0)
	c.Set("k", "v", time.Nanosecond) // expire immediately
	time.Sleep(time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Error("expired cache entry should return false")
	}
}

func TestTTLCache_Overwrite(t *testing.T) {
	c := NewTTLCache(0)
	c.Set("k", "old", time.Minute)
	c.Set("k", "new", time.Minute)
	val, _ := c.Get("k")
	if val != "new" {
		t.Errorf("overwritten value = %v, want %q", val, "new")
	}
}
