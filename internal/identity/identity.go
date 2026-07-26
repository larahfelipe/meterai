// Package identity reads the account details the official Claude CLI caches in
// its own state document. It exists so the UI can name the account being
// monitored without this app calling a profile endpoint of its own: the CLI has
// already fetched that profile and written it to disk, so reading its cache
// costs no request, works offline, and adds no second undocumented endpoint to
// depend on.
//
// The trade-off is a schema nobody publishes. The document carries a
// migrationVersion the CLI has already bumped repeatedly, so every field here is
// optional and every failure is non-fatal by contract: the caller hides a row
// rather than failing a poll. Like package credential, this package never
// writes.
//
// Account details never reach a log line or an error message. The errors below
// carry the path and the reason only.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// stateFileName is the CLI's per-user state document. It sits beside the
	// .claude directory rather than inside it.
	stateFileName = ".claude.json"

	// maxStateBytes bounds the read. The observed document is 37 KiB, but the
	// CLI caches remote feature flags in the same file and that section has no
	// stated ceiling, so the cap is deliberately generous: its job is to stop an
	// unbounded allocation, not to enforce a size the CLI never promised.
	maxStateBytes = 8 << 20
)

// ErrNoAccount reports a document that parsed but carried no account detail
// worth displaying. It is distinct from a read or parse failure because it is
// the expected state for a CLI that has been installed but never signed in.
var ErrNoAccount = errors.New("the CLI state document carries no account details")

// Account is the subset of the cached profile the UI shows. Every field is
// optional and an empty one hides its row; none of them is authoritative for
// behaviour, only for display.
type Account struct {
	DisplayName  string
	Email        string
	Organization string
}

// StatePathFor derives the state document from the credential file currently in
// use. Resolving it from the running user's home directory instead would let the
// UI name one account while the poller queries another, which is exactly what
// happens when the credential comes from a WSL distribution or from a path
// pinned in the config file.
func StatePathFor(credentialPath string) (string, error) {
	trimmed := strings.TrimSpace(credentialPath)
	if trimmed == "" {
		return "", errors.New("credential path is empty; the CLI state document cannot be located")
	}
	// <home>/.claude/.credentials.json -> <home>/.claude -> <home>
	home := filepath.Dir(filepath.Dir(trimmed))
	return filepath.Join(home, stateFileName), nil
}

// stateDocument records the observed schema. The document also carries
// accountUuid, organizationUuid, organizationRole, workspaceRole, seatTier,
// billingType and profileFetchedAt inside the same object, plus machine and
// project state elsewhere; none of it is read, so none of it is retained.
type stateDocument struct {
	OauthAccount *struct {
		DisplayName  string `json:"displayName"`
		Email        string `json:"emailAddress"`
		Organization string `json:"organizationName"`
	} `json:"oauthAccount"`
}

// Parse validates state-document bytes. It returns ErrNoAccount when the
// document is well-formed but names no account, so the caller can tell "not
// signed in" from "unreadable".
func Parse(raw []byte) (*Account, error) {
	var doc stateDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		// The decoder's own message can quote the offending value, and this
		// document holds the user's project history, so the underlying error is
		// deliberately not wrapped: only its structural cause is reported.
		return nil, fmt.Errorf("decode CLI state document: %s", jsonReason(err))
	}
	if doc.OauthAccount == nil {
		return nil, ErrNoAccount
	}
	account := &Account{
		DisplayName:  strings.TrimSpace(doc.OauthAccount.DisplayName),
		Email:        strings.TrimSpace(doc.OauthAccount.Email),
		Organization: strings.TrimSpace(doc.OauthAccount.Organization),
	}
	if account.DisplayName == "" && account.Email == "" && account.Organization == "" {
		return nil, ErrNoAccount
	}
	return account, nil
}

// jsonReason reduces a decoding failure to its structural cause. encoding/json
// quotes the offending value in some of its errors, and that value can be a
// fragment of the user's own data.
func jsonReason(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Sprintf("field %q holds a %s where a %s was expected",
			typeErr.Field, typeErr.Value, typeErr.Type)
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("malformed JSON at byte offset %d", syntaxErr.Offset)
	}
	return "unrecognized document structure"
}

// Load reads and parses the state document at path. A missing file yields an
// error wrapping os.ErrNotExist, which is the ordinary case on a machine where
// the CLI has never run.
func Load(path string) (*Account, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CLI state document %q: %w", path, err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CLI state document %q: %w", path, err)
	}
	if len(raw) > maxStateBytes {
		return nil, fmt.Errorf("CLI state document %q exceeds the %d byte limit", path, maxStateBytes)
	}

	account, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("CLI state document %q: %w", path, err)
	}
	return account, nil
}
