// Package updatecheck implements an opt-in, fail-silent "newer release
// available" check for the refresh CLI.
//
// It queries the GitHub Releases API for the latest tag, compares it against
// the running version with a small semver comparison, and (when the local
// build is behind) returns a one-line upgrade hint. Results are throttled via a
// small JSON cache under the user config dir so the network is hit at most once
// per day; every failure path is silent so the check never disrupts the CLI.
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

// DefaultBaseURL is the GitHub Releases "latest" endpoint for this project.
const DefaultBaseURL = "https://api.github.com/repos/dantech2000/refresh/releases/latest"

// checkInterval is the minimum time between network fetches. Within this window
// the cached tag is reused.
const checkInterval = 24 * time.Hour

// fetchTimeout bounds the network call so a slow/hanging endpoint can't add
// measurable latency to `refresh version`.
const fetchTimeout = 2 * time.Second

// Checker performs throttled, fail-silent update checks. The zero value is not
// usable; construct it with New.
type Checker struct {
	baseURL    string
	httpClient *http.Client
	cachePath  string
	now        func() time.Time
}

// Option customizes a Checker. Tests inject a base URL, HTTP client, cache path,
// and clock so no real network or real home dir is touched.
type Option func(*Checker)

// WithBaseURL overrides the releases endpoint.
func WithBaseURL(u string) Option { return func(c *Checker) { c.baseURL = u } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Checker) { c.httpClient = h } }

// WithCachePath overrides the cache file path.
func WithCachePath(p string) Option { return func(c *Checker) { c.cachePath = p } }

// WithNow overrides the clock.
func WithNow(now func() time.Time) Option { return func(c *Checker) { c.now = now } }

// New builds a Checker with production defaults, applying any options.
func New(opts ...Option) *Checker {
	c := &Checker{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: fetchTimeout},
		cachePath:  defaultCachePath(),
		now:        time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// defaultCachePath resolves <user-config-dir>/refresh/update-check.json. On any
// error it returns "", which disables caching (the check still works, just
// un-throttled, which the caller's TTL logic tolerates).
func defaultCachePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "refresh", "update-check.json")
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
// cache. It is fail-silent: any network/parse/cache error yields ("", nil-ish)
// without surfacing the error to the caller's hot path.
func (c *Checker) LatestTag(ctx context.Context) (string, error) {
	cached, ok := c.readCache()
	if ok && c.now().Sub(cached.LastCheck) < checkInterval {
		return cached.LatestTag, nil
	}

	tag, err := c.fetchLatest(ctx)
	if err != nil {
		// Offline/error: fall back to whatever (possibly empty) tag we cached
		// so we still don't re-hit the network repeatedly.
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

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return "", fmt.Errorf("building update-check request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
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
	if c.cachePath == "" {
		return cache{}, false
	}
	b, err := os.ReadFile(c.cachePath)
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
	if c.cachePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.cachePath), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(cc)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.cachePath, b, 0o644)
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
	for i := 0; i < 3; i++ {
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
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
