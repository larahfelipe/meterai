package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

func TestLoadCreatesDefaultsWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", fileName)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != Default() {
		t.Errorf("Load returned %+v, want defaults %+v", cfg, Default())
	}
	// The file must be created so the user can discover the settings.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
	// Reloading must produce the same document, which proves the written form
	// round-trips through the parser.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded != cfg {
		t.Errorf("reload = %+v, want %+v", reloaded, cfg)
	}
}

func TestDefaultPathUsesTheXDGConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, appDirName, fileName); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathSurfacesAnUnresolvableConfigDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("DefaultPath must report an error when the OS config directory cannot be resolved")
	}
}

func TestLoadSurfacesAGenericReadErrorWithoutOverwriting(t *testing.T) {
	// A directory at the config path fails ReadFile with something other than
	// ErrNotExist; Load must report it rather than treat it as "absent" and
	// attempt to write defaults over it.
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a directory at the config path must be a read error")
	}
}

func TestLoadRejectsADocumentThatFailsValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte(`{"warnAtPercent": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("Load = %v, want an error naming the invalid document", err)
	}
}

func TestLoadSurfacesAWriteFailureWhenCreatingDefaults(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, fileName)

	cfg, err := Load(path)
	if err == nil {
		t.Fatal("an unwritable config directory must surface as an error, not a silent default")
	}
	if cfg != Default() {
		t.Errorf("Load must still return usable defaults alongside the write error, got %+v", cfg)
	}
}

func TestSaveFailsWhenTheParentPathIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, fileName) // blocker is a file, not a directory
	if err := Save(path, Default()); err == nil {
		t.Fatal("Save must fail when the parent path is not a directory")
	}
}

func TestSaveFailsWithoutLosingTheExistingDestinationOrLeakingATempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the destination makes the final rename fail;
	// an empty one could be silently replaced on some platforms.
	if err := os.WriteFile(filepath.Join(path, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Save(path, Default()); err == nil {
		t.Fatal("Save must fail rather than silently lose a directory at the target path")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != fileName {
			t.Errorf("stray temp file left behind after a failed Save: %q", e.Name())
		}
	}
}

func TestSaveWritesHumanReadableDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	cfg := Default()
	cfg.PollInterval = Duration(7 * time.Minute)
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"pollInterval": "7m0s"`) {
		t.Errorf("config file is not hand-editable:\n%s", raw)
	}
}

func TestLoadFillsMissingFieldsFromDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	// A document written by an older release that predates the thresholds.
	if err := os.WriteFile(path, []byte(`{"pollInterval":"10m"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if time.Duration(cfg.PollInterval) != 10*time.Minute {
		t.Errorf("PollInterval = %v", time.Duration(cfg.PollInterval))
	}
	if cfg.UsageAlerts != quota.DefaultThresholds() {
		t.Errorf("missing fields were not defaulted: %+v", cfg)
	}
}

func TestLoadRefusesToOverwriteAnUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	const original = `{"pollInterval": "5m",` // truncated by a crash
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("a malformed config must be an error")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Error("Load overwrote a file the user may still want to repair")
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		mutate  func(*Config)
		wantErr string
	}{
		"cadence below the safe minimum": {
			func(c *Config) { c.PollInterval = Duration(10 * time.Second) }, "pollInterval",
		},
		"warn threshold at zero": {
			func(c *Config) { c.UsageAlerts.WarnAtPercent = 0 }, "warnAtPercent",
		},
		"warn threshold above the ceiling": {
			func(c *Config) { c.UsageAlerts.WarnAtPercent = 101 }, "warnAtPercent",
		},
		"critical threshold above the ceiling": {
			func(c *Config) { c.UsageAlerts.CriticalAtPercent = 150 }, "criticalAtPercent",
		},
		"critical below warn is unreachable": {
			func(c *Config) { c.UsageAlerts = quota.Thresholds{WarnAtPercent: 90, CriticalAtPercent: 60} }, "unreachable",
		},
		// A threshold failure has to name the group it sits in as well as the
		// field, since the document nests them.
		"the group is named alongside the field": {
			func(c *Config) { c.UsageAlerts.WarnAtPercent = -1 }, "usageAlerts",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%+v must not validate", cfg)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name %q", err, tc.wantErr)
			}
		})
	}
	if err := Default().Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
	// The floor must track the poller's own constant, not a copy of it.
	atFloor := Default()
	atFloor.PollInterval = Duration(poll.DefaultInterval)
	if err := atFloor.Validate(); err != nil {
		t.Errorf("the exact minimum cadence must be accepted: %v", err)
	}
}

func TestDurationRejectsNonDurationJSON(t *testing.T) {
	for _, raw := range []string{`300`, `"5 minutes"`, `null`, `{}`} {
		var d Duration
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Errorf("Unmarshal(%s) must fail", raw)
		}
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// No temporary file may survive a successful write.
	for _, e := range entries {
		if e.Name() != fileName {
			t.Errorf("stray file left behind: %q", e.Name())
		}
	}
}

func TestDefaultLanguageIsTheDefaultCatalogue(t *testing.T) {
	cfg := Default()
	if cfg.Language != string(i18n.DefaultLang) {
		t.Errorf("Language = %q, want %q", cfg.Language, i18n.DefaultLang)
	}
	if got := cfg.Catalog().Lang(); got != i18n.DefaultLang {
		t.Errorf("Catalog().Lang() = %q, want %q", got, i18n.DefaultLang)
	}
}

func TestValidateAcceptsEverySupportedLanguage(t *testing.T) {
	for _, lang := range i18n.Available() {
		cfg := Default()
		cfg.Language = string(lang)
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with language %q: %v", lang, err)
		}
		if got := cfg.Catalog().Lang(); got != lang {
			t.Errorf("Catalog().Lang() = %q, want %q", got, lang)
		}
	}
}

func TestValidateAcceptsAnAbsentLanguage(t *testing.T) {
	// A document written before the field existed carries no language, and must
	// keep loading rather than becoming invalid on upgrade.
	cfg := Default()
	cfg.Language = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with no language: %v", err)
	}
	if got := cfg.Catalog().Lang(); got != i18n.DefaultLang {
		t.Errorf("Catalog().Lang() = %q, want the default %q", got, i18n.DefaultLang)
	}
}

func TestValidateRejectsAnUnsupportedLanguage(t *testing.T) {
	cfg := Default()
	cfg.Language = "de-DE"
	err := cfg.Validate()
	if !errors.Is(err, i18n.ErrUnsupportedLang) {
		t.Fatalf("Validate() error = %v, want ErrUnsupportedLang", err)
	}
	// The user edits this file by hand, so the message has to name the field and
	// the accepted values.
	if !strings.Contains(err.Error(), "language") || !strings.Contains(err.Error(), string(i18n.LangPtBR)) {
		t.Errorf("Validate() error = %q, does not guide the fix", err)
	}
}

func TestCatalogFallsBackWhenValidationWasSkipped(t *testing.T) {
	// Catalog must never be the thing that stops the UI from drawing, even for a
	// value Validate would have rejected.
	cfg := Default()
	cfg.Language = "klingon"
	if got := cfg.Catalog().Lang(); got != i18n.DefaultLang {
		t.Errorf("Catalog().Lang() = %q, want the default %q", got, i18n.DefaultLang)
	}
}

func TestLoadPreservesAnExplicitLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Default()
	want.Language = string(i18n.LangPtBR)
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Language != want.Language {
		t.Errorf("Language = %q, want %q", got.Language, want.Language)
	}
}

func TestSaveAndLoadRoundTripTheUsageAlerts(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	want := Default()
	want.UsageAlerts = quota.Thresholds{WarnAtPercent: 60, CriticalAtPercent: 85}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The user edits this file by hand, so the group has to be visible as a
	// group rather than as two keys that happen to be adjacent.
	if !strings.Contains(string(raw), `"usageAlerts": {`) {
		t.Errorf("thresholds are not written as one object:\n%s", raw)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoadMigratesTheFlatThresholdsOfEarlierReleases(t *testing.T) {
	cases := map[string]struct {
		document string
		want     quota.Thresholds
	}{
		"both thresholds carried over": {
			`{"pollInterval":"10m","warnAtPercent":60,"criticalAtPercent":85}`,
			quota.Thresholds{WarnAtPercent: 60, CriticalAtPercent: 85},
		},
		// A document that tuned one and left the other at the default must not
		// lose the one it set.
		"one threshold carried over, the other defaulted": {
			`{"warnAtPercent":50}`,
			quota.Thresholds{WarnAtPercent: 50, CriticalAtPercent: quota.DefaultThresholds().CriticalAtPercent},
		},
		// Once the current shape is present it is authoritative, so a file that
		// carries both resolves to what this release writes rather than to a
		// merge of two generations of the document.
		"the current shape wins over a stale flat pair": {
			`{"usageAlerts":{"warnAtPercent":55,"criticalAtPercent":65},"warnAtPercent":95,"criticalAtPercent":99}`,
			quota.Thresholds{WarnAtPercent: 55, CriticalAtPercent: 65},
		},
		// A partial current shape is still the current shape: the missing half
		// comes from the defaults, not from the legacy key beside it.
		"a partial current shape does not fall back to the flat pair": {
			`{"usageAlerts":{"warnAtPercent":55},"criticalAtPercent":99}`,
			quota.Thresholds{WarnAtPercent: 55, CriticalAtPercent: quota.DefaultThresholds().CriticalAtPercent},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), fileName)
			if err := os.WriteFile(path, []byte(tc.document), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.UsageAlerts != tc.want {
				t.Errorf("UsageAlerts = %+v, want %+v", cfg.UsageAlerts, tc.want)
			}
		})
	}
}

// Migration is a read, never a write: Load must not rewrite a file it could
// still parse, because the user may be running two releases against it.
func TestLoadDoesNotRewriteALegacyDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	const original = `{"warnAtPercent":60,"criticalAtPercent":85}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("Load rewrote the document:\n%s", raw)
	}
}

// A migrated pair goes through the same gate as any other: carrying an invalid
// one forward silently would put the app in a state its own menu cannot produce.
func TestLoadValidatesMigratedThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte(`{"warnAtPercent":95,"criticalAtPercent":60}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("an inverted legacy pair must not load")
	}
}

func TestUnmarshalRejectsMalformedThresholds(t *testing.T) {
	for _, raw := range []string{
		`{"usageAlerts":[]}`,
		`{"usageAlerts":{"warnAtPercent":"75"}}`,
		`{"warnAtPercent":"75"}`,
		`{"warnAtPercent":true}`,
	} {
		cfg := Default()
		if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
			t.Errorf("Unmarshal(%s) must fail, got %+v", raw, cfg)
		}
	}
}
