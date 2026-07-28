//go:build !windows

package credential

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// This file exists so the package builds and its behaviour can be exercised on
// the development host (Linux/WSL), where the credential file is reachable
// directly. The shipping target is Windows; see locate_windows.go for the
// candidate enumeration that actually matters there.

// Origin records how a candidate path was derived.
type Origin uint8

const (
	// OriginConfigured: explicit path from the user's config file.
	OriginConfigured Origin = iota + 1
	// OriginNativeHome: the vendor's RelPath under $HOME, on the local filesystem.
	OriginNativeHome
)

// Candidate is one place credentials may live. It carries no distribution name:
// nothing on this platform is reached through another operating system, which is
// the whole of what the Windows build needs that field for.
type Candidate struct {
	Path   string
	Origin Origin
}

// isRemoteSource reports whether opening a path can cost more than an open.
// Nothing on this platform is reached through another operating system: the
// development host reads its own filesystem, and the price is always the same.
func isRemoteSource(string) bool { return false }

// Candidates returns probe paths in decreasing order of confidence.
func Candidates(_ context.Context, configuredPath string, rel RelPath) ([]Candidate, error) {
	var out []Candidate
	if configuredPath != "" {
		out = append(out, Candidate{Path: configuredPath, Origin: OriginConfigured})
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out, &Failure{Kind: Absent, Path: "", Err: err}
	}
	return append(out, Candidate{
		Path:   filepath.Join(home, rel.Dir, rel.File),
		Origin: OriginNativeHome,
	}), nil
}

// Discover returns the first candidate that yields valid credentials.
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
	return nil, &Failure{Kind: Absent, Path: "", Err: errors.New("no candidate path holds CLI credentials")}
}
