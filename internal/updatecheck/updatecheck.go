// Package updatecheck finds out whether a newer release of refresh exists.
//
// It asks the GitHub Releases API for the latest tag and compares it with
// the running version. A small cache file limits the network call to one a
// day. Every failure is an error that the caller ignores, so the check never
// disturbs the CLI.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultURL is the GitHub Releases "latest" endpoint of this project.
const DefaultURL = "https://api.github.com/repos/dantech2000/refresh/releases/latest"

// checkInterval is the minimum time between two network calls. Inside it
// the cached tag is used.
const checkInterval = 24 * time.Hour

// fetchTimeout limits the network call, so a slow endpoint cannot hold up
// "refresh version" for long.
const fetchTimeout = 2 * time.Second

// Checker reads the latest release tag. The zero value works: it asks
// DefaultURL with a new HTTP client and keeps no cache.
type Checker struct {
	// URL is the releases endpoint. Empty means DefaultURL.
	URL string
	// HTTPClient makes the request. Nil means a client with a short timeout.
	HTTPClient *http.Client
	// CachePath is the file that records the last check. Empty turns the
	// cache off, and then every call uses the network.
	CachePath string
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// DefaultCachePath returns update-check.json in the refresh directory under
// the user's configuration directory.
func DefaultCachePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "refresh", "update-check.json"), nil
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// cache is the throttle state persisted between runs.
type cache struct {
	LastCheck time.Time `json:"lastCheck"`
	LatestTag string    `json:"latestTag"`
}

// release is the subset of the GitHub release payload we parse.
type release struct {
	TagName string `json:"tag_name"`
}

// LatestTag returns the most recent release tag, using the cached value when it
// is fresher than checkInterval and otherwise fetching once and updating the
// cache. On a fetch failure it returns the cached tag if there is one;
// otherwise it returns the error, which callers ignore (fail-silent).
func (c *Checker) LatestTag(ctx context.Context) (string, error) {
	cached, ok := c.readCache()
	if ok && c.now().Sub(cached.LastCheck) < checkInterval {
		return cached.LatestTag, nil
	}

	tag, err := c.fetchLatest(ctx)
	if err != nil {
		// Offline/error: record the attempt, keeping whatever (possibly empty)
		// tag we cached, so an offline machine waits on the fetch timeout at
		// most once per checkInterval. A canceled run (Ctrl-C) is not an
		// attempt and is not recorded.
		if ctx.Err() == nil {
			c.writeCache(cache{LastCheck: c.now(), LatestTag: cached.LatestTag})
		}
		if ok {
			return cached.LatestTag, nil
		}
		return "", err
	}

	c.writeCache(cache{LastCheck: c.now(), LatestTag: tag})
	return tag, nil
}

// fetchLatest performs the single HTTP GET against the releases endpoint.
func (c *Checker) fetchLatest(ctx context.Context) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	url := c.URL
	if url == "" {
		url = DefaultURL
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building update-check request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update check: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading update-check response: %w", err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("parsing update-check response: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return "", errors.New("update check: empty tag_name")
	}
	return rel.TagName, nil
}

func (c *Checker) readCache() (cache, bool) {
	if c.CachePath == "" {
		return cache{}, false
	}
	b, err := os.ReadFile(c.CachePath)
	if err != nil {
		return cache{}, false
	}
	var cc cache
	if err := json.Unmarshal(b, &cc); err != nil {
		return cache{}, false
	}
	return cc, true
}

// writeCache persists the cache best-effort; errors are ignored so a read-only
// config dir never breaks the CLI.
func (c *Checker) writeCache(cc cache) {
	if c.CachePath == "" {
		return
	}
	// The directory is shared with the context store, so it gets the usual
	// mode. The cache file itself is private.
	if err := os.MkdirAll(filepath.Dir(c.CachePath), 0o755); err != nil { //nolint:gosec // G301: a shared config directory
		return
	}
	b, err := json.Marshal(cc)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.CachePath, b, 0o600)
}

// UpgradeHint returns a one-line "newer version available" message when latest
// is strictly newer than current, or "" otherwise (including when either is
// "dev"/unparseable). current/latest may be with or without a leading "v".
func UpgradeHint(current, latest string) string {
	cmp, ok := compareSemver(current, latest)
	if !ok || cmp >= 0 {
		return ""
	}
	return fmt.Sprintf("A newer version (%s) is available (you have %s). Upgrade: brew upgrade refresh",
		normalizeDisplay(latest), normalizeDisplay(current))
}

// normalizeDisplay ensures a single leading "v" for display.
func normalizeDisplay(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	return "v" + strings.TrimPrefix(v, "v")
}

// compareSemver compares two vX.Y.Z strings. It returns (-1|0|1, true) for
// current<latest / equal / current>latest, or (0, false) when either version is
// not a parseable release version (e.g. "dev", a pre-release, or malformed).
func compareSemver(current, latest string) (int, bool) {
	cur, ok1 := parseSemver(current)
	lat, ok2 := parseSemver(latest)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := range 3 {
		switch {
		case cur[i] < lat[i]:
			return -1, true
		case cur[i] > lat[i]:
			return 1, true
		}
	}
	return 0, true
}

// parseSemver parses "vX.Y.Z" (or "X.Y.Z") into [3]int. Pre-release/build
// suffixes and non-numeric components make it unparseable (returns false), which
// the caller treats as "skip the check".
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		// Atoi alone would accept a sign ("+1"); a release component is
		// digits only.
		if strings.TrimLeft(p, "0123456789") != "" {
			return out, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
