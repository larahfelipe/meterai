//go:build windows

package credential

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// This file is compiled only for Windows and is meant to be run as
//
//	GOOS=windows go test -c -o credential.exe ./internal/credential
//
// which WSL executes through binfmt. Every assertion here holds whether or not
// the host has WSL installed: enumeration succeeding or failing is exactly the
// difference these tests are written not to depend on.

func TestCandidatesAlwaysOfferTheNativeWindowsPath(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("USERPROFILE", profile)

	// The error is deliberately ignored: on a host with no WSL, enumeration
	// fails, and the contract is that the native candidate survives it.
	candidates, _ := Candidates(context.Background(), "")

	want := filepath.Join(profile, claudeConfigDirName, credentialFileName)
	found := false
	for _, c := range candidates {
		if c.Origin == OriginNativeWindows && c.Path == want {
			found = true
		}
	}
	if !found {
		t.Errorf("candidates = %+v, want the native path %q", candidates, want)
	}
}

// A broken or absent WSL installation must degrade discovery, never block it.
func TestCandidatesSurviveAFailedDistributionEnumeration(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("USERPROFILE", profile)

	candidates, err := Candidates(context.Background(), "")
	if err != nil && len(candidates) == 0 {
		t.Fatalf("enumeration failed with %v and returned nothing to probe", err)
	}
}

func TestCandidatesPutTheConfiguredPathFirst(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	const pinned = `C:\pinned\.claude\.credentials.json`

	candidates, _ := Candidates(context.Background(), pinned)
	if len(candidates) == 0 || candidates[0].Path != pinned {
		t.Fatalf("candidates = %+v, want %q first", candidates, pinned)
	}
	if candidates[0].Origin != OriginConfigured {
		t.Errorf("origin = %v, want OriginConfigured", candidates[0].Origin)
	}
}

// The only executable this app ever starts must be named by its absolute path
// under the real system directory. Resolving it through PATH would let any
// directory ahead of System32 decide what this process launches.
func TestWSLExecutableIgnoresPATH(t *testing.T) {
	planted := t.TempDir()
	if err := os.WriteFile(filepath.Join(planted, wslExeName), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", planted+";"+os.Getenv("PATH"))

	got, err := wslExecutable()
	if err != nil {
		t.Fatalf("wslExecutable: %v", err)
	}
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatalf("GetSystemDirectory: %v", err)
	}
	if want := filepath.Join(system32, wslExeName); got != want {
		t.Errorf("wslExecutable() = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("wslExecutable() = %q, which is not absolute", got)
	}
	if strings.HasPrefix(got, planted) {
		t.Errorf("wslExecutable() resolved to the planted copy at %q", got)
	}
}

// Every WSL candidate must stay inside the distribution it names. The names that
// build these paths come from wsl.exe's output and from a Linux filesystem, and
// neither is produced by this app.
func TestWSLCandidatesStayInsideTheirDistribution(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	candidates, _ := Candidates(context.Background(), "")
	for _, c := range candidates {
		if c.Origin != OriginWSL {
			continue
		}
		if !strings.HasPrefix(c.Path, wslUNCRoot+`\`+c.Distro+`\`) {
			t.Errorf("candidate %q is outside distribution %q", c.Path, c.Distro)
		}
		if strings.Contains(c.Path, "..") {
			t.Errorf("candidate %q carries a parent reference", c.Path)
		}
		if !strings.HasSuffix(c.Path, `\`+claudeConfigDirName+`\`+credentialFileName) {
			t.Errorf("candidate %q does not name the credential file", c.Path)
		}
	}
}
