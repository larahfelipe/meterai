package identity

import "sync"

// PathSource reports the credential file currently in use. Cache takes the
// credential path from here rather than resolving a home directory itself, which
// is what keeps the account it reports tied to the installation being polled.
type PathSource interface {
	Source() string
}

// Cache resolves the account once and holds it. The state document is read at
// most once per credential path: its contents change only when the user
// re-authenticates, and it is large enough — the CLI caches remote feature flags
// in the same file — that re-reading it on every poll would be waste.
//
// INV: mu guards account, err and attemptedPath.
type Cache struct {
	mu          sync.Mutex
	credentials PathSource
	account     *Account
	err         error
	// attemptedPath is the credential path the held result came from. A change
	// means the poller switched installations, which is the one event that can
	// change the account, so the document is read again.
	attemptedPath string
}

func NewCache(credentials PathSource) *Cache {
	return &Cache{credentials: credentials}
}

// Account reports the account being monitored.
//
// It returns (nil, nil) while the answer is still unknown: the credential path
// is not resolved until the first successful read, so the UI must be able to
// draw before an account exists without treating that as a failure. A non-nil
// error means the state document was found unusable and the account rows stay
// hidden; polling is unaffected either way.
func (c *Cache) Account() (*Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	path := c.credentials.Source()
	if path == "" {
		return nil, nil
	}
	if path == c.attemptedPath {
		return c.account, c.err
	}

	c.attemptedPath = path
	statePath, err := StatePathFor(path)
	if err != nil {
		c.account, c.err = nil, err
		return nil, err
	}
	c.account, c.err = Load(statePath)
	return c.account, c.err
}
