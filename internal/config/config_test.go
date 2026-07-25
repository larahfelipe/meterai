package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if cfg.WarnAtPercent != defaultWarnPercent || cfg.CriticalAtPercent != defaultCriticalPercent {
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
			func(c *Config) { c.WarnAtPercent = 0 }, "warnAtPercent",
		},
		"warn threshold above the ceiling": {
			func(c *Config) { c.WarnAtPercent = 101 }, "warnAtPercent",
		},
		"critical threshold above the ceiling": {
			func(c *Config) { c.CriticalAtPercent = 150 }, "criticalAtPercent",
		},
		"critical below warn is unreachable": {
			func(c *Config) { c.WarnAtPercent, c.CriticalAtPercent = 90, 60 }, "unreachable",
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

func TestSeverityForCombinesVendorAndLocalThresholds(t *testing.T) {
	cfg := Default() // warn 75, critical 90
	cases := []struct {
		percent float64
		vendor  quota.Severity
		want    quota.Severity
	}{
		{10, quota.SeverityNormal, quota.SeverityNormal},
		{74, quota.SeverityNormal, quota.SeverityNormal},
		// The case that motivates local thresholds: the vendor called 75%
		// normal, the user wants a warning.
		{75, quota.SeverityNormal, quota.SeverityWarning},
		{89.9, quota.SeverityNormal, quota.SeverityWarning},
		{90, quota.SeverityNormal, quota.SeverityCritical},
		// The vendor is never overruled downward.
		{5, quota.SeverityCritical, quota.SeverityCritical},
		{80, quota.SeverityCritical, quota.SeverityCritical},
	}
	for _, tc := range cases {
		if got := cfg.SeverityFor(tc.percent, tc.vendor); got != tc.want {
			t.Errorf("SeverityFor(%v, %v) = %v, want %v", tc.percent, tc.vendor, got, tc.want)
		}
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
