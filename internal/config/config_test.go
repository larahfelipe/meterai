package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meterAI/internal/poll"
	"meterAI/internal/quota"
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
