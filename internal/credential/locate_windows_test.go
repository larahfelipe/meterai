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

// Whether a source is remote decides whether the documents beside it are re-read
// on a cadence or only when the user asks, so a path that reaches a distribution
// must be recognized in every form one can be written in.
//
// The distribution names are deliberately all different: this classifier reads
// the root and the separator after it and nothing else, and a table written
// entirely against one distribution could not tell that apart from a table that
// simply never tried another. Validating the name itself is isPlainName's job,
// one layer earlier, before it ever becomes part of a path.
func TestIsRemoteSourceRecognizesEveryWSLRootWhateverTheDistribution(t *testing.T) {
	for path, want := range map[string]bool{
		`\\wsl.localhost\Ubuntu-24.04\home\sample\.claude\.credentials.json`:    true,
		`\\WSL.LOCALHOST\kali-linux\home\sample\.claude\.credentials.json`:      true,
		`\\wsl$\openSUSE-Tumbleweed\home\sample\.claude\.credentials.json`:      true,
		`\\wsl.localhost/Debian/home/sample/.claude/.credentials.json`:          true,
		`\\wsl.localhost\Imported Distro\home\sample\.claude\.credentials.json`: true,
		`\\wsl.localhost\Ubuntu-Ação\home\sample\.claude\.credentials.json`:     true,
		// Filtered out of enumeration as a system distribution, but a path pinned
		// into one by hand still costs what any other distribution costs.
		`\\wsl.localhost\docker-desktop\home\sample\.claude\.credentials.json`: true,
		`C:\Users\sample\.claude\.credentials.json`:                            false,
		`\\fileserver\share\.claude\.credentials.json`:                         false,
		// The bare roots name no file and reach no distribution.
		`\\wsl.localhost`: false,
		`\\wsl$`:          false,
		``:                false,
		// A host whose name merely starts like the root is not the root.
		`\\wsl.localhost.example.com\share\.credentials.json`: false,
	} {
		if got := isRemoteSource(path); got != want {
			t.Errorf("isRemoteSource(%q) = %v, want %v", path, got, want)
		}
	}
}

// The same file under a set of distributions has to classify identically, which
// is the property the table above samples and this one states outright.
func TestIsRemoteSourceIgnoresTheDistributionName(t *testing.T) {
	const suffix = `\home\sample\.claude\.credentials.json`
	for _, distro := range []string{"Ubuntu", "Debian", "kali-linux", "Ubuntu-24.04", "NixOS", "Imported Distro", "x"} {
		if !isRemoteSource(wslUNCRoot + `\` + distro + suffix) {
			t.Errorf("distribution %q was not recognized as remote", distro)
		}
	}
}
