package cluster

import (
	"testing"
	"time"
)

func TestNewCache_Empty(t *testing.T) {
	c := NewCache(time.Minute)
	if _, ok := c.Get("any"); ok {
		t.Error("new cache should be empty")
	}
}

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("k", "v", time.Minute)
	val, ok := c.Get("k")
	if !ok {
		t.Fatal("Get after Set should return true")
	}
	if val != "v" {
		t.Errorf("Get = %v, want %q", val, "v")
	}
}

func TestCache_Get_Expired(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("k", "v", time.Nanosecond)
	time.Sleep(time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Error("expired entry should return false")
	}
}

func TestCache_SetDefault_UsesDefaultTTL(t *testing.T) {
	c := NewCache(time.Minute)
	c.SetDefault("k", "v")
	if _, ok := c.Get("k"); !ok {
		t.Error("SetDefault should store value accessible via Get")
	}
}

func TestCache_Delete(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("k", "v", time.Minute)
	c.Delete("k")
	if _, ok := c.Get("k"); ok {
		t.Error("Deleted key should not be retrievable")
	}
}

func TestCache_Delete_NonexistentKeyIsNoOp(t *testing.T) {
	c := NewCache(time.Minute)
	c.Delete("ghost") // should not panic
}

func TestCache_Clear(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("a", 1, time.Minute)
	c.Set("b", 2, time.Minute)
	c.Clear()
	if _, ok := c.Get("a"); ok {
		t.Error("Clear should remove all entries")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("Clear should remove all entries")
	}
}

// Set must evict expired entries (this replaced the background cleanup
// goroutine, which could never be stopped and leaked per cache instance).
func TestCache_Set_EvictsExpiredEntries(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("stale", 1, time.Nanosecond)
	time.Sleep(time.Millisecond)
	c.Set("fresh", 2, time.Minute)

	stats := c.Stats()
	if stats["total"] != 1 {
		t.Errorf("total = %v, want 1 (stale entry should be evicted by Set)", stats["total"])
	}
	if _, ok := c.Get("fresh"); !ok {
		t.Error("fresh entry should still be retrievable")
	}
}

func TestCache_Stats_Active(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("a", 1, time.Minute)
	c.Set("b", 2, time.Minute)
	stats := c.Stats()
	if stats["total"] != 2 {
		t.Errorf("total = %v, want 2", stats["total"])
	}
	if stats["active"] != 2 {
		t.Errorf("active = %v, want 2", stats["active"])
	}
	if stats["expired"] != 0 {
		t.Errorf("expired = %v, want 0", stats["expired"])
	}
}

func TestCache_Stats_WithExpired(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("fresh", 1, time.Minute)
	c.Set("stale", 2, time.Nanosecond)
	time.Sleep(time.Millisecond)
	stats := c.Stats()
	if stats["expired"] == 0 {
		t.Error("Stats should count expired entries")
	}
}
