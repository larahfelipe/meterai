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
			Vendor: "anthropic",
			Plan:   "pro",
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

func TestRowsRenderMetersInProviderOrder(t *testing.T) {
	rows := presenterFor(t, i18n.LangPtBR).Rows(liveState(), now)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Label != "Sessão (5h)" || rows[0].Detail != "23% · reset em 2h54" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Label != "Semanal" || rows[1].Detail != "74% · reset em 1d01h" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestRowsRenderInTheDefaultLanguage(t *testing.T) {
	rows := presenterFor(t, i18n.LangEnUS).Rows(liveState(), now)
	if rows[0].Label != "Session (5h)" || rows[0].Detail != "23% · resets in 2h54" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Label != "Weekly" || rows[1].Detail != "74% · resets in 1d01h" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

// A window the vendor introduces after this release has no catalogue entry, and
// must still reach the menu under the provider's own name.
func TestRowsFallBackToTheProviderNameForAnUnknownMeter(t *testing.T) {
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "anthropic:monthly_opus", Name: "monthly_opus", Percent: 12},
	}}}
	for _, lang := range i18n.Available() {
		rows := presenterFor(t, lang).Rows(state, now)
		if rows[0].Label != "monthly_opus" {
			t.Errorf("%s: label = %q, want the raw kind", lang, rows[0].Label)
		}
	}
}

func TestRowsBeforeFirstPoll(t *testing.T) {
	if rows := presenterFor(t, i18n.LangEnUS).Rows(poll.State{}, now); rows != nil {
		t.Errorf("rows = %v, want nil before the first successful poll", rows)
	}
}

func TestRowsRenderMoneyMeters(t *testing.T) {
	limit := quota.Money{AmountMinor: 5000, Currency: "USD", Exponent: 2}
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{
			MeterID: "anthropic:spend", Name: "spend",
			Used:  quota.Money{AmountMinor: 1234, Currency: "USD", Exponent: 2},
			Limit: &limit, Percent: 24.68,
		},
		&quota.Balance{
			MeterID: "openrouter:credits", Name: "credits",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2},
		},
	}}}
	rows := presenterFor(t, i18n.LangPtBR).Rows(state, now)
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
		-time.Hour:                      "agora",
		0:                               "agora",
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
	healthy := presenter.StatusText(liveState(), now)
	if healthy != "Atualizado há 1min · próxima em 3min" {
		t.Errorf("healthy status = %q", healthy)
	}

	if got := presenter.StatusText(poll.State{}, now); got != "Consultando…" {
		t.Errorf("startup status = %q", got)
	}
}

func TestStatusTextInTheDefaultLanguage(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	if got, want := presenter.StatusText(liveState(), now), "Updated 1m ago · next in 3m"; got != want {
		t.Errorf("healthy status = %q, want %q", got, want)
	}
	if got, want := presenter.StatusText(poll.State{}, now), "Polling…"; got != want {
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
		state := liveState()
		state.LastError = &quota.FetchError{Kind: kind, Err: errors.New("x")}
		got := presenter.StatusText(state, now)
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
	state := poll.State{LastError: errors.New("boom")}
	got := presenter.StatusText(state, now)
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

func TestTooltipFitsThePlatformLimit(t *testing.T) {
	presenter := presenterFor(t, i18n.LangPtBR)
	tooltip := presenter.Tooltip(liveState(), now)
	if lines := strings.Split(tooltip, "\n"); len(lines) != 3 {
		t.Errorf("tooltip = %q, want two meter lines plus a status line", tooltip)
	}
	if !strings.Contains(tooltip, "Sessão (5h)  23%") {
		t.Errorf("tooltip = %q", tooltip)
	}

	// A vendor returning many meters must not produce a tooltip Windows will
	// reject or cut mid-character.
	crowded := liveState()
	for i := 0; i < 20; i++ {
		crowded.Snapshot.Meters = append(crowded.Snapshot.Meters, &quota.Utilization{
			Name: "an extra window with a deliberately long name", Percent: 50, Reset: now.Add(time.Hour),
		})
	}
	long := presenter.Tooltip(crowded, now)
	if runes := []rune(long); len(runes) > maxTooltipRunes {
		t.Errorf("tooltip is %d runes, over the %d limit", len(runes), maxTooltipRunes)
	}
	if !strings.HasSuffix(long, ellipsis) {
		t.Errorf("a truncated tooltip must be marked: %q", long)
	}
	if !strings.ContainsRune(long, 'ã') {
		t.Error("truncation corrupted multi-byte characters")
	}
}

// The tooltip limit is a platform constant, not a per-language one: a
// translation with longer words must still fit rather than be dropped by the
// shell.
func TestTooltipFitsThePlatformLimitInEveryLanguage(t *testing.T) {
	crowded := liveState()
	for i := 0; i < 20; i++ {
		crowded.Snapshot.Meters = append(crowded.Snapshot.Meters, &quota.Utilization{
			MeterID: "anthropic:weekly_opus", Name: "weekly_opus", Percent: 50, Reset: now.Add(time.Hour),
		})
	}
	for _, lang := range i18n.Available() {
		tooltip := presenterFor(t, lang).Tooltip(crowded, now)
		if runes := []rune(tooltip); len(runes) > maxTooltipRunes {
			t.Errorf("%s: tooltip is %d runes, over the %d limit", lang, len(runes), maxTooltipRunes)
		}
		if !strings.HasSuffix(tooltip, ellipsis) {
			t.Errorf("%s: a truncated tooltip must be marked: %q", lang, tooltip)
		}
	}
}

func TestIconStateTracksTheActiveWindow(t *testing.T) {
	cfg := config.Default() // warn 75, critical 90
	percent, level, stale := NewPresenter(cfg).IconState(liveState())
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
	cfg := config.Default()
	state := liveState()
	state.Snapshot.Meters[1].(*quota.Utilization).Percent = 91

	_, level, _ := NewPresenter(cfg).IconState(state)
	if level != quota.SeverityCritical {
		t.Errorf("level = %v, want critical past the local threshold even though the vendor said normal", level)
	}
}

func TestIconStateTracksABalancePrimary(t *testing.T) {
	cfg := config.Default() // warn 75, critical 90
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{MeterID: "v:credits", Percent: 82, Level: quota.SeverityNormal},
	}}}

	percent, level, stale := NewPresenter(cfg).IconState(state)
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
	percent, level, stale := NewPresenter(config.Default()).IconState(poll.State{})
	if percent != 0 || level != quota.SeverityNormal || !stale {
		t.Errorf("startup icon = (%v,%v,%v), want an empty greyed gauge", percent, level, stale)
	}
}

func TestIconStateWithAnEmptySnapshot(t *testing.T) {
	state := poll.State{Snapshot: &quota.Snapshot{Vendor: "anthropic"}}
	if _, _, stale := NewPresenter(config.Default()).IconState(state); !stale {
		t.Error("a snapshot carrying no meters must render as stale, not as 0% healthy")
	}
}

func TestIconStateMarksStaleOnFailure(t *testing.T) {
	state := liveState()
	state.LastError = &quota.FetchError{Kind: quota.Transient, Err: errors.New("offline")}
	if _, _, stale := NewPresenter(config.Default()).IconState(state); !stale {
		t.Error("a failed poll must grey the icon")
	}
}

func TestHeaderTextNamesTheAccountAndPlan(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for name, tc := range map[string]struct {
		account *identity.Account
		state   poll.State
		want    string
	}{
		"name and plan": {
			&identity.Account{DisplayName: "Sample", Email: "sample@example.com"},
			liveState(), "Sample · Pro",
		},
		"address stands in for a missing display name": {
			&identity.Account{Email: "sample@example.com"},
			liveState(), "sample@example.com · Pro",
		},
		"plan only while the account is unknown": {
			nil, liveState(), "Pro",
		},
		"account only before the first poll": {
			&identity.Account{DisplayName: "Sample"},
			poll.State{}, "Sample",
		},
		"organization alone does not identify the account": {
			&identity.Account{Organization: "Sample Org"},
			poll.State{}, "",
		},
		"nothing known yet": {nil, poll.State{}, ""},
		"snapshot without a plan": {
			nil,
			poll.State{Snapshot: &quota.Snapshot{Vendor: "anthropic"}},
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := presenter.HeaderText(tc.state, tc.account); got != tc.want {
				t.Errorf("HeaderText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHeaderTextCapitalizesOnlyTheFirstRune(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for plan, want := range map[string]string{
		"max":       "Max",
		"pro":       "Pro",
		"Max":       "Max",
		"max_5x":    "Max_5x",
		"étudiant":  "Étudiant",
		"\xff":      "\xff",
		"enterPris": "EnterPris",
	} {
		state := poll.State{Snapshot: &quota.Snapshot{Plan: plan}}
		if got := presenter.HeaderText(state, nil); got != want {
			t.Errorf("HeaderText() for plan %q = %q, want %q", plan, got, want)
		}
	}
}

func TestDetailRowsOmitFieldsTheVendorDidNotSupply(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)

	full := presenter.DetailRows(&identity.Account{
		DisplayName: "Sample", Email: "sample@example.com", Organization: "Sample Org",
	})
	if len(full) != 2 {
		t.Fatalf("rows = %d, want e-mail and organization", len(full))
	}
	if full[0].Label != "E-mail" || full[0].Detail != "sample@example.com" {
		t.Errorf("row 0 = %+v", full[0])
	}
	if full[1].Label != "Organization" || full[1].Detail != "Sample Org" {
		t.Errorf("row 1 = %+v", full[1])
	}

	// A personal account has no organization, and one with only a display name has
	// nothing to put under Details at all.
	personal := presenter.DetailRows(&identity.Account{Email: "sample@example.com"})
	if len(personal) != 1 || personal[0].Detail != "sample@example.com" {
		t.Errorf("personal rows = %+v", personal)
	}
	if rows := presenter.DetailRows(&identity.Account{DisplayName: "Sample"}); len(rows) != 0 {
		t.Errorf("rows = %+v, want none when only the display name is known", rows)
	}
	if rows := presenter.DetailRows(nil); rows != nil {
		t.Errorf("rows = %+v, want nil while the account is unknown", rows)
	}
}

func TestDetailRowLabelsAreLocalized(t *testing.T) {
	account := &identity.Account{Email: "sample@example.com", Organization: "Sample Org"}
	rows := presenterFor(t, i18n.LangPtBR).DetailRows(account)
	if len(rows) != 2 || rows[1].Label != "Organização" {
		t.Errorf("pt-BR rows = %+v", rows)
	}
}
