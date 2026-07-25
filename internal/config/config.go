// Package config holds the user-editable settings. The format is JSON in the
// OS config directory: it needs no third-party parser, and the file is small
// enough that JSON's lack of comments costs less than a dependency whose
// version has to be tracked for a five-field document.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"meterAI/internal/poll"
	"meterAI/internal/quota"
)

const (
	// appDirName is the per-user directory under the OS config root:
	// %APPDATA%\meterAI on Windows, $XDG_CONFIG_HOME/meterAI elsewhere.
	appDirName = "meterAI"
	fileName   = "config.json"

	// defaultWarnPercent and defaultCriticalPercent are local thresholds
	// applied on top of the vendor's own severity. They exist because vendors
	// report "normal" well past the point a user wants warning: the observed
	// Anthropic response called 74% of the weekly allowance normal.
	defaultWarnPercent     = 75.0
	defaultCriticalPercent = 90.0

	// percentCeiling bounds threshold configuration. A threshold above 100
	// could never fire, which is a silent misconfiguration.
	percentCeiling = 100.0

	// configFileMode keeps the file readable only by its owner. It holds no
	// secret today, but it does hold a path into the user's credential store.
	configFileMode = 0o600
	configDirMode  = 0o700
)

// Duration wraps time.Duration so the config file carries "5m" rather than a
// nanosecond count, which is unreadable when edited by hand.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("duration must be a string such as \"5m\": %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// Config is the complete settings document.
type Config struct {
	// CredentialPath pins the Claude CLI credential file. Empty means
	// autodiscovery. When set, it is authoritative: discovery will not fall
	// back to another location, so the app cannot silently monitor a different
	// account than the one pinned here.
	CredentialPath string `json:"credentialPath"`
	// PollInterval is the steady-state cadence. Values below the safe minimum
	// are rejected rather than silently corrected, so a user who sets 10s finds
	// out why it will not happen.
	PollInterval Duration `json:"pollInterval"`
	// WarnAtPercent and CriticalAtPercent escalate the tray icon locally.
	WarnAtPercent     float64 `json:"warnAtPercent"`
	CriticalAtPercent float64 `json:"criticalAtPercent"`
}

// Default returns the settings used when no file exists yet.
func Default() Config {
	return Config{
		PollInterval:      Duration(poll.DefaultInterval),
		WarnAtPercent:     defaultWarnPercent,
		CriticalAtPercent: defaultCriticalPercent,
	}
}

// SeverityFor combines the vendor's own assessment with the local thresholds,
// taking whichever is more severe. The vendor is never overruled downward: if
// Anthropic says critical, the icon says critical regardless of local settings.
func (c Config) SeverityFor(percent float64, vendor quota.Severity) quota.Severity {
	local := quota.SeverityNormal
	switch {
	case percent >= c.CriticalAtPercent:
		local = quota.SeverityCritical
	case percent >= c.WarnAtPercent:
		local = quota.SeverityWarning
	}
	if vendor > local {
		return vendor
	}
	return local
}

// Validate reports why a document cannot be used. Every failure names the field
// and the bound it violated, because the user fixes this by hand in an editor.
func (c Config) Validate() error {
	if interval := time.Duration(c.PollInterval); interval < poll.DefaultInterval {
		return fmt.Errorf("pollInterval is %s; the minimum safe cadence against these undocumented endpoints is %s",
			interval, poll.DefaultInterval)
	}
	if c.WarnAtPercent <= 0 || c.WarnAtPercent > percentCeiling {
		return fmt.Errorf("warnAtPercent is %v; it must be in (0,%v]", c.WarnAtPercent, percentCeiling)
	}
	if c.CriticalAtPercent <= 0 || c.CriticalAtPercent > percentCeiling {
		return fmt.Errorf("criticalAtPercent is %v; it must be in (0,%v]", c.CriticalAtPercent, percentCeiling)
	}
	if c.CriticalAtPercent < c.WarnAtPercent {
		return fmt.Errorf("criticalAtPercent (%v) is below warnAtPercent (%v); the critical threshold would be unreachable",
			c.CriticalAtPercent, c.WarnAtPercent)
	}
	return nil
}

// DefaultPath returns the config file location for this user.
func DefaultPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the OS config directory: %w", err)
	}
	return filepath.Join(root, appDirName, fileName), nil
}

// Load reads the config at path, creating it with defaults if it does not
// exist. A file that exists but cannot be parsed is an error: overwriting it
// would destroy settings the user meant to keep.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		defaults := Default()
		if writeErr := Save(path, defaults); writeErr != nil {
			// The app can still run on defaults; the caller decides whether an
			// unwritable config directory is fatal.
			return defaults, fmt.Errorf("write default config: %w", writeErr)
		}
		return defaults, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	// Start from defaults so a document written by an older release, missing
	// fields added since, still validates.
	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config atomically: a crash mid-write leaves the previous file
// intact rather than a truncated document that fails to parse on next start.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), configDirMode); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	encoded = append(encoded, '\n')

	// The temporary file must share a directory with the target so the rename
	// stays within one filesystem and is therefore atomic.
	temp, err := os.CreateTemp(filepath.Dir(path), fileName+".tmp*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		// No-op once the rename succeeded; cleans up every failure path.
		_ = os.Remove(tempName)
	}()

	if err := temp.Chmod(configFileMode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
