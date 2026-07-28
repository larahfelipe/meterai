//go:build windows

package credential

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// Origin records how a candidate path was derived. It is what the containment
// test asserts against: a WSL-derived path must stay inside the distribution it
// names, and that check needs to know which candidates came from a filesystem
// this app does not own.
type Origin uint8

const (
	// OriginConfigured: explicit path from the user's config file.
	OriginConfigured Origin = iota + 1
	// OriginNativeWindows: the vendor's RelPath under %USERPROFILE%, on the
	// Windows filesystem.
	OriginNativeWindows
	// OriginWSL: reached over the \\wsl.localhost 9P transport.
	OriginWSL
)

// Candidate is one place credentials may live.
type Candidate struct {
	Path   string
	Origin Origin
	// Distro is set only when Origin is OriginWSL.
	Distro string
}

const (
	// wslUNCRoot is the modern WSL2 network path. Preferred over the legacy
	// \\wsl$ prefix, which remains only as a compatibility alias.
	wslUNCRoot = `\\wsl.localhost`
	// wslExeName is the launcher the WSL optional component installs in the
	// system directory. It is never resolved through PATH: a PATH lookup starts
	// whichever executable of that name comes first, so any directory ahead of
	// System32 that is writable by something other than an administrator becomes
	// a way to have this process launch a program of someone else's choosing.
	wslExeName = "wsl.exe"
)

// wslEnumTimeout bounds distro enumeration, which only queries the service
// registry and never boots a distro. A hang here means the WSL service itself is
// wedged.
//
// It is the only timeout left: resolving a home directory used to boot the
// distribution to run a shell in it, and now reads the filesystem instead.
const wslEnumTimeout = 10 * time.Second

// systemDistros are shipped by other products and never contain a user's CLI
// state. Probing them would boot a VM for nothing.
var systemDistros = map[string]struct{}{
	"docker-desktop":       {},
	"docker-desktop-data":  {},
	"rancher-desktop":      {},
	"rancher-desktop-data": {},
}

// Candidates returns probe paths in decreasing order of confidence. Enumerating
// WSL distros spawns wsl.exe once, with a fixed argument list and no shell; if
// that fails the Windows-native candidates are still returned, so a broken WSL
// install degrades rather than blocks discovery.
//
// Nothing is executed inside a distribution. Its home directories are read over
// the same \\wsl.localhost transport the credential is read through, which is
// why an unusual home location is out of reach and the credentialPath setting is
// the answer for it.
func Candidates(ctx context.Context, configuredPath string, rel RelPath) ([]Candidate, error) {
	var out []Candidate
	if configuredPath != "" {
		out = append(out, Candidate{Path: configuredPath, Origin: OriginConfigured})
	}
	if profile := os.Getenv("USERPROFILE"); profile != "" {
		out = append(out, Candidate{
			Path:   filepath.Join(profile, rel.Dir, rel.File),
			Origin: OriginNativeWindows,
		})
	}
	distros, err := listDistros(ctx)
	if err != nil {
		return out, err
	}
	for _, d := range distros {
		for _, path := range wslCredentialPaths(wslUNCRoot+`\`+d, rel, readDirNames) {
			out = append(out, Candidate{Path: path, Origin: OriginWSL, Distro: d})
		}
	}
	return out, nil
}

// wslLegacyUNCRoot is the prefix WSL used before wsl.localhost and still honours.
// Nothing here builds one, but a user may well have pinned one in credentialPath,
// and it reaches the same distribution at the same price.
const wslLegacyUNCRoot = `\\wsl$`

// isRemoteSource reports whether a path is served by a WSL distribution, and so
// whether opening it can cost the boot of a stopped one rather than an open.
//
// Comparison is case-insensitive because these are Windows paths and the user may
// have typed this one into the settings document by hand.
func isRemoteSource(path string) bool {
	for _, root := range [...]string{wslUNCRoot, wslLegacyUNCRoot} {
		if len(path) > len(root) && strings.EqualFold(path[:len(root)], root) &&
			(path[len(root)] == '\\' || path[len(root)] == '/') {
			return true
		}
	}
	return false
}

// readDirNames lists a directory's entry names. Reaching a stopped distribution
// this way starts it, exactly as opening the credential file would.
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// Discover returns the first candidate that yields valid credentials. Absent
// and unreadable candidates are skipped; a malformed or incomplete file is
// returned immediately, because silently falling through would mask a CLI
// schema change behind a stale secondary source.
func Discover(ctx context.Context, configuredPath string, rel RelPath, decode Decoder) (*Credentials, error) {
	// An explicitly configured path is authoritative. Falling back to another
	// candidate would silently monitor a different account than the one the
	// user pinned, with no visible difference in the UI.
	if configuredPath != "" {
		return Load(configuredPath, decode)
	}
	candidates, enumErr := Candidates(ctx, configuredPath, rel)
	for _, c := range candidates {
		creds, err := Load(c.Path, decode)
		if err == nil {
			return creds, nil
		}
		if !IsAbsent(err) {
			return nil, err
		}
	}
	if enumErr != nil {
		return nil, enumErr
	}
	return nil, &Failure{Kind: Absent, Path: "", Err: errors.New("no candidate path holds CLI credentials; configure the path manually")}
}

func listDistros(ctx context.Context) ([]string, error) {
	raw, err := runWSL(ctx, wslEnumTimeout, "-l", "-q")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, isSystem := systemDistros[strings.ToLower(name)]; isSystem {
			continue
		}
		// The name is about to become a path component. wsl.exe is trusted, but
		// the distribution name is chosen by whoever registered it.
		if !isPlainName(name) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// wslExecutable names the launcher by its absolute path under the real system
// directory, which is what keeps a PATH entry from deciding what this process
// executes. A host where the system directory cannot even be resolved has
// nothing to enumerate, so the failure degrades discovery to the Windows-native
// candidate rather than falling back to a search.
func wslExecutable() (string, error) {
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(system32, wslExeName), nil
}

func runWSL(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	executable, err := wslExecutable()
	if err != nil {
		return "", &Failure{Kind: Unreadable, Path: wslExeName, Err: err}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, args...)
	// WSL_UTF8 makes wsl.exe emit UTF-8 (WSL >= 0.64.0). decodeWSLOutput still
	// handles UTF-16LE so that older builds keep working.
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	stdout := &boundedBuffer{limit: maxWSLOutputBytes}
	stderr := &boundedBuffer{limit: maxWSLOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", &Failure{Kind: Unreadable, Path: wslExeName + " " + strings.Join(args, " "),
			Err: errors.New(err.Error() + ": " + decodeWSLOutput(stderr.Bytes()))}
	}
	return decodeWSLOutput(stdout.Bytes()), nil
}

// decodeWSLOutput normalizes wsl.exe output, which is UTF-16LE on builds that
// predate WSL_UTF8 support and UTF-8 afterwards. Detection is by NUL byte:
// valid UTF-8 text never contains one, while UTF-16LE ASCII is half NULs.
func decodeWSLOutput(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.ContainsRune(b, 0) {
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	b = bytes.TrimPrefix(b, []byte{0xFF, 0xFE})
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	s := string(utf16.Decode(units))
	if !utf8.ValidString(s) {
		return ""
	}
	return strings.ReplaceAll(s, "\r\n", "\n")
}
