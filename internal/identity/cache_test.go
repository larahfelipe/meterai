package identity

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// stubCredentials is a PathSource whose answer the test controls, standing in for
// credential.Cache without depending on it.
type stubCredentials struct {
	mu   sync.Mutex
	path string
}

func (s *stubCredentials) Source() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *stubCredentials) set(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
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

func TestCacheReportsUnknownBeforeTheCredentialIsResolved(t *testing.T) {
	// The credential path is empty until the first successful read. That is not a
	// failure, and the UI has to be able to draw through it.
	cache := NewCache(&stubCredentials{})

	account, err := cache.Account()
	if account != nil || err != nil {
		t.Fatalf("Account() = (%+v, %v), want (nil, nil) while unresolved", account, err)
	}
}

func TestCacheLoadsOnceTheCredentialPathIsKnown(t *testing.T) {
	credentials := &stubCredentials{}
	cache := NewCache(credentials)
	if _, err := cache.Account(); err != nil {
		t.Fatalf("Account() before resolution: %v", err)
	}

	credentials.set(homeWithAccount(t, observedDocument))
	account, err := cache.Account()
	if err != nil {
		t.Fatalf("Account(): %v", err)
	}
	if account.Email != "sample@example.com" {
		t.Errorf("Email = %q", account.Email)
	}
}

func TestCacheDoesNotRereadForTheSameCredential(t *testing.T) {
	credentials := &stubCredentials{}
	credentialPath := homeWithAccount(t, observedDocument)
	credentials.set(credentialPath)
	cache := NewCache(credentials)

	first, err := cache.Account()
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

	second, err := cache.Account()
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
	cache := NewCache(credentials)

	if _, err := cache.Account(); err != nil {
		t.Fatalf("Account(): %v", err)
	}

	credentials.set(homeWithAccount(t, `{"oauthAccount":{"displayName":"Other","emailAddress":"other@example.com"}}`))
	account, err := cache.Account()
	if err != nil {
		t.Fatalf("Account() after switching: %v", err)
	}
	if account.Email != "other@example.com" {
		t.Errorf("Email = %q, want the account of the newly resolved credential", account.Email)
	}
}

func TestCacheHoldsTheFailureItSaw(t *testing.T) {
	credentials := &stubCredentials{}
	// A home with no state document: the CLI has never run there.
	home := t.TempDir()
	credentials.set(filepath.Join(home, ".claude", ".credentials.json"))
	cache := NewCache(credentials)

	account, err := cache.Account()
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

func TestCacheIsSafeForConcurrentUse(t *testing.T) {
	credentials := &stubCredentials{}
	credentials.set(homeWithAccount(t, observedDocument))
	cache := NewCache(credentials)

	// The poller's goroutine and the UI's goroutine both read this.
	const readers = 8
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if account, err := cache.Account(); err != nil || account.Email == "" {
					t.Errorf("Account() = (%+v, %v)", account, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCacheRejectsACredentialPathItCannotDeriveFrom(t *testing.T) {
	// A path that is non-empty yet unusable must surface as a failure rather than
	// being read as "still unresolved", which would hide it forever.
	credentials := &stubCredentials{}
	credentials.set("   ")
	cache := NewCache(credentials)

	account, err := cache.Account()
	if err == nil {
		t.Fatalf("Account() = %+v, want an error", account)
	}
	if account != nil {
		t.Errorf("Account() = %+v alongside an error", account)
	}
}
