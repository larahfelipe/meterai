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
)

// Origin records how a candidate path was derived, so the UI can explain which
// environment it is reading and so a WSL-backed source can be treated as
// possibly-unavailable (distro stopped) rather than permanently gone.
type Origin uint8

const (
	// OriginConfigured: explicit path from the user's config file.
	OriginConfigured Origin = iota + 1
	// OriginNativeWindows: %USERPROFILE%\.claude on the Windows filesystem.
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
	// credentialFileName is fixed by the Claude CLI.
	credentialFileName = ".credentials.json"
	// claudeConfigDirName is the CLI's per-user state directory.
	claudeConfigDirName = ".claude"
	// wslUNCRoot is the modern WSL2 network path. Preferred over the legacy
	// \\wsl$ prefix, which remains only as a compatibility alias.
	wslUNCRoot = `\\wsl.localhost`
	// wslExe is resolved through PATH; on a WSL-enabled host it lives in
	// System32 and is always present when the WSL feature is installed.
	wslExe = "wsl.exe"
)

// wslEnumTimeout bounds distro enumeration, which only queries the service
// registry and never boots a distro. A hang here means the WSL service itself is
// wedged.
//
// It is the only timeout left: resolving a home directory used to boot the
// distribution to run a shell in it, and now reads the filesystem instead.
const wslEnumTimeout = 10 * time.Second

// systemDistros are shipped by other products and never contain a user's
// Claude CLI state. Probing them would boot a VM for nothing.
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
func Candidates(ctx context.Context, configuredPath string) ([]Candidate, error) {
	var out []Candidate
	if configuredPath != "" {
		out = append(out, Candidate{Path: configuredPath, Origin: OriginConfigured})
	}
	if profile := os.Getenv("USERPROFILE"); profile != "" {
		out = append(out, Candidate{
			Path:   filepath.Join(profile, claudeConfigDirName, credentialFileName),
			Origin: OriginNativeWindows,
		})
	}
	distros, err := listDistros(ctx)
	if err != nil {
		return out, err
	}
	for _, d := range distros {
		for _, path := range wslCredentialPaths(wslUNCRoot+`\`+d, readDirNames) {
			out = append(out, Candidate{Path: path, Origin: OriginWSL, Distro: d})
		}
	}
	return out, nil
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
func Discover(ctx context.Context, configuredPath string) (*Credentials, error) {
	// An explicitly configured path is authoritative. Falling back to another
	// candidate would silently monitor a different account than the one the
	// user pinned, with no visible difference in the UI.
	if configuredPath != "" {
		return Load(configuredPath)
	}
	candidates, enumErr := Candidates(ctx, configuredPath)
	for _, c := range candidates {
		creds, err := Load(c.Path)
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
	return nil, &Failure{Kind: Absent, Path: "", Err: errors.New("no candidate path holds Claude CLI credentials; configure the path manually")}
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

func runWSL(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, wslExe, args...)
	// WSL_UTF8 makes wsl.exe emit UTF-8 (WSL >= 0.64.0). decodeWSLOutput still
	// handles UTF-16LE so that older builds keep working.
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &Failure{Kind: Unreadable, Path: wslExe + " " + strings.Join(args, " "),
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
