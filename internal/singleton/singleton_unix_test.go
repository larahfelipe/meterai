//go:build !windows

package singleton

import (
	"errors"
	"path/filepath"
	"testing"
)

// isolateLock points the guard at a per-test path so the suite does not
// contend with a real instance running on the development host.
func isolateLock(t *testing.T) {
	t.Helper()
	original := lockPath
	lockPath = filepath.Join(t.TempDir(), "meterAI.lock")
	t.Cleanup(func() { lockPath = original })
}

func TestAcquireIsExclusive(t *testing.T) {
	isolateLock(t)

	release, err := Acquire()
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	if _, err := Acquire(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire = %v, want ErrAlreadyRunning", err)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// The guard must be reusable after release, so a restart is not blocked by
	// the previous run.
	second, err := Acquire()
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := second(); err != nil {
		t.Errorf("second release: %v", err)
	}
}

func TestReleaseIsNotSilentOnASecondCall(t *testing.T) {
	isolateLock(t)

	release, err := Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	// The doc comment requires exactly one call; a second call must still
	// report an error rather than panic on the now-closed descriptor.
	if err := release(); err == nil {
		t.Error("releasing an already-released guard silently succeeded")
	}
}

func TestAcquireReportsUnusablePath(t *testing.T) {
	original := lockPath
	// A path whose parent directory does not exist cannot be created.
	lockPath = filepath.Join(t.TempDir(), "no-such-dir", "meterAI.lock")
	t.Cleanup(func() { lockPath = original })

	_, err := Acquire()
	if err == nil {
		t.Fatal("an uncreatable lock path must be an error, not a silent success")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Error("an I/O failure must not be reported as a running instance")
	}
}
