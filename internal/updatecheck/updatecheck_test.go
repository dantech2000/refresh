package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	c := &Checker{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		CachePath:  filepath.Join(t.TempDir(), "update-check.json"),
		Now:        func() time.Time { return time.Unix(1_000_000, 0) },
	}
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

	c := &Checker{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		CachePath:  cachePath,
		Now:        func() time.Time { return now },
	}

	// First call hits the network and writes the cache.
	if _, err := c.LatestTag(context.Background()); err != nil {
		t.Fatalf("first LatestTag: %v", err)
	}
	// Second call, 1h later (within the 24h window): must NOT hit the network.
	now = now.Add(time.Hour)
	c2 := &Checker{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		CachePath:  cachePath,
		Now:        func() time.Time { return now },
	}
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
	c3 := &Checker{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		CachePath:  cachePath,
		Now:        func() time.Time { return now },
	}
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

	c := &Checker{
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		CachePath:  filepath.Join(t.TempDir(), "update-check.json"),
		Now:        func() time.Time { return time.Unix(1_000_000, 0) },
	}
	tag, err := c.LatestTag(context.Background())
	if err == nil {
		t.Fatalf("expected an error on HTTP 500, got tag=%q", tag)
	}
	if tag != "" {
		t.Fatalf("tag = %q, want empty on error", tag)
	}
	// The caller treats any error as "no hint", so the
	// returned error never reaches the user — this just documents the contract.
}

// TestLatestTagRecordsFailedAttempt: offline, a failed fetch still starts a
// new check interval, so the next run within it does not wait on the network
// again. The previously cached tag survives the failed attempt.
func TestLatestTagRecordsFailedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seedTag string // "" = no cache file before the failed fetch
	}{
		{name: "no cache", seedTag: ""},
		{name: "stale cache", seedTag: "v1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(srv.Close)
			cachePath := filepath.Join(t.TempDir(), "update-check.json")
			now := time.Unix(1_000_000, 0)
			if tc.seedTag != "" {
				(&Checker{CachePath: cachePath}).writeCache(cache{LastCheck: now.Add(-48 * time.Hour), LatestTag: tc.seedTag})
			}
			newChecker := func() *Checker {
				return &Checker{
					URL:        srv.URL,
					HTTPClient: srv.Client(),
					CachePath:  cachePath,
					Now:        func() time.Time { return now },
				}
			}

			_, _ = newChecker().LatestTag(context.Background())
			if got := atomic.LoadInt32(&hits); got != 1 {
				t.Fatalf("hits = %d after first call, want 1", got)
			}

			now = now.Add(time.Hour)
			tag, err := newChecker().LatestTag(context.Background())
			if err != nil {
				t.Fatalf("second LatestTag: %v", err)
			}
			if tag != tc.seedTag {
				t.Fatalf("tag = %q, want the cached %q", tag, tc.seedTag)
			}
			if got := atomic.LoadInt32(&hits); got != 1 {
				t.Fatalf("hits = %d within the interval after a failure, want 1 (no re-fetch)", got)
			}
		})
	}
}

// TestLatestTagCanceledRunNotRecorded: an interrupted run is not an attempt,
// so the next run checks again.
func TestLatestTagCanceledRunNotRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Checker{
		URL:       "http://127.0.0.1:0",
		CachePath: filepath.Join(t.TempDir(), "update-check.json"),
		Now:       func() time.Time { return time.Unix(1_000_000, 0) },
	}
	if _, err := c.LatestTag(ctx); err == nil {
		t.Fatal("expected an error with a canceled context")
	}
	if _, ok := c.readCache(); ok {
		t.Fatal("a canceled run wrote the cache; want no record")
	}
}

// TestWriteCacheReplacesAtomically: the cache is replaced by a rename, so a
// reader that opened the old file still reads all of the old content, and no
// temporary file stays behind.
func TestWriteCacheReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update-check.json")
	c := &Checker{CachePath: cachePath}
	first := cache{LastCheck: time.Unix(1_000_000, 0).UTC(), LatestTag: "v1.0.0"}
	second := cache{LastCheck: time.Unix(2_000_000, 0).UTC(), LatestTag: "v2.0.0"}

	c.writeCache(first)
	before, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("the first write made no cache file: %v", err)
	}
	if got := before.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Errorf("cache file mode = %o, want 600", got)
	}
	reader, err := os.Open(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	c.writeCache(second)

	after, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the second write changed the cache file in place; want a new file renamed over it")
	}
	var old cache
	if err := json.NewDecoder(reader).Decode(&old); err != nil {
		t.Fatalf("reading through the handle opened before the write: %v", err)
	}
	if old != first {
		t.Errorf("old handle read %+v, want %+v", old, first)
	}
	if got, ok := c.readCache(); !ok || got != second {
		t.Errorf("readCache() = %+v, %t; want %+v, true", got, ok, second)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "update-check.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("cache directory holds %v, want only update-check.json", names)
	}
}

// TestWriteCacheFailureLeavesNoFile: a cache that cannot be replaced keeps
// its content, and the temporary file is removed.
func TestWriteCacheFailureLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	// A directory at the cache path makes the rename fail.
	cachePath := filepath.Join(dir, "update-check.json")
	if err := os.Mkdir(cachePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cachePath, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	(&Checker{CachePath: cachePath}).writeCache(cache{LatestTag: "v1.0.0"})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("cache directory holds %d entries, want 1: the temporary file was not removed", len(entries))
	}
}
