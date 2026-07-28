//go:build !windows

package credential

// These assertions are about locate_other.go's semantics: $HOME as the only
// non-configured source, and an unresolvable one as an error. The Windows build
// answers both differently — %USERPROFILE% plus enumerated distributions, with a
// failed enumeration degrading rather than erroring — so it has its own file.
// Running the suite as `GOOS=windows go test -c` under WSL binfmt is what
// surfaced the mismatch; the untagged version of this file failed there.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// isolateHomeDirectory points $HOME (and, harmlessly on this platform,
// %USERPROFILE%) at an empty temp directory so Candidates/Discover can never
// resolve to this host's real Claude CLI credential file.
func isolateHomeDirectory(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestCandidatesConfiguredPathIsFirst(t *testing.T) {
	isolateHomeDirectory(t)

	candidates, err := Candidates(context.Background(), "/configured/creds.json", testRel)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(candidates) == 0 || candidates[0].Path != "/configured/creds.json" {
		t.Fatalf("candidates = %+v, want the configured path first", candidates)
	}
	if candidates[0].Origin != OriginConfigured {
		t.Errorf("Origin = %v, want OriginConfigured", candidates[0].Origin)
	}
}

func TestCandidatesOmitConfiguredEntryWhenPathIsEmpty(t *testing.T) {
	isolateHomeDirectory(t)

	candidates, err := Candidates(context.Background(), "", testRel)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	for _, c := range candidates {
		if c.Origin == OriginConfigured {
			t.Errorf("an empty configuredPath must not produce a configured candidate: %+v", c)
		}
	}
}

func TestDiscoverConfiguredPathIsAuthoritativeEvenWhenMissing(t *testing.T) {
	home := isolateHomeDirectory(t)

	// A valid credential sits at the default (non-configured) location. If
	// Discover ever fell back to it, this test would observe a successful
	// read instead of the Absent failure it must report.
	homeCredPath := filepath.Join(home, testRel.Dir, testRel.File)
	if err := os.MkdirAll(filepath.Dir(homeCredPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeCredPath, testDocument("TOKEN", 1785022579691), 0o600); err != nil {
		t.Fatal(err)
	}

	missingConfigured := filepath.Join(t.TempDir(), "missing.json")
	_, err := Discover(context.Background(), missingConfigured, testRel, decodeTestDocument)
	if !IsAbsent(err) {
		t.Fatalf("Discover with a missing configured path = %v, want an Absent failure and no fallback", err)
	}
}

func TestDiscoverConfiguredPathSucceeds(t *testing.T) {
	isolateHomeDirectory(t)

	path := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(path, testDocument("TOKEN", 1785022579691), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(context.Background(), path, testRel, decodeTestDocument)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.Source != path {
		t.Errorf("Source = %q, want %q", got.Source, path)
	}
}

func TestDiscoverWithNoConfiguredPathFindsTheHomeCandidate(t *testing.T) {
	home := isolateHomeDirectory(t)

	homeCredPath := filepath.Join(home, testRel.Dir, testRel.File)
	if err := os.MkdirAll(filepath.Dir(homeCredPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeCredPath, testDocument("TOKEN", 1785022579691), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "", testRel, decodeTestDocument)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.Source != homeCredPath {
		t.Errorf("Source = %q, want %q", got.Source, homeCredPath)
	}
}

func TestCandidatesSurfacesAnUnresolvableHomeDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := Candidates(context.Background(), "", testRel); err == nil {
		t.Fatal("Candidates must report an error when the home directory cannot be resolved")
	}
}

func TestDiscoverReportsAbsentWhenNoCandidateExists(t *testing.T) {
	isolateHomeDirectory(t)

	_, err := Discover(context.Background(), "", testRel, decodeTestDocument)
	if !IsAbsent(err) {
		t.Fatalf("Discover with nothing on disk = %v, want an Absent failure", err)
	}
}

func TestDiscoverStopsOnANonAbsentFailureRatherThanMaskingIt(t *testing.T) {
	home := isolateHomeDirectory(t)

	// A malformed file at the only non-configured candidate must surface
	// directly: silently treating it as "absent" would mask a genuine CLI
	// schema change behind a confusing "no credentials found" message.
	homeCredPath := filepath.Join(home, testRel.Dir, testRel.File)
	if err := os.MkdirAll(filepath.Dir(homeCredPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeCredPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Discover(context.Background(), "", testRel, decodeTestDocument)
	var f *Failure
	if !errors.As(err, &f) || f.Kind != Malformed {
		t.Fatalf("Discover = %v, want a Malformed failure surfaced verbatim", err)
	}
}
