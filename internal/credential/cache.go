package credential

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Cache is the CredentialSource the providers consume. It exists to decouple
// poll cadence from filesystem access: the access token lives for hours, so
// re-reading the file on every poll would wake a stopped WSL distro every few
// minutes for no benefit. The file is re-read only when the cached token
// approaches expiry, or when it was never read at all.
//
// INV: mu guards current, and Token is its only mutator; the cache is safe for
// concurrent use by the poller and by a user-triggered manual refresh.
// resolvedPath is atomic rather than guarded because Source must answer while a
// reload holds mu — see Source.
type Cache struct {
	mu sync.Mutex
	// current is the last successfully parsed credential set, or nil.
	current *Credentials
	// resolvedPath is the path current came from. Re-reading it directly skips
	// the wsl.exe distro enumeration on the common path.
	resolvedPath atomic.Pointer[string]

	configuredPath string
	skewMargin     time.Duration
	// now is injected so expiry behaviour is deterministic under test.
	now func() time.Time
}

// DefaultSkewMargin is the headroom applied to token expiry. It absorbs clock
// drift between this host and Anthropic's edge, and the round-trip time of a
// poll issued just before the boundary.
const DefaultSkewMargin = 5 * time.Minute

// NewCache builds a Cache. configuredPath may be empty, in which case
// discovery falls back to the platform's candidate list.
func NewCache(configuredPath string, skewMargin time.Duration) *Cache {
	if skewMargin <= 0 {
		skewMargin = DefaultSkewMargin
	}
	return &Cache{configuredPath: configuredPath, skewMargin: skewMargin, now: time.Now}
}

// Token returns credentials usable now, re-reading from disk when the cached
// copy is stale. A stale cache plus an unreadable source is reported as
// Unreadable with the cached value discarded only if the fresh read succeeded,
// so a transient WSL outage cannot silently downgrade a usable token.
func (c *Cache) Token(ctx context.Context) (*Credentials, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.current != nil && c.current.IsUsableAt(c.now(), c.skewMargin) {
		return c.current, nil
	}

	fresh, err := c.reload(ctx)
	if err != nil {
		if c.current != nil {
			// The source is momentarily unavailable (distro stopped, UNC
			// hiccup) but the cached token has not yet expired hard: prefer it
			// over failing the poll.
			if c.current.IsUsableAt(c.now(), 0) {
				return c.current, nil
			}
		}
		return nil, err
	}

	c.current = fresh
	source := fresh.Source
	c.resolvedPath.Store(&source)
	if !fresh.IsUsableAt(c.now(), c.skewMargin) {
		return nil, &Failure{Kind: Expired, Path: fresh.Source,
			Err: errors.New("the credential file on disk holds an expired access token; run the claude CLI once to renew it")}
	}
	return fresh, nil
}

// reload reads the previously resolved path first, falling back to full
// discovery when that path no longer yields credentials (distro renamed, user
// re-installed the CLI elsewhere).
func (c *Cache) reload(ctx context.Context) (*Credentials, error) {
	if path := c.Source(); path != "" {
		if fresh, err := Load(path); err == nil {
			return fresh, nil
		}
	}
	return Discover(ctx, c.configuredPath)
}

// Source reports the path currently in use, or "" before the first successful
// read. It is the only way to tell which of several candidate paths won.
//
// It deliberately takes no lock. The UI reads it on every menu update to decide
// which installation it is describing, while the reload holding mu can take as
// long as starting a stopped WSL distribution; blocking the UI behind that would
// leave the tray unresponsive for as long as discovery runs.
func (c *Cache) Source() string {
	if path := c.resolvedPath.Load(); path != nil {
		return *path
	}
	return ""
}
