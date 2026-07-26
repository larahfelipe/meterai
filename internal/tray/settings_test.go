package tray

import (
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
)

func TestIntervalPresetsStartAtTheFloorAndAscend(t *testing.T) {
	presets := IntervalPresets()
	if len(presets) == 0 {
		t.Fatal("no presets offered")
	}
	if presets[0] != poll.DefaultInterval {
		t.Errorf("presets[0] = %v, want the %v floor", presets[0], poll.DefaultInterval)
	}
	for i, preset := range presets {
		// Offering a cadence config.Validate rejects would present the user with a
		// choice that cannot be saved.
		if _, err := WithInterval(config.Default(), preset); err != nil {
			t.Errorf("preset %v is not a valid setting: %v", preset, err)
		}
		if i > 0 && preset <= presets[i-1] {
			t.Errorf("presets are not ascending: %v after %v", preset, presets[i-1])
		}
	}
}

func TestIntervalPresetsCannotBeMutatedThroughTheReturnedSlice(t *testing.T) {
	presets := IntervalPresets()
	presets[0] = time.Nanosecond
	if again := IntervalPresets(); again[0] != poll.DefaultInterval {
		t.Errorf("the preset table was mutated through a caller's slice: %v", again[0])
	}
}

func TestIntervalLabelNamesWholeUnits(t *testing.T) {
	english := presenterFor(t, i18n.LangEnUS)
	for interval, want := range map[time.Duration]string{
		5 * time.Minute:  "5 min",
		10 * time.Minute: "10 min",
		90 * time.Minute: "90 min",
		time.Hour:        "1 h",
		2 * time.Hour:    "2 h",
	} {
		if got := english.IntervalLabel(interval); got != want {
			t.Errorf("IntervalLabel(%v) = %q, want %q", interval, got, want)
		}
	}

	// A cadence that is neither whole minutes nor whole hours must not be rounded
	// into a label that misstates it.
	for _, odd := range []time.Duration{90 * time.Second, 0, -time.Minute} {
		if got := english.IntervalLabel(odd); got != odd.String() {
			t.Errorf("IntervalLabel(%v) = %q, want the exact %q", odd, got, odd.String())
		}
	}
}

func TestIntervalLabelIsLocalized(t *testing.T) {
	// Both catalogues abbreviate the same way today; the assertion exists so a
	// future translation that does not still renders through the catalogue.
	for _, lang := range i18n.Available() {
		if got := presenterFor(t, lang).IntervalLabel(15 * time.Minute); got == "" {
			t.Errorf("%s: IntervalLabel returned nothing", lang)
		}
	}
}

// A settings row states the value in force beside its name, so nothing has to be
// opened to read it. The submenu is only needed to change one.
func TestSettingsRowsCarryTheValueInForce(t *testing.T) {
	cfg := config.Default()
	cfg.PollInterval = config.Duration(30 * time.Minute)
	cfg.Language = string(i18n.LangPtBR)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	presenter := NewPresenter(cfg)

	interval := presenter.IntervalRow()
	if interval.Label != "Atualizar a cada" || interval.Detail != "30 min" {
		t.Errorf("interval row = %+v", interval)
	}
	language := presenter.LanguageRow()
	if language.Label != "Idioma" || language.Detail != "Português (BR)" {
		t.Errorf("language row = %+v", language)
	}
	// A language is named by its own endonym in every catalogue, so the row a user
	// who cannot read the current interface looks for still names theirs.
	for _, lang := range i18n.Available() {
		if got := presenterFor(t, lang).LanguageRow().Detail; got != lang.NativeName() {
			t.Errorf("%s: language row value = %q, want the endonym %q", lang, got, lang.NativeName())
		}
	}
}

// The value belongs in the column every other row puts its value in, which is
// what the tab selects. A row that put it on the left would be the one row in the
// menu that does not align.
func TestSettingsRowsUseTheValueColumn(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for _, row := range []Row{presenter.IntervalRow(), presenter.LanguageRow()} {
		title := MenuRowTitle(row)
		if strings.Count(title, menuRightAlign) != 1 {
			t.Errorf("title %q must carry exactly one column break", title)
		}
		if _, right, _ := strings.Cut(title, menuRightAlign); right != row.Detail {
			t.Errorf("right of the break = %q, want the value %q", right, row.Detail)
		}
	}
}

// Every cadence the menu offers has to be nameable: a preset whose row rendered a
// raw Go duration would be the one row in the menu written by the compiler.
func TestEverySettingsValueIsNameable(t *testing.T) {
	for _, preset := range IntervalPresets() {
		for _, lang := range i18n.Available() {
			cfg := config.Default()
			cfg.PollInterval = config.Duration(preset)
			cfg.Language = string(lang)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("%v in %s: %v", preset, lang, err)
			}
			if got := NewPresenter(cfg).IntervalRow().Detail; got == preset.String() {
				t.Errorf("%s: cadence %v renders as its Go duration %q", lang, preset, got)
			}
		}
	}
}

func TestWithIntervalRejectsWhatValidateRejects(t *testing.T) {
	base := config.Default()
	tooFast := poll.DefaultInterval - time.Second

	changed, err := WithInterval(base, tooFast)
	if err == nil {
		t.Fatalf("WithInterval accepted %v", tooFast)
	}
	// The rejected value must not reach the returned document: the caller persists
	// what it gets back.
	if changed != base {
		t.Errorf("WithInterval returned a modified config alongside an error: %+v", changed)
	}
	if got := time.Duration(changed.PollInterval); got != poll.DefaultInterval {
		t.Errorf("PollInterval = %v, want the original", got)
	}
}

func TestWithIntervalKeepsEveryOtherField(t *testing.T) {
	base := config.Default()
	base.CredentialPath = "/pinned/.claude/.credentials.json"
	base.Language = string(i18n.LangPtBR)
	base.WarnAtPercent, base.CriticalAtPercent = 60, 80

	changed, err := WithInterval(base, 30*time.Minute)
	if err != nil {
		t.Fatalf("WithInterval: %v", err)
	}
	if time.Duration(changed.PollInterval) != 30*time.Minute {
		t.Errorf("PollInterval = %v", time.Duration(changed.PollInterval))
	}
	want := base
	want.PollInterval = changed.PollInterval
	if changed != want {
		t.Errorf("WithInterval changed more than the cadence: %+v", changed)
	}
}

func TestWithLanguageAcceptsEverySupportedTag(t *testing.T) {
	base := config.Default()
	for _, lang := range i18n.Available() {
		changed, err := WithLanguage(base, lang)
		if err != nil {
			t.Fatalf("WithLanguage(%s): %v", lang, err)
		}
		if changed.Language != string(lang) {
			t.Errorf("Language = %q, want %q", changed.Language, lang)
		}
		if changed.Catalog().Lang() != lang {
			t.Errorf("Catalog().Lang() = %q, want %q", changed.Catalog().Lang(), lang)
		}
		want := base
		want.Language = changed.Language
		if changed != want {
			t.Errorf("WithLanguage changed more than the language: %+v", changed)
		}
	}
}

func TestWithLanguageRejectsAnUnsupportedTag(t *testing.T) {
	base := config.Default()
	changed, err := WithLanguage(base, "de-DE")
	if err == nil {
		t.Fatalf("WithLanguage accepted de-DE as %+v", changed)
	}
	if changed != base {
		t.Errorf("WithLanguage returned a modified config alongside an error: %+v", changed)
	}
}

func TestConfigRoundTripsThroughThePresenter(t *testing.T) {
	// The platform layer derives a changed document from this, so it must be the
	// document the Presenter actually renders with.
	cfg := config.Default()
	cfg.Language = string(i18n.LangPtBR)
	cfg.WarnAtPercent = 55
	if got := NewPresenter(cfg).Config(); got != cfg {
		t.Errorf("Config() = %+v, want %+v", got, cfg)
	}
}
