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
	// OriginNativeHome: $HOME/.claude on the local filesystem.
	OriginNativeHome
)

// Candidate is one place credentials may live.
type Candidate struct {
	Path   string
	Origin Origin
	Distro string
}

const (
	credentialFileName  = ".credentials.json"
	claudeConfigDirName = ".claude"
)

// Candidates returns probe paths in decreasing order of confidence.
func Candidates(_ context.Context, configuredPath string) ([]Candidate, error) {
	var out []Candidate
	if configuredPath != "" {
		out = append(out, Candidate{Path: configuredPath, Origin: OriginConfigured})
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out, &Failure{Kind: Absent, Path: "", Err: err}
	}
	return append(out, Candidate{
		Path:   filepath.Join(home, claudeConfigDirName, credentialFileName),
		Origin: OriginNativeHome,
	}), nil
}

// Discover returns the first candidate that yields valid credentials.
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
	return nil, &Failure{Kind: Absent, Path: "", Err: errors.New("no candidate path holds Claude CLI credentials")}
}
