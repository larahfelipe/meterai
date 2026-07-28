// Package config holds the user-editable settings. The format is JSON in the
// OS config directory: it needs no third-party parser, and the file is small
// enough that JSON's lack of comments costs less than a dependency whose
// version has to be tracked for a document this small.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

const (
	// appDirName is the per-user directory under the OS config root:
	// %APPDATA%\meterAI on Windows, $XDG_CONFIG_HOME/meterAI elsewhere.
	appDirName = "meterAI"
	fileName   = "config.json"

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

// ProviderConfig is one vendor's settings. It holds only what every provider
// needs regardless of shape; anything vendor-specific stays out of this
// package entirely.
type ProviderConfig struct {
	// CredentialPath pins that vendor's CLI credential file. Empty means
	// autodiscovery. When set, it is authoritative: discovery will not fall
	// back to another location, so the app cannot silently monitor a different
	// account than the one pinned here.
	CredentialPath string `json:"credentialPath"`
}

// Config is the complete settings document.
type Config struct {
	// Providers is keyed by the vendor key each provider's quota.MeterID
	// already uses as a prefix ("anthropic", "openai"). Adding a provider is
	// wiring a new key here and in cmd/meterai/main.go — never a change to
	// this struct.
	Providers map[string]ProviderConfig `json:"providers"`
	// PollInterval is the steady-state cadence. Values below the safe minimum
	// are rejected rather than silently corrected, so a user who sets 10s finds
	// out why it will not happen.
	PollInterval Duration `json:"pollInterval"`
	// UsageAlerts is the local escalation policy: where a reading starts being
	// a warning, and where it becomes critical. It is nested rather than flat
	// because it is the first of what the menu presents as one group of
	// consumption settings, and a group in the UI reads better as a group in
	// the document a user edits by hand.
	UsageAlerts quota.Thresholds `json:"usageAlerts"`
	// Language is a BCP 47 tag with a catalogue in internal/i18n. Empty means
	// the default catalogue, so a document written before this field existed
	// keeps working.
	Language string `json:"language"`
}

// Default returns the settings used when no file exists yet.
func Default() Config {
	return Config{
		Providers:    map[string]ProviderConfig{},
		PollInterval: Duration(poll.DefaultInterval),
		UsageAlerts:  quota.DefaultThresholds(),
		Language:     string(i18n.DefaultLang),
	}
}

// anthropicProviderKey is the vendor key predating this struct's Providers
// map, used only to migrate the flat credentialPath a pre-multi-provider
// document carried. internal/provider/anthropic.VendorKey has the same value;
// it is not imported from there to keep this package free of a provider
// dependency for one literal.
const anthropicProviderKey = "anthropic"

// UnmarshalJSON fills the document, preserving whatever the receiver already
// holds for keys the file omits — which is what lets Load seed it with the
// defaults and have a partial document validate.
//
// It also accepts two shapes written before this release: the flat
// usageAlerts pair, and the single top-level credentialPath from before
// Providers existed. Ignoring either would silently revert a hand-tuned
// setting to the defaults on upgrade, which is exactly the kind of change to
// a user's settings this app must not make without saying so.
func (c *Config) UnmarshalJSON(raw []byte) error {
	// document has Config's fields but not its method set, so unmarshalling
	// into it cannot recurse back into here.
	type document Config
	doc := document(*c)
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	*c = Config(doc)

	var legacy struct {
		UsageAlerts       *json.RawMessage `json:"usageAlerts"`
		WarnAtPercent     *float64         `json:"warnAtPercent"`
		CriticalAtPercent *float64         `json:"criticalAtPercent"`
		Providers         *json.RawMessage `json:"providers"`
		CredentialPath    *string          `json:"credentialPath"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return err
	}

	// The current shape wins outright when present, so a document carrying both
	// resolves to the one this release writes rather than to a merge of the two.
	if legacy.Providers == nil && legacy.CredentialPath != nil {
		if c.Providers == nil {
			c.Providers = map[string]ProviderConfig{}
		}
		c.Providers[anthropicProviderKey] = ProviderConfig{CredentialPath: *legacy.CredentialPath}
	}

	if legacy.UsageAlerts != nil {
		return nil
	}
	if legacy.WarnAtPercent != nil {
		c.UsageAlerts.WarnAtPercent = *legacy.WarnAtPercent
	}
	if legacy.CriticalAtPercent != nil {
		c.UsageAlerts.CriticalAtPercent = *legacy.CriticalAtPercent
	}
	return nil
}

// Catalog resolves the language into the catalogue the UI renders from. Validate
// is what rejects an unsupported tag; here an unresolvable value degrades to the
// default catalogue, because a language nobody can read is still better than a
// UI that refuses to draw.
func (c Config) Catalog() *i18n.Catalog {
	lang, err := i18n.Parse(c.Language)
	if err != nil {
		lang = i18n.DefaultLang
	}
	return i18n.For(lang)
}

// Validate reports why a document cannot be used. Every failure names the field
// and the bound it violated, because the user fixes this by hand in an editor.
func (c Config) Validate() error {
	if interval := time.Duration(c.PollInterval); interval < poll.DefaultInterval {
		return fmt.Errorf("pollInterval is %s; the minimum safe cadence against these undocumented endpoints is %s",
			interval, poll.DefaultInterval)
	}
	if err := c.UsageAlerts.Validate(); err != nil {
		return fmt.Errorf("usageAlerts: %w", err)
	}
	if _, err := i18n.Parse(c.Language); err != nil {
		return fmt.Errorf("language is invalid: %w", err)
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
	// The rename is atomic with respect to the directory, but only a file whose
	// contents already reached the disk makes that atomicity mean anything:
	// without this, a crash between the rename and the writeback can leave the
	// entry pointing at a zero-length file, which is the truncated document this
	// whole path exists to make impossible.
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("flush temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
