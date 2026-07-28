package tray

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/identity"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

var now = time.Date(2026, 7, 25, 18, 0, 0, 0, time.UTC)

// presenterFor builds a Presenter pinned to one language, so a rendering test
// asserts the text of a catalogue it names rather than whatever the default
// happens to be.
func presenterFor(t *testing.T, lang i18n.Lang) *Presenter {
	t.Helper()
	cfg := config.Default()
	cfg.Language = string(lang)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config with language %q is invalid: %v", lang, err)
	}
	return NewPresenter(cfg)
}

// liveState reproduces the shape of a real Anthropic poll: an inactive 5-hour
// session window plus the active weekly window, with the figures observed on
// 2026-07-25.
func liveState() poll.State {
	return poll.State{
		Snapshot: &quota.Snapshot{
			Vendor:  "anthropic",
			Product: "Claude",
			Plan:    "pro",
			Meters: []quota.Meter{
				&quota.Utilization{
					MeterID: "anthropic:session", Name: "session", Percent: 23,
					Reset: now.Add(2*time.Hour + 54*time.Minute), Level: quota.SeverityNormal,
				},
				&quota.Utilization{
					MeterID: "anthropic:weekly_all", Name: "weekly_all", Percent: 74,
					Reset: now.Add(25*time.Hour + 44*time.Minute), Level: quota.SeverityNormal,
					IsActive: true,
				},
			},
		},
		UpdatedAt:  now.Add(-90 * time.Second),
		NextPollAt: now.Add(3*time.Minute + 30*time.Second),
	}
}

var sampleAccount = &identity.Account{
	DisplayName: "Sample", Email: "sample@example.com", Organization: "Sample Org",
}

var samplePreferences = &identity.Preferences{Model: "opus", EffortLevel: "high"}

// live is one fully described subscription: a poll that succeeded, and both CLI
// documents read.
func live() Subscription {
	return Subscription{
		Vendor: "anthropic", State: liveState(),
		Account: sampleAccount, Preferences: samplePreferences,
	}
}

// metering wraps a bare snapshot state as a subscription, for the tests whose
// subject is the meter rows rather than the provider around them.
func metering(state poll.State) Subscription {
	return Subscription{Vendor: "anthropic", State: state}
}

func TestMeterRowsRenderMetersInProviderOrder(t *testing.T) {
	rows := presenterFor(t, i18n.LangPtBR).MeterRows(live(), now)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Label != "Sessão (5h) · reset em 2h54" || rows[0].Detail != "23%" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Label != "Semanal (7d) · reset em 1d01h" || rows[1].Detail != "74%" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestMeterRowsRenderInTheDefaultLanguage(t *testing.T) {
	rows := presenterFor(t, i18n.LangEnUS).MeterRows(live(), now)
	if rows[0].Label != "Session (5h) · resets in 2h54" || rows[0].Detail != "23%" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Label != "Weekly (7d) · resets in 1d01h" || rows[1].Detail != "74%" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

// The figure is the one field that changes between polls, so it is the only one
// that shares the right column with the gauge: a countdown or a window name there
// would move the number the row exists to show.
func TestMeterRowsKeepTheFigureAloneInTheValueColumn(t *testing.T) {
	for _, lang := range i18n.Available() {
		for _, row := range presenterFor(t, lang).MeterRows(live(), now) {
			if row.Detail == "" || strings.ContainsAny(row.Detail, "·") {
				t.Errorf("%s: value column = %q, want the figure alone", lang, row.Detail)
			}
			if !strings.Contains(row.Label, "·") {
				t.Errorf("%s: label = %q, want the reset countdown beside the name", lang, row.Label)
			}
		}
	}
}

// Anthropic reports every weekly window with one reset instant, so the countdown
// is stated by the first row of the group and the rest read as belonging to it.
func TestMeterRowsStateOneResetInstantOnce(t *testing.T) {
	weekly := now.Add(4*24*time.Hour + 6*time.Hour)
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "anthropic:session", Name: "session", Percent: 47,
			Reset: now.Add(2*time.Hour + 13*time.Minute)},
		&quota.Utilization{MeterID: "anthropic:weekly_all", Name: "weekly_all", Percent: 74, Reset: weekly},
		&quota.Utilization{MeterID: "anthropic:weekly_opus", Name: "weekly_opus", Percent: 4, Reset: weekly},
		&quota.Utilization{MeterID: "anthropic:weekly_sonnet", Name: "weekly_sonnet", Percent: 12, Reset: weekly},
	}}})

	rows := presenterFor(t, i18n.LangEnUS).MeterRows(sub, now)
	for _, want := range []string{
		"Session (5h) · resets in 2h13",
		"Weekly (7d) · resets in 4d06h",
		"Weekly Opus (7d)",
		"Weekly Sonnet (7d)",
	} {
		if got := rows[0].Label; got != want {
			t.Errorf("label = %q, want %q", got, want)
		}
		rows = rows[1:]
	}

	// Every figure survives the suppression: it is the countdown that is shared,
	// never the reading.
	for _, row := range presenterFor(t, i18n.LangEnUS).MeterRows(sub, now) {
		if row.Detail == "" {
			t.Errorf("row %q lost its figure", row.Label)
		}
	}
}

// Suppression follows the rows the user reads, not the set of instants in the
// document: a window that resets at the same time as one further up, with another
// window between them, has to say so.
func TestMeterRowsRepeatAResetThatIsNotConsecutive(t *testing.T) {
	shared := now.Add(3 * time.Hour)
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "a:first", Name: "first", Percent: 1, Reset: shared},
		&quota.Utilization{MeterID: "a:middle", Name: "middle", Percent: 2, Reset: now.Add(time.Hour)},
		&quota.Utilization{MeterID: "a:last", Name: "last", Percent: 3, Reset: shared},
	}}})

	for i, row := range presenterFor(t, i18n.LangEnUS).MeterRows(sub, now) {
		if !strings.Contains(row.Label, "resets in") {
			t.Errorf("row %d = %q, want its own countdown", i, row.Label)
		}
	}
}

// A meter with no reset instant at all must neither state one nor let the next row
// inherit its silence.
func TestMeterRowsAroundAMeterThatNeverResets(t *testing.T) {
	shared := now.Add(3 * time.Hour)
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "a:windowed", Name: "windowed", Percent: 1, Reset: shared},
		&quota.Balance{MeterID: "a:credits", Name: "credits",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2}},
		&quota.Utilization{MeterID: "a:again", Name: "again", Percent: 3, Reset: shared},
	}}})

	rows := presenterFor(t, i18n.LangEnUS).MeterRows(sub, now)
	if !strings.Contains(rows[0].Label, "resets in") {
		t.Errorf("row 0 = %q", rows[0].Label)
	}
	if rows[1].Label != "credits" {
		t.Errorf("row 1 = %q, want no countdown at all", rows[1].Label)
	}
	if !strings.Contains(rows[2].Label, "resets in") {
		t.Errorf("row 2 = %q, want its own countdown after a meter that has none", rows[2].Label)
	}
}

// A meter the vendor never resets, such as a continuously drawn balance, must not
// grow a dangling separator where its countdown would be.
func TestMeterRowsOmitTheResetOfAMeterThatHasNone(t *testing.T) {
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{MeterID: "openrouter:credits", Name: "credits",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2}},
	}}})
	row := presenterFor(t, i18n.LangEnUS).MeterRows(sub, now)[0]
	if row.Label != "credits" {
		t.Errorf("label = %q, want the bare name", row.Label)
	}
}

// A window the vendor introduces after this release has no catalogue entry, and
// must still reach the menu under the provider's own name.
func TestMeterRowsFallBackToTheProviderNameForAnUnknownMeter(t *testing.T) {
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "anthropic:monthly_opus", Name: "monthly_opus", Percent: 12},
	}}})
	for _, lang := range i18n.Available() {
		rows := presenterFor(t, lang).MeterRows(sub, now)
		if rows[0].Label != "monthly_opus" {
			t.Errorf("%s: label = %q, want the raw kind", lang, rows[0].Label)
		}
	}
}

func TestMeterRowsBeforeFirstPoll(t *testing.T) {
	if rows := presenterFor(t, i18n.LangEnUS).MeterRows(Subscription{}, now); rows != nil {
		t.Errorf("rows = %v, want nil before the first successful poll", rows)
	}
}

func TestMeterRowsRenderMoneyMeters(t *testing.T) {
	limit := quota.Money{AmountMinor: 5000, Currency: "USD", Exponent: 2}
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{
			MeterID: "anthropic:spend", Name: "spend",
			Used:  quota.Money{AmountMinor: 1234, Currency: "USD", Exponent: 2},
			Limit: &limit, Percent: 24.68,
		},
		&quota.Balance{
			MeterID: "openrouter:credits", Name: "credits",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2},
		},
	}}})
	rows := presenterFor(t, i18n.LangPtBR).MeterRows(sub, now)
	if rows[0].Detail != "12.34 USD de 50.00 USD" {
		t.Errorf("capped balance = %q", rows[0].Detail)
	}
	// An uncapped balance must not invent a limit or a percentage.
	if rows[1].Detail != "7.50 USD" {
		t.Errorf("uncapped balance = %q", rows[1].Detail)
	}
}

func TestFormatCountdown(t *testing.T) {
	presenter := presenterFor(t, i18n.LangPtBR)
	cases := map[time.Duration]string{
		-time.Hour:                      "instantes",
		0:                               "instantes",
		30 * time.Second:                "<1min",
		time.Minute:                     "1min",
		59*time.Minute + 59*time.Second: "59min",
		time.Hour:                       "1h00",
		2*time.Hour + 54*time.Minute:    "2h54",
		23*time.Hour + 59*time.Minute:   "23h59",
		24 * time.Hour:                  "1d00h",
		25*time.Hour + 44*time.Minute:   "1d01h",
		7*24*time.Hour + 3*time.Hour:    "7d03h",
	}
	for d, want := range cases {
		if got := presenter.countdown(d); got != want {
			t.Errorf("countdown(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestStatusTextByHealth(t *testing.T) {
	presenter := presenterFor(t, i18n.LangPtBR)
	healthy := presenter.StatusText(live(), now)
	if healthy != "Atualizado há 1min · próxima em 3min" {
		t.Errorf("healthy status = %q", healthy)
	}

	if got := presenter.StatusText(Subscription{}, now); got != "Consultando…" {
		t.Errorf("startup status = %q", got)
	}
}

func TestStatusTextInTheDefaultLanguage(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	if got, want := presenter.StatusText(live(), now), "Updated 1m ago · next in 3m"; got != want {
		t.Errorf("healthy status = %q, want %q", got, want)
	}
	if got, want := presenter.StatusText(Subscription{}, now), "Polling…"; got != want {
		t.Errorf("startup status = %q, want %q", got, want)
	}
}

func TestStatusTextNamesTheUserAction(t *testing.T) {
	presenter := presenterFor(t, i18n.LangPtBR)
	cases := map[quota.FetchErrorKind]string{
		quota.Unauthorized: "rode `claude`",
		quota.RateLimited:  "aguardando",
		quota.Transient:    "tentando novamente",
		quota.Protocol:     "atualizado",
	}
	for kind, want := range cases {
		sub := live()
		sub.State.LastError = &quota.FetchError{Kind: kind, RenewHint: "claude", Err: errors.New("x")}
		got := presenter.StatusText(sub, now)
		if !strings.Contains(got, want) {
			t.Errorf("status for %v = %q, does not mention %q", kind, got, want)
		}
		// A stale reading must say how old it is, so the user never reads an
		// outdated percentage as current.
		if !strings.Contains(got, "dados de 1min atrás") {
			t.Errorf("status for %v = %q, does not disclose staleness", kind, got)
		}
	}
}

func TestStatusTextForUntypedError(t *testing.T) {
	presenter := presenterFor(t, i18n.LangPtBR)
	sub := metering(poll.State{LastError: errors.New("boom")})
	got := presenter.StatusText(sub, now)
	if !strings.Contains(got, "Falha inesperada") {
		t.Errorf("status = %q", got)
	}
	// With no snapshot there is nothing to call stale.
	if strings.Contains(got, "dados de") {
		t.Errorf("status = %q, must not claim stale data when none was ever fetched", got)
	}
}

func TestHumanizeErrorDefaultsForAnUnrecognizedKind(t *testing.T) {
	// A FetchErrorKind this build does not recognize (e.g. added by a newer
	// vendor integration and not yet handled here) must still degrade to a
	// generic message rather than an empty or misleading one.
	err := &quota.FetchError{Kind: quota.FetchErrorKind(99), Err: errors.New("x")}
	if got := presenterFor(t, i18n.LangPtBR).humanizeError(err); got != "Falha ao consultar" {
		t.Errorf("humanizeError(unrecognized kind) = %q, want the generic fallback", got)
	}
}

// A poll that has just landed must not be described by composing a zero-length
// span into "%s ago": that produced "Atualizado há agora" and "Updated now ago".
func TestStatusTextReadsNaturallyRightAfterAPoll(t *testing.T) {
	justPolled := live()
	justPolled.State.UpdatedAt = now
	justPolled.State.NextPollAt = now.Add(5 * time.Minute)

	for lang, want := range map[i18n.Lang]string{
		i18n.LangEnUS: "Updated moments ago · next in 5m",
		i18n.LangPtBR: "Atualizado há instantes · próxima em 5min",
	} {
		got := presenterFor(t, lang).StatusText(justPolled, now)
		if got != want {
			t.Errorf("%s: status = %q, want %q", lang, got, want)
		}
	}
}

// The same composition appears in the staleness disclosure and in every countdown
// that has run out, so none of them may pair an instant with a span either.
func TestNoStatusLinePairsAnInstantWithASpan(t *testing.T) {
	overdue := live()
	overdue.State.UpdatedAt = now
	overdue.State.NextPollAt = now
	overdue.State.LastError = &quota.FetchError{Kind: quota.Transient, Err: errors.New("offline")}
	overdue.State.Snapshot.Meters[0].(*quota.Utilization).Reset = now

	for _, lang := range i18n.Available() {
		presenter := presenterFor(t, lang)
		texts := []string{presenter.StatusText(overdue, now), presenter.Tooltip(overdue, now)}
		for _, row := range presenter.MeterRows(overdue, now) {
			texts = append(texts, row.Label, row.Detail)
		}
		for _, text := range texts {
			for _, broken := range []string{"há agora", "now ago", "de agora atrás", "in now", "em agora"} {
				if strings.Contains(text, broken) {
					t.Errorf("%s: %q composes an instant into a span (%q)", lang, text, broken)
				}
			}
		}
	}
}

func TestIconStateTracksTheActiveWindow(t *testing.T) {
	cfg := config.Default() // warn 75, critical 90
	percent, level, stale := NewPresenter(cfg).IconState(live())
	// The weekly window is the active one, even though it is not first.
	if percent != 74 {
		t.Errorf("percent = %v, want the active window's 74", percent)
	}
	if level != quota.SeverityNormal {
		t.Errorf("level = %v, want normal just under the 75%% threshold", level)
	}
	if stale {
		t.Error("a healthy state must not render as stale")
	}
}

func TestIconStateEscalatesOnLocalThreshold(t *testing.T) {
	sub := live()
	sub.State.Snapshot.Meters[1].(*quota.Utilization).Percent = 91

	_, level, _ := NewPresenter(config.Default()).IconState(sub)
	if level != quota.SeverityCritical {
		t.Errorf("level = %v, want critical past the local threshold even though the vendor said normal", level)
	}
}

func TestIconStateTracksABalancePrimary(t *testing.T) {
	cfg := config.Default() // warn 75, critical 90
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{MeterID: "v:credits", Percent: 82, Level: quota.SeverityNormal},
	}}})

	percent, level, stale := NewPresenter(cfg).IconState(sub)
	if percent != 82 {
		t.Errorf("percent = %v, want the balance meter's 82", percent)
	}
	if level != quota.SeverityWarning {
		t.Errorf("level = %v, want warning past the local 75%% threshold", level)
	}
	if stale {
		t.Error("a healthy balance-only state must not render as stale")
	}
}

func TestIconStateBeforeFirstPoll(t *testing.T) {
	percent, level, stale := NewPresenter(config.Default()).IconState(Subscription{})
	if percent != 0 || level != quota.SeverityNormal || !stale {
		t.Errorf("startup icon = (%v,%v,%v), want an empty greyed gauge", percent, level, stale)
	}
}

func TestIconStateWithAnEmptySnapshot(t *testing.T) {
	sub := metering(poll.State{Snapshot: &quota.Snapshot{Vendor: "anthropic"}})
	if _, _, stale := NewPresenter(config.Default()).IconState(sub); !stale {
		t.Error("a snapshot carrying no meters must render as stale, not as 0% healthy")
	}
}

func TestIconStateMarksStaleOnFailure(t *testing.T) {
	sub := live()
	sub.State.LastError = &quota.FetchError{Kind: quota.Transient, Err: errors.New("offline")}
	if _, _, stale := NewPresenter(config.Default()).IconState(sub); !stale {
		t.Error("a failed poll must grey the icon")
	}
}

// stubController and stubCLI stand in for the poller and the identity cache.
type stubController struct {
	vendor string
	state  poll.State
}

func (s stubController) Vendor() string          { return s.vendor }
func (s stubController) State() poll.State       { return s.state }
func (stubController) Refresh() bool             { return true }
func (stubController) SetInterval(time.Duration) {}

type stubCLI struct {
	account    *identity.Account
	accountErr error
	prefs      *identity.Preferences
	prefsErr   error
	// invalidated is a pointer so a copy of this stub still counts against the
	// same total: the wiring holds these by value.
	invalidated *int
}

func (s stubCLI) Account() (*identity.Account, error) { return s.account, s.accountErr }

func (s stubCLI) Preferences() (*identity.Preferences, error) { return s.prefs, s.prefsErr }

func (s stubCLI) Invalidate() {
	if s.invalidated != nil {
		*s.invalidated++
	}
}

// oneProvider is the wiring shape main.go builds, with the ports stubbed.
func oneProvider(cli stubCLI) []ProviderWiring {
	return []ProviderWiring{{
		Controller: stubController{vendor: "anthropic", state: liveState()},
		CLI:        cli,
	}}
}
