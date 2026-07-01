package mcpcal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const defaultCacheTTL = 15 * time.Minute

// cacheEntry is a feed's cached body plus the HTTP validators needed to
// revalidate it cheaply (conditional GET).
type cacheEntry struct {
	ETag         string    `json:"etag"`
	LastModified string    `json:"last_modified"`
	FetchedAt    time.Time `json:"fetched_at"`
	Body         string    `json:"body"` // raw ICS (text)
}

// feedCache persists one cacheEntry per feed URL as JSON on disk, so large feeds
// aren't re-downloaded every run. Files are named by a hash of the URL (the URL
// carries the secret token, so it's never used as a filename). A nil/empty-dir
// cache is a no-op (disabled).
type feedCache struct {
	dir string
	mu  sync.Mutex
}

func newFeedCache(dir string) *feedCache { return &feedCache{dir: dir} }

// calCacheDir resolves where feed bodies are cached: AGENTBOX_CAL_CACHE_DIR if
// set, else the persisted memory dir. Empty disables the on-disk cache.
func calCacheDir() string {
	if d := os.Getenv("AGENTBOX_CAL_CACHE_DIR"); d != "" {
		return d
	}
	return os.Getenv("AGENTBOX_MEMORY_DIR")
}

// calCacheTTL is how long a cached body is served without even revalidating.
// AGENTBOX_CAL_CACHE_TTL (seconds) overrides; 0 means always revalidate via a
// conditional request.
func calCacheTTL() time.Duration {
	if v := os.Getenv("AGENTBOX_CAL_CACHE_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultCacheTTL
}

func (c *feedCache) path(url string) string {
	h := sha256.Sum256([]byte(url))
	return filepath.Join(c.dir, "cal-"+hex.EncodeToString(h[:])+".json")
}

func (c *feedCache) load(url string) (cacheEntry, bool) {
	if c == nil || c.dir == "" {
		return cacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(c.path(url))
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if json.Unmarshal(b, &e) != nil || e.Body == "" {
		return cacheEntry{}, false
	}
	return e, true
}

func (c *feedCache) save(url string, e cacheEntry) error {
	if c == nil || c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	p := c.path(url)
	tmp := p + ".tmp"
	// 0o600: the cached body holds calendar PII (attendees, titles, locations).
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p) // atomic replace
}
