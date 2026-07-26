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
	// settingsFileName is the CLI's user-level settings document. Unlike the state
	// document it sits *inside* the .claude directory, beside the credential file.
	settingsFileName = "settings.json"

	// maxSettingsBytes bounds the read. The observed document is under 1 KiB, but
	// the same file holds the user's permission rules, hooks and environment
	// overrides, none of which has a stated ceiling; the cap exists to stop an
	// unbounded allocation, not to enforce a size the CLI never promised.
	maxSettingsBytes = 1 << 20
)

// ErrNoPreferences reports a settings document that parsed but declared neither a
// model nor an effort level. It is the expected state for a CLI left on its
// defaults, which is why it is distinct from a read or parse failure.
var ErrNoPreferences = errors.New("the CLI settings document declares no model or effort level")

// Preferences is what the official CLI is configured to prefer, as opposed to
// what any particular session is running.
//
// Both fields are the user-level default only. The CLI resolves an effective
// value per session from a chain this app cannot observe — a runtime `/model`
// choice, an environment variable, a project's own settings file — so a session
// may well be using something else. Nothing here is authoritative for behaviour;
// it is display text, and an empty field hides its row.
type Preferences struct {
	Model       string
	EffortLevel string
}

// SettingsPathFor derives the settings document from the credential file in use,
// for the same reason StatePathFor does: resolving it from the running user's
// home directory would let the menu describe one installation while the poller
// queries another.
func SettingsPathFor(credentialPath string) (string, error) {
	trimmed := strings.TrimSpace(credentialPath)
	if trimmed == "" {
		return "", errors.New("credential path is empty; the CLI settings document cannot be located")
	}
	// <home>/.claude/.credentials.json -> <home>/.claude/settings.json
	return filepath.Join(filepath.Dir(trimmed), settingsFileName), nil
}

// settingsDocument records the two fields this app reads. The same document also
// carries permission rules, hook commands and an env block whose values can be
// API keys for other services; decoding into this narrow struct is what keeps all
// of that out of memory beyond the raw read, and no error path here ever quotes
// the document.
type settingsDocument struct {
	Model       string `json:"model"`
	EffortLevel string `json:"effortLevel"`
}

// ParsePreferences validates settings-document bytes.
func ParsePreferences(raw []byte) (*Preferences, error) {
	var doc settingsDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode CLI settings document: %s", jsonReason(err))
	}
	prefs := &Preferences{
		Model:       strings.TrimSpace(doc.Model),
		EffortLevel: strings.TrimSpace(doc.EffortLevel),
	}
	if prefs.Model == "" && prefs.EffortLevel == "" {
		return nil, ErrNoPreferences
	}
	return prefs, nil
}

// LoadPreferences reads and parses the settings document at path. A missing file
// yields an error wrapping os.ErrNotExist, which is the ordinary case for a CLI
// that has never had a setting changed.
func LoadPreferences(path string) (*Preferences, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CLI settings document %q: %w", path, err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CLI settings document %q: %w", path, err)
	}
	if len(raw) > maxSettingsBytes {
		return nil, fmt.Errorf("CLI settings document %q exceeds the %d byte limit", path, maxSettingsBytes)
	}
	return ParsePreferences(raw)
}
