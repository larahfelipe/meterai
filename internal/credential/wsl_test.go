package credential

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeListing serves one directory listing and records what was asked for, so a
// test can assert both the paths produced and the paths consulted.
type fakeListing struct {
	entries map[string][]string
	asked   []string
	err     error
}

func (f *fakeListing) list(dir string) ([]string, error) {
	f.asked = append(f.asked, dir)
	if f.err != nil {
		return nil, f.err
	}
	entries, ok := f.entries[dir]
	if !ok {
		return nil, errors.New("no such directory")
	}
	return entries, nil
}

func TestWSLCredentialPathsCoverEveryLoginHome(t *testing.T) {
	listing := &fakeListing{entries: map[string][]string{
		`\\wsl.localhost\Ubuntu\home`: {"felipe", "deploy"},
	}}

	paths := wslCredentialPaths(`\\wsl.localhost\Ubuntu`, testRel, listing.list)
	want := []string{
		// Sorted, because a directory listing carries no order of its own.
		`\\wsl.localhost\Ubuntu\home\deploy\.testvendor\creds.json`,
		`\\wsl.localhost\Ubuntu\home\felipe\.testvendor\creds.json`,
		// root is last: the CLI is normally run as an ordinary user.
		`\\wsl.localhost\Ubuntu\root\.testvendor\creds.json`,
	}
	assertPaths(t, paths, want)

	// Only the home parent is listed. Anything else would be this app walking a
	// filesystem it has no business walking.
	if len(listing.asked) != 1 || listing.asked[0] != `\\wsl.localhost\Ubuntu\home` {
		t.Errorf("directories listed = %v, want the home parent alone", listing.asked)
	}
}

// A distribution that has never been started, or one with no /home at all, still
// has to yield the root candidate rather than nothing.
func TestWSLCredentialPathsSurviveAnUnreadableHomeParent(t *testing.T) {
	for name, listing := range map[string]*fakeListing{
		"listing fails": {err: errors.New("the distribution is not running")},
		"no /home":      {entries: map[string][]string{}},
		"empty /home":   {entries: map[string][]string{`\\wsl.localhost\Alpine\home`: {}}},
	} {
		t.Run(name, func(t *testing.T) {
			paths := wslCredentialPaths(`\\wsl.localhost\Alpine`, testRel, listing.list)
			assertPaths(t, paths, []string{`\\wsl.localhost\Alpine\root\.testvendor\creds.json`})
		})
	}
}

// The home directory names come from a filesystem another operating system owns.
// A name carrying a separator or a parent reference would build a path outside
// the distribution being searched.
func TestWSLCredentialPathsRejectNamesThatAreNotOneComponent(t *testing.T) {
	listing := &fakeListing{entries: map[string][]string{
		`\\wsl.localhost\Ubuntu\home`: {
			`..`, `.`, ``, `..\..\..\Users\Victim`, `sub/dir`, `C:`, `ok`,
		},
	}}

	paths := wslCredentialPaths(`\\wsl.localhost\Ubuntu`, testRel, listing.list)
	assertPaths(t, paths, []string{
		`\\wsl.localhost\Ubuntu\home\ok\.testvendor\creds.json`,
		`\\wsl.localhost\Ubuntu\root\.testvendor\creds.json`,
	})
	for _, path := range paths {
		if strings.Contains(path, "..") {
			t.Errorf("path escapes the distribution: %q", path)
		}
	}
}

// Every probe is a file open over a slow transport, so a pathological listing
// must not become a hang.
func TestWSLCredentialPathsAreBounded(t *testing.T) {
	many := make([]string, 500)
	for i := range many {
		many[i] = fmt.Sprintf("user%03d", i)
	}
	listing := &fakeListing{entries: map[string][]string{`\\wsl.localhost\Big\home`: many}}

	paths := wslCredentialPaths(`\\wsl.localhost\Big`, testRel, listing.list)
	if len(paths) != maxWSLHomeDirs+1 {
		t.Fatalf("produced %d paths, want %d homes plus root", len(paths), maxWSLHomeDirs)
	}
	// The cap keeps the first names in sorted order rather than an arbitrary
	// window of them.
	if !strings.Contains(paths[0], `\user000\`) {
		t.Errorf("first path = %q, want the lowest-sorting home", paths[0])
	}
}

// Nothing here may run inside the distribution: the whole point of reading the
// filesystem is that no process is created to learn a directory name.
func TestWSLCredentialPathsNeverNameAShell(t *testing.T) {
	listing := &fakeListing{entries: map[string][]string{`\\wsl.localhost\Ubuntu\home`: {"felipe"}}}
	for _, path := range wslCredentialPaths(`\\wsl.localhost\Ubuntu`, testRel, listing.list) {
		for _, forbidden := range []string{"sh", "bash", "cmd", "powershell", "-c"} {
			if strings.Contains(path, " "+forbidden) {
				t.Errorf("path %q carries a command fragment", path)
			}
		}
	}
}

func TestIsPlainName(t *testing.T) {
	for name, want := range map[string]bool{
		"felipe":       true,
		"Ubuntu-22.04": true,
		"user name":    true,
		"":             false,
		".":            false,
		"..":           false,
		`a\b`:          false,
		"a/b":          false,
		"C:":           false,
		`..\..\Users`:  false,
	} {
		if got := isPlainName(name); got != want {
			t.Errorf("isPlainName(%q) = %v, want %v", name, got, want)
		}
	}
}

// wsl.exe is a child process, and a child process is an unbounded producer: what
// it writes has to stop growing this process's heap at a stated limit, while the
// command itself still completes rather than failing on a short write.
func TestBoundedBufferKeepsOnlyItsLimit(t *testing.T) {
	const limit = 8
	b := &boundedBuffer{limit: limit}

	for _, chunk := range [][]byte{[]byte("12345"), []byte("6789"), []byte("overflow")} {
		n, err := b.Write(chunk)
		if err != nil {
			t.Fatalf("Write(%q): %v", chunk, err)
		}
		if n != len(chunk) {
			t.Errorf("Write(%q) = %d, want %d: a short write fails the command", chunk, n, len(chunk))
		}
	}
	if got := string(b.Bytes()); got != "12345678" {
		t.Errorf("Bytes() = %q, want the first %d bytes", got, limit)
	}
}

func TestBoundedBufferWithNoRoomKeepsNothing(t *testing.T) {
	b := &boundedBuffer{}
	if n, err := b.Write([]byte("anything")); n != 8 || err != nil {
		t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
	}
	if len(b.Bytes()) != 0 {
		t.Errorf("Bytes() = %q, want nothing", b.Bytes())
	}
}

func assertPaths(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
}
