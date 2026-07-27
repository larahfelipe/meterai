package tray

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

// alertsOf is the pair under test throughout this file, and reading it through
// the presenter is what proves a change reached the object the UI renders from
// rather than only the document that was written.
func alertsOf(cfg config.Config) quota.Thresholds { return NewPresenter(cfg).Config().UsageAlerts }

func configWith(t *testing.T, warn, critical float64) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.UsageAlerts = quota.Thresholds{WarnAtPercent: warn, CriticalAtPercent: critical}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config %v/%v is invalid: %v", warn, critical, err)
	}
	return cfg
}

func TestAlertPresetsAscendWithoutRepeating(t *testing.T) {
	for name, presets := range map[string][]float64{"warning": WarnPresets(), "critical": CriticalPresets()} {
		t.Run(name, func(t *testing.T) {
			if len(presets) == 0 {
				t.Fatal("no presets offered")
			}
			for i, preset := range presets {
				if i > 0 && preset <= presets[i-1] {
					t.Errorf("presets are not ascending: %v after %v", preset, presets[i-1])
				}
				// A preset outside the bounds the document accepts would be a
				// menu entry that can only ever produce an error line.
				if err := (quota.Thresholds{WarnAtPercent: preset, CriticalAtPercent: preset}).Validate(); err != nil &&
					!strings.Contains(err.Error(), "unreachable") {
					t.Errorf("preset %v is outside the accepted bounds: %v", preset, err)
				}
			}
		})
	}
}

// Both submenus are cut from one list, so the two thresholds are chosen from one
// scale and are read against each other without a conversion.
func TestBothThresholdsAreOfferedTheSameScale(t *testing.T) {
	warn, critical := WarnPresets(), CriticalPresets()
	if len(warn) != len(critical) {
		t.Fatalf("the two lists are not the same list minus one end: %v and %v", warn, critical)
	}
	// The lists overlap on everything but their outer ends: warning drops the
	// top, critical drops the bottom.
	for i := range warn[1:] {
		if warn[i+1] != critical[i] {
			t.Fatalf("the lists diverge at %d: %v and %v", i, warn, critical)
		}
	}
	if critical[len(critical)-1] <= warn[len(warn)-1] {
		t.Errorf("the critical list must reach above the warning list: %v, %v", warn, critical)
	}
	if warn[0] >= critical[0] {
		t.Errorf("the warning list must start below the critical list: %v, %v", warn, critical)
	}
}

// Nothing can sit above the highest preset to keep a warning below it, and
// nothing below the lowest to keep a critical above it. Those two are therefore
// absent from the menus rather than present and rejected on click.
func TestNeitherMenuOffersAValueThatCannotBePaired(t *testing.T) {
	base := config.Default()
	for _, preset := range WarnPresets() {
		if _, err := WithWarnThreshold(base, preset); err != nil {
			t.Errorf("the warning menu offers %v, which cannot be set: %v", preset, err)
		}
	}
	for _, preset := range CriticalPresets() {
		if _, err := WithCriticalThreshold(base, preset); err != nil {
			t.Errorf("the critical menu offers %v, which cannot be set: %v", preset, err)
		}
	}

	// The two values the menus deliberately withhold are exactly the ones with
	// no room on the far side, and the transition reports them rather than
	// persisting a pair the next start would reject.
	top := CriticalPresets()[len(CriticalPresets())-1]
	if changed, err := WithWarnThreshold(base, top); err == nil {
		t.Errorf("WithWarnThreshold accepted the ceiling as %+v", changed.UsageAlerts)
	}
	bottom := WarnPresets()[0]
	if changed, err := WithCriticalThreshold(base, bottom); err == nil {
		t.Errorf("WithCriticalThreshold accepted the floor as %+v", changed.UsageAlerts)
	}
}

func TestPresetsCannotBeMutatedThroughTheReturnedSlice(t *testing.T) {
	first := WarnPresets()[0]
	WarnPresets()[0] = -1
	CriticalPresets()[0] = -1
	if again := WarnPresets(); again[0] != first {
		t.Errorf("the preset table was mutated through a caller's slice: %v", again[0])
	}
}

func TestWithWarnThresholdSetsTheChosenValue(t *testing.T) {
	base := configWith(t, 75, 90)
	changed, err := WithWarnThreshold(base, 60)
	if err != nil {
		t.Fatalf("WithWarnThreshold: %v", err)
	}
	if got := alertsOf(changed); got.WarnAtPercent != 60 || got.CriticalAtPercent != 90 {
		t.Errorf("UsageAlerts = %+v, want 60/90", got)
	}
}

func TestWithCriticalThresholdSetsTheChosenValue(t *testing.T) {
	base := configWith(t, 75, 90)
	changed, err := WithCriticalThreshold(base, 95)
	if err != nil {
		t.Fatalf("WithCriticalThreshold: %v", err)
	}
	if got := alertsOf(changed); got.WarnAtPercent != 75 || got.CriticalAtPercent != 95 {
		t.Errorf("UsageAlerts = %+v, want 75/95", got)
	}
}

// The choice the user made is always taken. The other threshold moves to the
// adjacent preset that keeps the pair valid — the least the app can do to a
// setting the user did not click.
func TestAThresholdSetPastTheOtherCarriesItAlong(t *testing.T) {
	cases := map[string]struct {
		warn, critical float64
		apply          func(config.Config) (config.Config, error)
		want           quota.Thresholds
	}{
		"warning set above critical pushes it up one preset": {
			75, 80,
			func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 90) },
			quota.Thresholds{WarnAtPercent: 90, CriticalAtPercent: 95},
		},
		"warning set equal to critical still pushes it": {
			50, 80,
			func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 80) },
			quota.Thresholds{WarnAtPercent: 80, CriticalAtPercent: 85},
		},
		"critical set below warning pulls it down one preset": {
			90, 95,
			func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 70) },
			quota.Thresholds{WarnAtPercent: 60, CriticalAtPercent: 70},
		},
		"critical set equal to warning still pulls it": {
			80, 95,
			func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 80) },
			quota.Thresholds{WarnAtPercent: 75, CriticalAtPercent: 80},
		},
		// The last position each threshold can hold, where the companion lands
		// on the very end of the scale.
		"warning at the top of its list reaches the ceiling": {
			50, 60,
			func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 95) },
			quota.Thresholds{WarnAtPercent: 95, CriticalAtPercent: 100},
		},
		"critical at the bottom of its list reaches the floor": {
			90, 95,
			func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 60) },
			quota.Thresholds{WarnAtPercent: 50, CriticalAtPercent: 60},
		},
		// A pair edited by hand into values no menu offers still resolves: the
		// companion snaps onto the scale rather than being nudged off it.
		"a hand-edited companion snaps onto the scale": {
			62, 63,
			func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 85) },
			quota.Thresholds{WarnAtPercent: 85, CriticalAtPercent: 90},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			changed, err := tc.apply(configWith(t, tc.warn, tc.critical))
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := alertsOf(changed); got != tc.want {
				t.Errorf("UsageAlerts = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A threshold that is already clear of the other is never moved: the rule fires
// only where the pair would otherwise invert.
func TestTheCompanionThresholdIsLeftAloneWhenItIsAlreadyClear(t *testing.T) {
	base := configWith(t, 75, 90)
	for _, tc := range []struct {
		name  string
		apply func(config.Config) (config.Config, error)
		want  quota.Thresholds
	}{
		{"warning lowered", func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 50) },
			quota.Thresholds{WarnAtPercent: 50, CriticalAtPercent: 90}},
		{"critical raised", func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 100) },
			quota.Thresholds{WarnAtPercent: 75, CriticalAtPercent: 100}},
		// Re-selecting the value already in force is a no-op, not a nudge of the
		// pair: a user reopening a submenu to confirm what is ticked must not
		// change anything by clicking it.
		{"warning reselected", func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 75) },
			quota.Thresholds{WarnAtPercent: 75, CriticalAtPercent: 90}},
		{"critical reselected", func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 90) },
			quota.Thresholds{WarnAtPercent: 75, CriticalAtPercent: 90}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := tc.apply(base)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := alertsOf(changed); got != tc.want {
				t.Errorf("UsageAlerts = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Every click the menu can produce, from every state the menu can be in. The
// pair that comes out is always one the next start will accept, which is what
// makes the rejection path unreachable through the UI.
func TestEveryOfferedChoiceFromEveryOfferedStateStaysValid(t *testing.T) {
	for _, startWarn := range WarnPresets() {
		for _, startCritical := range CriticalPresets() {
			if startCritical <= startWarn {
				continue // not a state the menu can be in
			}
			base := configWith(t, startWarn, startCritical)
			for _, chosen := range WarnPresets() {
				changed, err := WithWarnThreshold(base, chosen)
				if err != nil {
					t.Fatalf("warning %v from %v/%v: %v", chosen, startWarn, startCritical, err)
				}
				assertUsable(t, changed, "warning", chosen)
				if got := changed.UsageAlerts.WarnAtPercent; got != chosen {
					t.Errorf("warning %v from %v/%v landed on %v", chosen, startWarn, startCritical, got)
				}
			}
			for _, chosen := range CriticalPresets() {
				changed, err := WithCriticalThreshold(base, chosen)
				if err != nil {
					t.Fatalf("critical %v from %v/%v: %v", chosen, startWarn, startCritical, err)
				}
				assertUsable(t, changed, "critical", chosen)
				if got := changed.UsageAlerts.CriticalAtPercent; got != chosen {
					t.Errorf("critical %v from %v/%v landed on %v", chosen, startWarn, startCritical, got)
				}
			}
		}
	}
}

// assertUsable checks the two properties every persisted pair has to hold: the
// document validates, and the companion is a value its own menu can tick, so
// the menu can never show a threshold with nothing marked against it.
func assertUsable(t *testing.T, cfg config.Config, which string, chosen float64) {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Errorf("%s %v produced an unpersistable document: %v", which, chosen, err)
	}
	alerts := cfg.UsageAlerts
	if alerts.WarnAtPercent >= alerts.CriticalAtPercent {
		t.Errorf("%s %v produced the inverted pair %+v", which, chosen, alerts)
	}
	if !slices.Contains(WarnPresets(), alerts.WarnAtPercent) {
		t.Errorf("%s %v left the warning threshold at %v, which its menu cannot tick", which, chosen, alerts.WarnAtPercent)
	}
	if !slices.Contains(CriticalPresets(), alerts.CriticalAtPercent) {
		t.Errorf("%s %v left the critical threshold at %v, which its menu cannot tick", which, chosen, alerts.CriticalAtPercent)
	}
}

// A rejected change must leave the caller holding the document it started with:
// the platform layer persists whatever comes back.
func TestARejectedThresholdReturnsTheOriginalDocument(t *testing.T) {
	base := configWith(t, 75, 90)
	for _, tc := range []struct {
		name  string
		apply func(config.Config) (config.Config, error)
	}{
		{"a warning above every preset", func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 100) }},
		{"a warning past the ceiling", func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 140) }},
		{"a critical below every preset", func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 50) }},
		{"a critical at zero", func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 0) }},
		{"a negative warning", func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, -10) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := tc.apply(base)
			if err == nil {
				t.Fatalf("accepted, producing %+v", changed.UsageAlerts)
			}
			if changed != base {
				t.Errorf("returned a modified config alongside an error: %+v", changed)
			}
			// The user reads this message off the status line, so it has to name
			// the figure that was refused.
			if !strings.Contains(err.Error(), "%") {
				t.Errorf("error %q does not name the rejected figure", err)
			}
		})
	}
}

func TestAThresholdChangeKeepsEveryOtherSetting(t *testing.T) {
	base := configWith(t, 75, 90)
	base.CredentialPath = "/pinned/.claude/.credentials.json"
	base.Language = string(i18n.LangPtBR)

	for name, apply := range map[string]func(config.Config) (config.Config, error){
		"warning":  func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 60) },
		"critical": func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 95) },
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := apply(base)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			want := base
			want.UsageAlerts = changed.UsageAlerts
			if changed != want {
				t.Errorf("%s changed more than the thresholds: %+v", name, changed)
			}
		})
	}
}

// The cadence and the language transitions carry the thresholds untouched, and
// the threshold transitions carry the cadence: one settings document, one gate.
func TestTheSettingsTransitionsDoNotDisturbEachOther(t *testing.T) {
	base := configWith(t, 60, 85)
	changed, err := WithLanguage(base, i18n.LangPtBR)
	if err != nil {
		t.Fatalf("WithLanguage: %v", err)
	}
	if got := changed.UsageAlerts; got != base.UsageAlerts {
		t.Errorf("a language change moved the thresholds to %+v", got)
	}
	changed, err = WithWarnThreshold(changed, 70)
	if err != nil {
		t.Fatalf("WithWarnThreshold: %v", err)
	}
	if changed.Language != string(i18n.LangPtBR) || changed.PollInterval != base.PollInterval {
		t.Errorf("a threshold change disturbed another setting: %+v", changed)
	}
}

func TestUsageAlertRowsCarryTheValuesInForce(t *testing.T) {
	presenter := NewPresenter(configWith(t, 60, 85))
	for _, tc := range []struct {
		row  Row
		want string
	}{
		{presenter.UsageAlertsRow(), "60% • 85%"},
		{presenter.WarnThresholdRow(), "60%"},
		{presenter.CriticalThresholdRow(), "85%"},
	} {
		if tc.row.Detail != tc.want {
			t.Errorf("row %+v does not read %q", tc.row, tc.want)
		}
		if tc.row.Label == "" {
			t.Errorf("row %+v has no name on the left", tc.row)
		}
		// A settings row carries no gauge: a threshold is a boundary, not a
		// reading, and a bar beside it would imply consumption.
		if tc.row.Bar != "" {
			t.Errorf("row %+v draws a gauge", tc.row)
		}
	}
}

// The group row exists so the pair can be read without opening anything, which
// is only true if it states both figures in the order they escalate.
func TestTheUsageAlertsRowStatesBothThresholdsAscending(t *testing.T) {
	detail := NewPresenter(configWith(t, 50, 100)).UsageAlertsRow().Detail
	warn, critical, found := strings.Cut(detail, preferenceSeparator)
	if !found {
		t.Fatalf("the group row %q does not state two settings", detail)
	}
	if warn != "50%" || critical != "100%" {
		t.Errorf("the group row reads %q, want the warning before the critical", detail)
	}
}

// The figure is written the same way wherever it appears, so a threshold and the
// reading it classifies are compared by eye without a unit conversion.
func TestThresholdsAreWrittenLikeTheReadingsTheyClassify(t *testing.T) {
	meter := &quota.Utilization{MeterID: "anthropic:session", Percent: 75}
	presenter := NewPresenter(configWith(t, 75, 90))
	if got, want := presenter.WarnThresholdRow().Detail, presenter.figure(meter); got != want {
		t.Errorf("the threshold reads %q where the identical reading reads %q", got, want)
	}
}

// Every caption in the new submenus resolves through a catalogue, in every
// language: a literal reaching the menu is a defect, and a missing key renders
// as "!KeyName" rather than as text.
func TestUsageAlertCaptionsAreTranslatedInEveryLanguage(t *testing.T) {
	for _, lang := range i18n.Available() {
		cfg := configWith(t, 75, 90)
		cfg.Language = string(lang)
		presenter := NewPresenter(cfg)
		rows := map[string]Row{
			"group":    presenter.UsageAlertsRow(),
			"warning":  presenter.WarnThresholdRow(),
			"critical": presenter.CriticalThresholdRow(),
		}
		for name, row := range rows {
			if row.Label == "" || strings.HasPrefix(row.Label, "!") {
				t.Errorf("%s: the %s row has no catalogue entry: %q", lang, name, row.Label)
			}
		}
		if rows["warning"].Label == rows["critical"].Label {
			t.Errorf("%s: the two thresholds share one caption %q", lang, rows["warning"].Label)
		}
	}
}

// The whole point of the setting: a change to the thresholds has to reach the
// icon on the very next render, with no poll in between.
func TestAThresholdChangeReclassifiesTheCurrentReadingImmediately(t *testing.T) {
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{
			MeterID: "anthropic:session", Name: "session", Percent: 80,
			Level: quota.SeverityNormal, IsActive: true,
		},
	}}})

	// 80% is normal under a 90/95 pair, a warning under the defaults, and
	// critical once the critical threshold drops below it — same snapshot, three
	// readings, driven only by the setting.
	for _, tc := range []struct {
		warn, critical float64
		want           quota.Severity
	}{
		{90, 95, quota.SeverityNormal},
		{75, 90, quota.SeverityWarning},
		{50, 60, quota.SeverityCritical},
	} {
		presenter := NewPresenter(configWith(t, tc.warn, tc.critical))
		percent, level, _ := presenter.IconState(sub)
		if percent != 80 {
			t.Errorf("the reading itself moved with the thresholds: %v", percent)
		}
		if level != tc.want {
			t.Errorf("%v/%v classified 80%% as %v, want %v", tc.warn, tc.critical, level, tc.want)
		}
	}
}

// The thresholds are local and additive: they escalate a vendor that under-
// reports, and never talk one down.
func TestConfiguredThresholdsNeverOverruleTheVendorDownward(t *testing.T) {
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{
			MeterID: "anthropic:session", Name: "session", Percent: 10,
			Level: quota.SeverityCritical, IsActive: true,
		},
	}}})
	// A pair that would call 10% normal, against a vendor that called it critical.
	_, level, _ := NewPresenter(configWith(t, 95, 100)).IconState(sub)
	if level != quota.SeverityCritical {
		t.Errorf("IconState = %v, want the vendor's own critical", level)
	}
}

// The whole loop the feature is: a choice made in the menu, persisted through
// Wiring.SaveSettings, reloaded on the next start, and classifying the same
// reading the same way on both sides of the restart.
func TestAThresholdChosenInTheMenuSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	loaded, err := config.Load(path) // first start: the file is written with defaults
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.UsageAlerts != quota.DefaultThresholds() {
		t.Fatalf("a fresh install must run on %+v, got %+v", quota.DefaultThresholds(), loaded.UsageAlerts)
	}

	// The user opens Usage alerts → Warning threshold and picks 60%, then
	// Critical threshold and picks 70%. Each choice is persisted before it is
	// applied, exactly as changeSettings does it.
	for _, apply := range []func(config.Config) (config.Config, error){
		func(c config.Config) (config.Config, error) { return WithWarnThreshold(c, 60) },
		func(c config.Config) (config.Config, error) { return WithCriticalThreshold(c, 70) },
	} {
		changed, err := apply(loaded)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := config.Save(path, changed); err != nil {
			t.Fatalf("Save: %v", err)
		}
		loaded = changed
	}

	restarted, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if restarted != loaded {
		t.Fatalf("reload = %+v, want the document the menu wrote %+v", restarted, loaded)
	}

	// 65% sits between the two thresholds the user chose and would have been
	// plain normal under the defaults; both the running app and the restarted
	// one now call it a warning.
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "anthropic:session", Percent: 65, Level: quota.SeverityNormal, IsActive: true},
	}}})
	if _, level, _ := NewPresenter(config.Default()).IconState(sub); level != quota.SeverityNormal {
		t.Fatalf("65%% under the defaults = %v, want normal", level)
	}
	for name, cfg := range map[string]config.Config{"before the restart": loaded, "after it": restarted} {
		if _, level, _ := NewPresenter(cfg).IconState(sub); level != quota.SeverityWarning {
			t.Errorf("%s: 65%% = %v, want the warning the user configured", name, level)
		}
	}
}

// A pair the menu produced by moving the companion is persisted and reloaded as
// the pair the menu shows, not as the one the user clicked half of.
func TestACarriedCompanionThresholdIsPersistedToo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	changed, err := WithWarnThreshold(configWith(t, 75, 80), 90)
	if err != nil {
		t.Fatalf("WithWarnThreshold: %v", err)
	}
	if err := config.Save(path, changed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded.UsageAlerts; got != (quota.Thresholds{WarnAtPercent: 90, CriticalAtPercent: 95}) {
		t.Errorf("reloaded %+v, want the carried critical alongside the chosen warning", got)
	}
	// And it lands where its own submenu can tick it, so the menu after the
	// restart is not showing a threshold with nothing marked.
	if !slices.Contains(CriticalPresets(), reloaded.UsageAlerts.CriticalAtPercent) {
		t.Errorf("the carried critical %v is not an offered preset", reloaded.UsageAlerts.CriticalAtPercent)
	}
}

// A failed write leaves the running configuration alone: changeSettings applies
// nothing when save fails, so the document the presenter holds must still be the
// one on disk.
func TestAThresholdIsNotAppliedWhenItCannotBePersisted(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	unwritable := filepath.Join(blocker, "config.json") // blocker is a file, not a directory

	running := configWith(t, 75, 90)
	changed, err := WithWarnThreshold(running, 50)
	if err != nil {
		t.Fatalf("WithWarnThreshold: %v", err)
	}
	if err := config.Save(unwritable, changed); err == nil {
		t.Fatal("the write must fail for this test to mean anything")
	}
	if got := alertsOf(running); got != (quota.Thresholds{WarnAtPercent: 75, CriticalAtPercent: 90}) {
		t.Errorf("the running configuration moved despite the failed write: %+v", got)
	}
}
