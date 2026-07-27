package identity

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stubCredentials is a PathSource whose answers the test controls, standing in
// for credential.Cache without depending on it.
type stubCredentials struct {
	mu     sync.Mutex
	path   string
	remote bool
}

func (s *stubCredentials) Source() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *stubCredentials) SourceIsRemote() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remote
}

func (s *stubCredentials) set(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
}

func (s *stubCredentials) setRemote(remote bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remote = remote
}

// newInlineCache runs every scheduled read on the calling goroutine, so the
// caching rules can be asserted without waiting on anything. The goroutine the
// shipping cache uses is asserted separately, in the tests that name it.
func newInlineCache(credentials PathSource) *Cache {
	cache := NewCache(credentials, nil)
	cache.run = func(read func()) { read() }
	return cache
}

// settled drives one full cycle of an accessor: the call that schedules the
// read, the read, and the call that sees its result. Every caller has this
// shape — the first ask through a new credential path publishes nothing, and it
// is the redraw the cache announces that brings the answer.
func settled[T any](ask func() (*T, error)) (*T, error) {
	ask()
	return ask()
}

// homeWithAccount writes a state document in a home directory and returns the
// credential path inside it, mirroring the real layout.
func homeWithAccount(t *testing.T, document string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, stateFileName), []byte(document), 0o600); err != nil {
		t.Fatalf("write state document: %v", err)
	}
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("create .claude: %v", err)
	}
	return filepath.Join(claudeDir, ".credentials.json")
}

// writePreferences puts a settings document beside a credential path.
func writePreferences(t *testing.T, credentialPath, document string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(credentialPath), settingsFileName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write settings document: %v", err)
	}
}

func TestCacheReportsUnknownBeforeTheCredentialIsResolved(t *testing.T) {
	// The credential path is empty until the first successful read. That is not a
	// failure, and the UI has to be able to draw through it.
	cache := newInlineCache(&stubCredentials{})

	account, err := settled(cache.Account)
	if account != nil || err != nil {
		t.Fatalf("Account() = (%+v, %v), want (nil, nil) while unresolved", account, err)
	}
	prefs, err := settled(cache.Preferences)
	if prefs != nil || err != nil {
		t.Fatalf("Preferences() = (%+v, %v), want (nil, nil) while unresolved", prefs, err)
	}
}

// The first ask through a path cannot answer: it is what schedules the read.
// Reporting anything else would mean the accessor waited for I/O.
func TestCacheAnswersNotYetKnownUntilTheFirstReadLands(t *testing.T) {
	credentials := &stubCredentials{}
	credentials.set(homeWithAccount(t, observedDocument))
	cache := newInlineCache(credentials)

	if account, err := cache.Account(); account != nil || err != nil {
		t.Fatalf("first Account() = (%+v, %v), want (nil, nil)", account, err)
	}
	if account, err := cache.Account(); err != nil || account == nil {
		t.Fatalf("Account() after the read = (%+v, %v)", account, err)
	}
}

func TestCacheLoadsOnceTheCredentialPathIsKnown(t *testing.T) {
	credentials := &stubCredentials{}
	cache := newInlineCache(credentials)
	if _, err := settled(cache.Account); err != nil {
		t.Fatalf("Account() before resolution: %v", err)
	}

	credentials.set(homeWithAccount(t, observedDocument))
	account, err := settled(cache.Account)
	if err != nil {
		t.Fatalf("Account(): %v", err)
	}
	if account.Email != "sample@example.com" {
		t.Errorf("Email = %q", account.Email)
	}
}

func TestCacheDoesNotRereadTheAccountForTheSameCredential(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	credentials.set(credentialPath)
	cache := newInlineCache(credentials)

	first, err := settled(cache.Account)
	if err != nil {
		t.Fatalf("Account(): %v", err)
	}
	// Rewriting the document must not change the answer: the account changes only
	// on re-authentication, and this file is large enough that re-reading it on
	// every poll would be waste.
	statePath := filepath.Join(filepath.Dir(filepath.Dir(credentialPath)), stateFileName)
	if err := os.WriteFile(statePath, []byte(`{"oauthAccount":{"displayName":"Replaced"}}`), 0o600); err != nil {
		t.Fatalf("rewrite state document: %v", err)
	}

	second, err := settled(cache.Account)
	if err != nil {
		t.Fatalf("Account() second call: %v", err)
	}
	if second != first {
		t.Errorf("Account() re-read the document: %+v then %+v", first, second)
	}
}

func TestCacheRereadsWhenTheCredentialPathChanges(t *testing.T) {
	// Switching installations is the one event that can change the account, so it
	// is also the only one that invalidates the held result.
	credentials := &stubCredentials{}
	credentials.set(homeWithAccount(t, observedDocument))
	cache := newInlineCache(credentials)

	if _, err := settled(cache.Account); err != nil {
		t.Fatalf("Account(): %v", err)
	}

	credentials.set(homeWithAccount(t, `{"oauthAccount":{"displayName":"Other","emailAddress":"other@example.com"}}`))
	account, err := settled(cache.Account)
	if err != nil {
		t.Fatalf("Account() after switching: %v", err)
	}
	if account.Email != "other@example.com" {
		t.Errorf("Email = %q, want the account of the newly resolved credential", account.Email)
	}
}

// Documents from a path that is no longer in use would name one account while
// the poller queries another. Nothing is better than wrong.
func TestCacheReportsNothingWhileTheNewPathIsStillUnread(t *testing.T) {
	credentials := &stubCredentials{}
	credentials.set(homeWithAccount(t, observedDocument))
	cache := NewCache(credentials, nil)
	// A runner that never runs: the read is scheduled and never lands.
	cache.run = func(func()) {}

	if _, err := settled(cache.Account); err != nil {
		t.Fatalf("Account(): %v", err)
	}
	if account, _ := cache.Account(); account != nil {
		t.Fatalf("Account() = %+v with no read ever performed", account)
	}
}

func TestCacheHoldsTheFailureItSaw(t *testing.T) {
	credentials := &stubCredentials{}
	// A home with no state document: the CLI has never run there.
	home := t.TempDir()
	credentials.set(filepath.Join(home, ".claude", ".credentials.json"))
	cache := newInlineCache(credentials)

	account, err := settled(cache.Account)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Account() error = %v, want os.ErrNotExist", err)
	}
	if account != nil {
		t.Errorf("Account() = %+v alongside an error", account)
	}
	// The same failure is reported without touching the filesystem again.
	if _, second := cache.Account(); !errors.Is(second, os.ErrNotExist) {
		t.Errorf("second Account() error = %v", second)
	}
}

func TestCacheRejectsACredentialPathItCannotDeriveFrom(t *testing.T) {
	// A path that is non-empty yet unusable must surface as a failure rather than
	// being read as "still unresolved", which would hide it forever.
	credentials := &stubCredentials{}
	credentials.set("   ")
	cache := newInlineCache(credentials)

	account, err := settled(cache.Account)
	if err == nil {
		t.Fatalf("Account() = %+v, want an error", account)
	}
	if account != nil {
		t.Errorf("Account() = %+v alongside an error", account)
	}
	if prefs, err := settled(cache.Preferences); err == nil || prefs != nil {
		t.Errorf("Preferences() = (%+v, %v), want a failure", prefs, err)
	}
}

// One document failing must not hide the other: a CLI that has never signed in
// still has a model configured, and the reverse is just as ordinary.
func TestCacheReadsTheTwoDocumentsIndependently(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := filepath.Join(t.TempDir(), ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writePreferences(t, credentialPath, `{"model":"opus","effortLevel":"high"}`)
	credentials.set(credentialPath)
	cache := newInlineCache(credentials)

	if _, err := settled(cache.Account); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Account() error = %v, want the missing state document", err)
	}
	prefs, err := settled(cache.Preferences)
	if err != nil {
		t.Fatalf("Preferences(): %v", err)
	}
	if prefs.Model != "opus" {
		t.Errorf("Model = %q, want the settings document to survive the other failure", prefs.Model)
	}
}

// A local source costs an open, so the preferences stay as fresh as they were
// before any of this was cached: a model changed in the CLI reaches the menu on
// the next update without the user asking.
func TestCacheRereadsPreferencesFromALocalSource(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	writePreferences(t, credentialPath, `{"model":"sonnet"}`)
	credentials.set(credentialPath)
	cache := newInlineCache(credentials)

	if prefs, err := settled(cache.Preferences); err != nil || prefs.Model != "sonnet" {
		t.Fatalf("Preferences() = (%+v, %v)", prefs, err)
	}

	writePreferences(t, credentialPath, `{"model":"opus"}`)
	prefs, err := settled(cache.Preferences)
	if err != nil {
		t.Fatalf("Preferences() after the CLI was reconfigured: %v", err)
	}
	if prefs.Model != "opus" {
		t.Errorf("Model = %q, want a local document to be re-read", prefs.Model)
	}
}

// A remote source can cost the boot of a stopped distribution, which is not a
// price to pay every five minutes for a decorative row. It is read once, and
// then only when the user asks.
func TestCacheDoesNotRereadPreferencesFromARemoteSource(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	writePreferences(t, credentialPath, `{"model":"sonnet"}`)
	credentials.set(credentialPath)
	credentials.setRemote(true)
	cache := newInlineCache(credentials)

	if prefs, err := settled(cache.Preferences); err != nil || prefs.Model != "sonnet" {
		t.Fatalf("Preferences() = (%+v, %v)", prefs, err)
	}

	writePreferences(t, credentialPath, `{"model":"opus"}`)
	prefs, err := settled(cache.Preferences)
	if err != nil {
		t.Fatalf("Preferences(): %v", err)
	}
	if prefs.Model != "sonnet" {
		t.Errorf("Model = %q, want the held reading: a remote source is not re-read on a cadence", prefs.Model)
	}

	// The Refresh command is what pays that price, and it pays it once.
	cache.Invalidate()
	prefs, err = settled(cache.Preferences)
	if err != nil {
		t.Fatalf("Preferences() after Invalidate: %v", err)
	}
	if prefs.Model != "opus" {
		t.Errorf("Model = %q, want the document the user asked to have re-read", prefs.Model)
	}

	writePreferences(t, credentialPath, `{"model":"haiku"}`)
	if prefs, _ := settled(cache.Preferences); prefs.Model != "opus" {
		t.Errorf("Model = %q, want one re-read per Invalidate", prefs.Model)
	}
}

// Invalidate re-reads both documents. The account is held per credential path
// precisely because it does not change, but a user asking for a refresh has
// asked for everything the row shows.
func TestInvalidateRereadsTheAccountToo(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	credentials.set(credentialPath)
	credentials.setRemote(true)
	cache := newInlineCache(credentials)

	if _, err := settled(cache.Account); err != nil {
		t.Fatalf("Account(): %v", err)
	}
	statePath := filepath.Join(filepath.Dir(filepath.Dir(credentialPath)), stateFileName)
	if err := os.WriteFile(statePath, []byte(`{"oauthAccount":{"emailAddress":"renewed@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cache.Invalidate()
	account, err := settled(cache.Account)
	if err != nil {
		t.Fatalf("Account() after Invalidate: %v", err)
	}
	if account.Email != "renewed@example.com" {
		t.Errorf("Email = %q, want the document re-read on the user's request", account.Email)
	}
}

// An Invalidate that lands while a read is in flight must not be answered by
// that read: it was already reading the bytes the user asked to have replaced.
func TestInvalidateDuringAReadSchedulesAnother(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	writePreferences(t, credentialPath, `{"model":"sonnet"}`)
	credentials.set(credentialPath)
	credentials.setRemote(true)
	cache := NewCache(credentials, nil)
	cache.run = func(read func()) {
		// The user clicks Refresh while this read is under way.
		cache.Invalidate()
		read()
	}

	if prefs, err := settled(cache.Preferences); err != nil || prefs.Model != "sonnet" {
		t.Fatalf("Preferences() = (%+v, %v)", prefs, err)
	}
	cache.run = func(read func()) { read() }
	writePreferences(t, credentialPath, `{"model":"opus"}`)

	if prefs, _ := settled(cache.Preferences); prefs.Model != "opus" {
		t.Errorf("Model = %q, want the re-read the pending Invalidate still owed", prefs.Model)
	}
}

// The UI is redrawn by the announcement, so an announcement on every read would
// schedule the read that produced it. Only a reading that differs is announced.
func TestCacheAnnouncesOnlyRealChanges(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	writePreferences(t, credentialPath, `{"model":"opus"}`)
	credentials.set(credentialPath)

	var announcements int
	cache := NewCache(credentials, func() { announcements++ })
	cache.run = func(read func()) { read() }

	for i := 0; i < 5; i++ {
		if _, err := cache.Preferences(); err != nil {
			t.Fatalf("Preferences(): %v", err)
		}
	}
	if announcements != 1 {
		t.Errorf("announcements = %d, want exactly the one reading that changed", announcements)
	}

	// A persistent failure mints a new error value per read, and comparing those
	// by identity is what would make the announcement self-sustaining.
	if err := os.Remove(filepath.Join(filepath.Dir(credentialPath), settingsFileName)); err != nil {
		t.Fatal(err)
	}
	before := announcements
	for i := 0; i < 5; i++ {
		cache.Preferences()
	}
	if got := announcements - before; got != 1 {
		t.Errorf("a persistent failure announced itself %d times, want 1", got)
	}
}

// The shipping cache reads on a goroutine of its own: the accessor returns
// while the read is still running, which is the whole reason it exists.
func TestCacheReadsOffTheCallersGoroutine(t *testing.T) {
	credentials := &stubCredentials{}
	credentials.set(homeWithAccount(t, observedDocument))

	announced := make(chan struct{}, 1)
	cache := NewCache(credentials, func() { announced <- struct{}{} })

	if account, err := cache.Account(); account != nil || err != nil {
		t.Fatalf("Account() = (%+v, %v), want the caller to be handed a not-yet-known", account, err)
	}
	select {
	case <-announced:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduled read never published anything")
	}
	account, err := cache.Account()
	if err != nil || account == nil {
		t.Fatalf("Account() after the announcement = (%+v, %v)", account, err)
	}
	if account.Email != "sample@example.com" {
		t.Errorf("Email = %q", account.Email)
	}
}

func TestCacheIsSafeForConcurrentUse(t *testing.T) {
	credentials := &stubCredentials{}
	credentials.set(homeWithAccount(t, observedDocument))
	cache := NewCache(credentials, nil)

	// The poller's goroutine and the UI's goroutine both read this, while the
	// cache's own reads publish underneath them.
	const readers = 8
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if account, err := cache.Account(); err != nil {
					t.Errorf("Account() = (%+v, %v)", account, err)
					return
				}
				cache.Preferences()
				cache.Invalidate()
			}
		}()
	}
	wg.Wait()
}
