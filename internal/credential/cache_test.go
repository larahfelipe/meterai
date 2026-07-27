package credential

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// documentExpiringAt renders a credential document whose access token expires
// at the given instant.
func documentExpiringAt(t time.Time, token string) []byte {
	return []byte(`{"claudeAiOauth":{"accessToken":"` + token + `","expiresAt":` +
		strconv.FormatInt(t.UnixMilli(), 10) + `,"subscriptionType":"pro"}}`)
}

// newCacheOnFile returns a Cache pinned to a temp credential file plus a
// pointer to the clock it reads, so tests can move time without sleeping.
func newCacheOnFile(t *testing.T, contents []byte) (*Cache, string, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), credentialFileName)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 25, 18, 0, 0, 0, time.UTC)
	c := NewCache(path, DefaultSkewMargin)
	c.now = func() time.Time { return clock }
	return c, path, &clock
}

func TestNewCacheDefaultsAnInvalidSkewMargin(t *testing.T) {
	for _, given := range []time.Duration{0, -time.Minute} {
		c := NewCache("", given)
		if c.skewMargin != DefaultSkewMargin {
			t.Errorf("NewCache(skew=%v).skewMargin = %v, want the default %v", given, c.skewMargin, DefaultSkewMargin)
		}
	}
	const explicit = 10 * time.Minute
	if c := NewCache("", explicit); c.skewMargin != explicit {
		t.Errorf("an explicit positive skew margin must be honoured, got %v", c.skewMargin)
	}
}

func TestCacheReadsOnceWhileTokenIsFresh(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 23, 0, 0, 0, time.UTC)
	c, path, _ := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-A"))

	first, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if first.AccessToken.Reveal() != "TOKEN-A" {
		t.Fatalf("unexpected token")
	}
	if c.Source() != path {
		t.Errorf("Source = %q, want %q", c.Source(), path)
	}

	// Rewrite the file. A fresh cached token must NOT trigger a re-read: that
	// is the property that keeps a stopped WSL distro asleep between polls.
	if err := os.WriteFile(path, documentExpiringAt(expiry, "TOKEN-B"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if second.AccessToken.Reveal() != "TOKEN-A" {
		t.Errorf("cache re-read the file while the token was still fresh")
	}
}

func TestCacheReloadsWhenTokenNearsExpiry(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)
	c, path, clock := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-OLD"))

	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}

	// The CLI renews the token on disk.
	renewed := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, documentExpiringAt(renewed, "TOKEN-NEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Advance to inside the skew margin of the old expiry.
	*clock = expiry.Add(-time.Minute)

	got, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after expiry: %v", err)
	}
	if got.AccessToken.Reveal() != "TOKEN-NEW" {
		t.Errorf("cache served a stale token past the skew margin")
	}
}

func TestCacheReportsExpiredWhenDiskCopyIsAlsoStale(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)
	c, _, clock := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-OLD"))
	*clock = expiry.Add(time.Hour)

	_, err := c.Token(context.Background())
	var f *Failure
	if !errors.As(err, &f) || f.Kind != Expired {
		t.Fatalf("want Expired failure, got %v", err)
	}
	if IsAbsent(err) {
		t.Error("an expired token must not be treated as a skippable candidate")
	}
}

// An access token no call site may use is exposure held for the life of the
// process: the cache reports the expiry and keeps nothing.
func TestCacheRetainsNoSecretItCannotUse(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)
	c, path, clock := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-A"))

	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if c.current == nil {
		t.Fatal("a usable token must be cached")
	}

	*clock = expiry.Add(time.Hour)
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("an expired token on disk must be reported")
	}
	if c.current != nil {
		t.Errorf("cache held %v past the point any caller could use it", c.current.ExpiresAt)
	}
	// The path survives the expiry: it is what locates the CLI's own documents,
	// and the account behind a dead credential is still the one being described.
	if c.Source() != path {
		t.Errorf("Source = %q, want %q", c.Source(), path)
	}

	// A renewal on disk is picked up with nothing stale in the way.
	renewed := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, documentExpiringAt(renewed, "TOKEN-B"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after renewal: %v", err)
	}
	if got.AccessToken.Reveal() != "TOKEN-B" {
		t.Errorf("cache did not pick up the renewed token")
	}
}

func TestCacheSurvivesTransientSourceOutage(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 23, 0, 0, 0, time.UTC)
	c, path, clock := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-A"))

	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// The WSL distro stops: the file becomes unreachable.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Move inside the skew margin so a reload is attempted and fails, while
	// the token itself is still genuinely valid for another minute.
	*clock = expiry.Add(-time.Minute)

	got, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("outage with a still-valid token must not fail the poll: %v", err)
	}
	if got.AccessToken.Reveal() != "TOKEN-A" {
		t.Errorf("unexpected token")
	}

	// Once the token is genuinely dead, the outage must surface.
	*clock = expiry.Add(time.Minute)
	if _, err := c.Token(context.Background()); err == nil {
		t.Error("a dead token plus an unreachable source must be an error")
	}
}

// Source is read by the UI on every menu update, while the lock it used to take
// is held for the whole of a reload — which on Windows can mean starting a
// stopped WSL distribution. It must answer regardless.
func TestSourceDoesNotWaitOnAnInFlightReload(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 23, 0, 0, 0, time.UTC)
	c, path, _ := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-A"))
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}

	// Stand in for a reload in progress: Token holds this lock across the whole
	// of Discover.
	c.mu.Lock()
	defer c.mu.Unlock()

	answered := make(chan string, 1)
	go func() { answered <- c.Source() }()
	select {
	case got := <-answered:
		if got != path {
			t.Errorf("Source = %q, want %q", got, path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Source blocked behind the reload lock")
	}
}

func TestCacheIsConcurrencySafe(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 23, 0, 0, 0, time.UTC)
	c, _, _ := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-A"))

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.Token(context.Background()); err != nil {
				t.Errorf("concurrent Token: %v", err)
			}
			_ = c.Source()
		}()
	}
	wg.Wait()
}

// A source on this platform is always local, so nothing that reads the documents
// beside it has a reason to hold back.
func TestSourceIsRemoteOnTheDevelopmentHost(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 23, 0, 0, 0, time.UTC)
	c, _, _ := newCacheOnFile(t, documentExpiringAt(expiry, "TOKEN-A"))

	if c.SourceIsRemote() {
		t.Error("SourceIsRemote = true before any path was resolved")
	}
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.SourceIsRemote() {
		t.Error("a path on this host's own filesystem must not read as remote")
	}
}
