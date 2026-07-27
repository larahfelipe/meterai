package identity

import (
	"sync"
	"sync/atomic"
)

// PathSource reports the credential file currently in use, and what reading the
// documents beside it costs. Cache takes the path from here rather than resolving
// a home directory itself, which is what keeps the account it reports tied to the
// installation being polled.
type PathSource interface {
	Source() string
	// SourceIsRemote reports whether that file is served by another operating
	// system, where an open can cost the boot of a stopped virtual machine
	// instead of a few microseconds. Cache never learns what that other system
	// is; it only reads the price off this call.
	SourceIsRemote() bool
}

// documents is one reading of both CLI documents beside one credential file. It
// is built complete, published whole and never mutated, which is what lets the
// accessors answer from a single atomic load.
type documents struct {
	path        string
	account     *Account
	accountErr  error
	preferences *Preferences
	prefsErr    error
}

// Cache answers from memory what the official CLI recorded locally about the
// installation being polled.
//
// Neither accessor performs I/O, for the reason credential.Cache.Source takes no
// lock: the caller is the goroutine that also dispatches menu clicks, and on a
// WSL-hosted installation one open can cost the boot of a stopped distribution.
// The read runs on a goroutine of its own and publishes atomically; a caller that
// asks before it has finished is told the answer is not known yet, which is a
// state it already has to handle.
//
// When a read is due depends on what it costs, because the two documents have
// different lifetimes and so do the filesystems they sit on:
//
//   - The account is read once per credential path, whatever the price. It
//     changes only on re-authentication, and the state document holds the CLI's
//     cache of remote feature flags besides. An explicit Invalidate reads it
//     again regardless: signing out and back in as somebody else rewrites that
//     document in place, leaving the path it was reached through unchanged.
//   - The preferences are re-read on every call from a local source, where the
//     price is an open — the behaviour before any of this was cached. From a
//     remote source they are read once and then only when the user asks, because
//     there the same call can start a virtual machine, and a decorative row is
//     not worth waking another operating system on a five-minute cadence.
//
// INV: mu guards reading and forced; published is written only by read, and only
// while reading is set, which is what makes that single writer exclusive.
type Cache struct {
	credentials PathSource
	// onChange announces that the published documents now differ from what the
	// caller last drew. It must not block: the UI adapter is expected to hand off
	// to its own event loop, exactly as poll.Poller's callback does.
	onChange func()

	published atomic.Pointer[documents]
	// run executes one scheduled read. It is injected for the same reason
	// poll.Poller injects its timer: the default is what ships and is what makes
	// the accessors non-blocking, while a test that runs the read inline asserts
	// the caching rules without waiting on a goroutine.
	run func(read func())

	mu sync.Mutex
	// reading is the single-flight guard. Without it every accessor on every
	// update would start a read of its own against the same file.
	reading bool
	// forced records a re-read the user asked for, and survives until one is
	// actually scheduled: an Invalidate that lands while a read is in flight must
	// not be answered by that read, which was already reading stale bytes.
	forced bool
}

// ignoreChange is the sink for a Cache built without a callback: the documents
// stay readable through the accessors, so there is nothing to announce.
func ignoreChange() {} // nothing to announce to

// NewCache builds a Cache. onChange may be nil, in which case a new reading is
// published but not announced, and the next caller sees it.
func NewCache(credentials PathSource, onChange func()) *Cache {
	if onChange == nil {
		onChange = ignoreChange
	}
	return &Cache{
		credentials: credentials,
		onChange:    onChange,
		run:         func(read func()) { go read() },
	}
}

// Account reports the account being monitored.
//
// It returns (nil, nil) while the answer is still unknown: the credential path is
// not resolved until the first successful poll, and the read that follows it is
// not instant, so the UI must be able to draw before an account exists without
// treating that as a failure. A non-nil error means the state document was found
// unusable and the account rows stay hidden; polling is unaffected either way.
func (c *Cache) Account() (*Account, error) {
	inUse := c.inUse()
	if inUse == nil {
		return nil, nil
	}
	return inUse.account, inUse.accountErr
}

// Preferences reports the model and effort the CLI is configured to prefer. The
// contract is identical to Account's, and the two are read independently: one
// document failing never hides the other.
func (c *Cache) Preferences() (*Preferences, error) {
	inUse := c.inUse()
	if inUse == nil {
		return nil, nil
	}
	return inUse.preferences, inUse.prefsErr
}

// Invalidate asks for one re-read of both documents.
//
// It is what keeps a remote source readable at all after the first time. A user
// who changes the model in their CLI and then asks the menu to refresh is the one
// moment at which paying for a possible virtual-machine boot is the thing they
// asked for, which is why this is reached from that click and from nowhere else.
// It performs no I/O itself and does not wait for the read it schedules.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.forced = true
	c.mu.Unlock()
}

// inUse returns the published documents when they describe the credential file
// currently in use, scheduling a read whenever one is due. It returns nil while
// nothing describing that file has been published — before the first read, and
// after the path changes underneath one — which is the "not known yet" both
// accessors report as (nil, nil).
//
// Documents from a previous path are never returned: they would name one account
// while the poller queries another, and a wrong name is worse than no name.
func (c *Cache) inUse() *documents {
	path := c.credentials.Source()
	if path == "" {
		return nil
	}
	published := c.published.Load()
	describesSource := published != nil && published.path == path
	remote := c.credentials.SourceIsRemote()

	c.mu.Lock()
	forced := c.forced
	due := (!describesSource || forced || !remote) && !c.reading
	if due {
		c.reading, c.forced = true, false
	}
	c.mu.Unlock()

	// Scheduled outside the lock: read takes the same one, and a runner that
	// executes inline would otherwise deadlock against it.
	if due {
		c.run(func() { c.read(path, forced) })
	}

	if describesSource {
		return published
	}
	return nil
}

// read performs the I/O for one credential path and publishes the result.
//
// Nothing joins the goroutine this runs on: it terminates on its own, and a
// process exiting mid-read loses only a value that would have been replaced. The
// single-flight guard makes this the only writer of published, so the reading it
// compares against cannot change underneath it.
func (c *Cache) read(path string, forced bool) {
	previous := c.published.Load()
	fresh := &documents{path: path}

	// The account is carried forward unless the user asked for it: it is the
	// expensive document, and nothing short of a re-authentication changes it.
	if !forced && previous != nil && previous.path == path {
		fresh.account, fresh.accountErr = previous.account, previous.accountErr
	} else if statePath, err := StatePathFor(path); err != nil {
		fresh.accountErr = err
	} else {
		fresh.account, fresh.accountErr = Load(statePath)
	}

	if settingsPath, err := SettingsPathFor(path); err != nil {
		fresh.prefsErr = err
	} else {
		fresh.preferences, fresh.prefsErr = LoadPreferences(settingsPath)
	}

	changed := !fresh.equals(previous)
	if changed {
		c.published.Store(fresh)
	}

	c.mu.Lock()
	c.reading = false
	c.mu.Unlock()

	if changed {
		c.onChange()
	}
}

// equals reports whether two readings would draw the same rows.
//
// Errors are compared by message rather than by identity, and that is what makes
// the announcement terminate: every failed read mints a new error value, so
// identity would report a change on every read, the change would announce itself
// to the UI, and the update that announcement triggers would schedule the read
// that produced it.
func (d *documents) equals(other *documents) bool {
	if other == nil {
		return false
	}
	return d.path == other.path &&
		samePointee(d.account, other.account) &&
		errorText(d.accountErr) == errorText(other.accountErr) &&
		samePointee(d.preferences, other.preferences) &&
		errorText(d.prefsErr) == errorText(other.prefsErr)
}

// samePointee compares two optional documents by value, nil being a value of its
// own. Both are structs of strings, so equality is what it looks like.
func samePointee[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
