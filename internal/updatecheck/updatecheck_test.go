package updatecheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeHint(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		wantHit bool // true if a hint should be produced
	}{
		{"behind", "v1.2.0", "v1.3.0", true},
		{"behind patch", "1.2.0", "1.2.1", true},
		{"equal", "v1.2.0", "v1.2.0", false},
		{"ahead", "v2.0.0", "v1.9.9", false},
		{"dev local", "dev", "v1.0.0", false},
		{"unparseable latest", "v1.0.0", "nightly", false},
		{"prerelease ignored", "v1.0.0", "v1.1.0-rc1", false},
		{"mixed v prefix", "1.2.0", "v1.3.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := UpgradeHint(tt.current, tt.latest)
			if tt.wantHit && hint == "" {
				t.Fatalf("UpgradeHint(%q,%q) = empty, want a hint", tt.current, tt.latest)
			}
			if !tt.wantHit && hint != "" {
				t.Fatalf("UpgradeHint(%q,%q) = %q, want empty", tt.current, tt.latest, hint)
			}
		})
	}
}

// newTestServer returns a server that serves the given tag and counts hits.
func newTestServer(t *testing.T, tag string, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestTagFetches(t *testing.T) {
	var hits int32
	srv := newTestServer(t, "v9.9.9", &hits)
	c := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithCachePath(filepath.Join(t.TempDir(), "update-check.json")),
		WithNow(func() time.Time { return time.Unix(1_000_000, 0) }),
	)
	tag, err := c.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v9.9.9" {
		t.Fatalf("tag = %q, want v9.9.9", tag)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestLatestTagCacheThrottle(t *testing.T) {
	var hits int32
	srv := newTestServer(t, "v9.9.9", &hits)
	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Unix(1_000_000, 0)

	c := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithCachePath(cachePath),
		WithNow(func() time.Time { return now }),
	)

	// First call hits the network and writes the cache.
	if _, err := c.LatestTag(context.Background()); err != nil {
		t.Fatalf("first LatestTag: %v", err)
	}
	// Second call, 1h later (within the 24h window): must NOT hit the network.
	now = now.Add(time.Hour)
	c2 := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithCachePath(cachePath),
		WithNow(func() time.Time { return now }),
	)
	tag, err := c2.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("second LatestTag: %v", err)
	}
	if tag != "v9.9.9" {
		t.Fatalf("cached tag = %q, want v9.9.9", tag)
	}
	if hits != 1 {
		t.Fatalf("hits = %d after cached call, want 1 (no re-fetch)", hits)
	}

	// More than 24h later: cache is stale, so it re-fetches.
	now = now.Add(48 * time.Hour)
	c3 := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithCachePath(cachePath),
		WithNow(func() time.Time { return now }),
	)
	if _, err := c3.LatestTag(context.Background()); err != nil {
		t.Fatalf("third LatestTag: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d after stale-cache call, want 2", hits)
	}
}

func TestLatestTagFailSilentOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := New(
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithCachePath(filepath.Join(t.TempDir(), "update-check.json")),
		WithNow(func() time.Time { return time.Unix(1_000_000, 0) }),
	)
	tag, err := c.LatestTag(context.Background())
	if err == nil {
		t.Fatalf("expected an error on HTTP 500, got tag=%q", tag)
	}
	if tag != "" {
		t.Fatalf("tag = %q, want empty on error", tag)
	}
	// The caller (maybePrintUpdateHint) treats any error as "no hint", so the
	// returned error never reaches the user — this just documents the contract.
}
