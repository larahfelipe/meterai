package identity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsPathSitsBesideTheCredential(t *testing.T) {
	// The settings document is inside the .claude directory, unlike the state
	// document, which is beside it.
	for credential, want := range map[string]string{
		filepath.Join("/home/u", ".claude", ".credentials.json"): filepath.Join("/home/u", ".claude", "settings.json"),
		filepath.Join(`\\wsl.localhost\Ubuntu\home\u`, ".claude", ".credentials.json"): filepath.Join(
			`\\wsl.localhost\Ubuntu\home\u`, ".claude", "settings.json"),
		filepath.Join("/pinned", "elsewhere", "creds.json"): filepath.Join("/pinned", "elsewhere", "settings.json"),
	} {
		got, err := SettingsPathFor(credential)
		if err != nil {
			t.Fatalf("SettingsPathFor(%q): %v", credential, err)
		}
		if got != want {
			t.Errorf("SettingsPathFor(%q) = %q, want %q", credential, got, want)
		}
	}
}

// A path derived from an unresolved credential would name the running user's own
// home directory, describing an installation that is not the one being polled.
func TestSettingsPathRejectsAnEmptyCredentialPath(t *testing.T) {
	for _, empty := range []string{"", "   ", "\t"} {
		if got, err := SettingsPathFor(empty); err == nil {
			t.Errorf("SettingsPathFor(%q) = %q, want an error", empty, got)
		}
	}
}

func TestParsePreferencesReadsTheTwoConfiguredFields(t *testing.T) {
	prefs, err := ParsePreferences([]byte(`{"model":"opus","effortLevel":"high","theme":"dark"}`))
	if err != nil {
		t.Fatalf("ParsePreferences: %v", err)
	}
	// Verbatim: this is configuration the user wrote, and the row exists so it can
	// be compared against the file.
	if prefs.Model != "opus" || prefs.EffortLevel != "high" {
		t.Errorf("preferences = %+v", prefs)
	}
}

func TestParsePreferencesAcceptsEitherFieldAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		raw           string
		model, effort string
	}{
		"model only":  {`{"model":"sonnet"}`, "sonnet", ""},
		"effort only": {`{"effortLevel":"medium"}`, "", "medium"},
		"padded":      {`{"model":"  opus  ","effortLevel":"\thigh\n"}`, "opus", "high"},
	} {
		t.Run(name, func(t *testing.T) {
			prefs, err := ParsePreferences([]byte(tc.raw))
			if err != nil {
				t.Fatalf("ParsePreferences: %v", err)
			}
			if prefs.Model != tc.model || prefs.EffortLevel != tc.effort {
				t.Errorf("preferences = %+v, want %q/%q", prefs, tc.model, tc.effort)
			}
		})
	}
}

// A CLI left on its defaults writes neither field, which is not a failure: it is
// the reason the rows hide.
func TestParsePreferencesReportsADocumentThatDeclaresNeither(t *testing.T) {
	for name, raw := range map[string]string{
		"empty object":   `{}`,
		"other settings": `{"theme":"dark","autoUpdatesChannel":"stable"}`,
		"blank values":   `{"model":"","effortLevel":"   "}`,
		"null values":    `{"model":null,"effortLevel":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePreferences([]byte(raw)); !errors.Is(err, ErrNoPreferences) {
				t.Errorf("err = %v, want ErrNoPreferences", err)
			}
		})
	}
}

func TestParsePreferencesRejectsAMalformedDocument(t *testing.T) {
	for name, raw := range map[string]string{
		"truncated":     `{"model":"opus"`,
		"not an object": `["opus"]`,
		"wrong type":    `{"model":{"name":"opus"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePreferences([]byte(raw)); err == nil {
				t.Errorf("ParsePreferences(%s) accepted a malformed document", raw)
			}
		})
	}
}

// The settings document holds the user's permission rules, hook commands and an
// env block whose values can be credentials for other services. A decoding
// failure must report structure only, never a fragment of any of it.
func TestPreferenceFailuresNeverQuoteTheDocument(t *testing.T) {
	const secret = "sk-do-not-echo-this"
	documents := []string{
		`{"env":{"OTHER_API_KEY":"` + secret + `"},"model":{"nested":true}}`,
		`{"env":{"OTHER_API_KEY":"` + secret + `"},"model":`,
		`{"permissions":{"allow":["Bash(` + secret + `)"]},"effortLevel":[1]}`,
	}
	for _, raw := range documents {
		_, err := ParsePreferences([]byte(raw))
		if err == nil {
			t.Fatalf("ParsePreferences(%s) accepted the document", raw)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error message leaked the document: %v", err)
		}
	}
}

func TestLoadPreferencesReadsAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"opus","effortLevel":"high"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prefs, err := LoadPreferences(path)
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if prefs.Model != "opus" || prefs.EffortLevel != "high" {
		t.Errorf("preferences = %+v", prefs)
	}
}

// A CLI that has never had a setting changed has no such file, which is the
// ordinary case and must be distinguishable from an unreadable one.
func TestLoadPreferencesReportsAMissingFile(t *testing.T) {
	_, err := LoadPreferences(filepath.Join(t.TempDir(), "settings.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want one wrapping os.ErrNotExist", err)
	}
}

// A directory opens but does not read, which is the failure a settings path can
// hit when the CLI's layout is not what this app assumes.
func TestLoadPreferencesRejectsADirectory(t *testing.T) {
	if _, err := LoadPreferences(t.TempDir()); err == nil {
		t.Error("LoadPreferences accepted a directory")
	}
}

// An undocumented producer can always grow its output, so the read is bounded
// rather than trusted.
func TestLoadPreferencesRejectsAnOversizeDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	padding := strings.Repeat("p", maxSettingsBytes)
	if err := os.WriteFile(path, []byte(`{"model":"opus","pad":"`+padding+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPreferences(path); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("err = %v, want the size limit to be reported", err)
	}
}

func TestCachePreferencesFollowTheCredentialPath(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	source := &stubPathSource{}
	cache := NewCache(source)

	// Before the first successful poll there is no credential path, and that is
	// not a failure.
	if prefs, err := cache.Preferences(); prefs != nil || err != nil {
		t.Errorf("Preferences() = (%+v, %v), want (nil, nil) while the path is unknown", prefs, err)
	}

	source.path = filepath.Join(claude, ".credentials.json")
	prefs, err := cache.Preferences()
	if err != nil {
		t.Fatalf("Preferences: %v", err)
	}
	if prefs.Model != "opus" {
		t.Errorf("preferences = %+v", prefs)
	}

	// The document is re-read rather than held: the user can change it at any
	// moment, and a value cached for the life of the process would go on
	// contradicting the file.
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet","effortLevel":"low"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := cache.Preferences()
	if err != nil {
		t.Fatalf("Preferences: %v", err)
	}
	if again.Model != "sonnet" || again.EffortLevel != "low" {
		t.Errorf("preferences = %+v, want the rewritten document", again)
	}
}

// A credential path that is present but unusable must surface as an error rather
// than be resolved against the running user's own home directory.
func TestCachePreferencesRejectAnUnusableCredentialPath(t *testing.T) {
	cache := NewCache(&stubPathSource{path: "   "})
	prefs, err := cache.Preferences()
	if err == nil {
		t.Fatalf("Preferences() = %+v, want an error", prefs)
	}
	if prefs != nil {
		t.Errorf("Preferences() returned %+v alongside an error", prefs)
	}
}

type stubPathSource struct{ path string }

func (s *stubPathSource) Source() string { return s.path }
