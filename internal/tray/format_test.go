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

func TestRowsRenderMetersInProviderOrder(t *testing.T) {
	rows := presenterFor(t, i18n.LangPtBR).Rows(liveState(), now)
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

func TestRowsRenderInTheDefaultLanguage(t *testing.T) {
	rows := presenterFor(t, i18n.LangEnUS).Rows(liveState(), now)
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
func TestRowsKeepTheFigureAloneInTheValueColumn(t *testing.T) {
	for _, lang := range i18n.Available() {
		for _, row := range presenterFor(t, lang).Rows(liveState(), now) {
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
func TestRowsStateOneResetInstantOnce(t *testing.T) {
	weekly := now.Add(4*24*time.Hour + 6*time.Hour)
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "anthropic:session", Name: "session", Percent: 47,
			Reset: now.Add(2*time.Hour + 13*time.Minute)},
		&quota.Utilization{MeterID: "anthropic:weekly_all", Name: "weekly_all", Percent: 74, Reset: weekly},
		&quota.Utilization{MeterID: "anthropic:weekly_opus", Name: "weekly_opus", Percent: 4, Reset: weekly},
		&quota.Utilization{MeterID: "anthropic:weekly_sonnet", Name: "weekly_sonnet", Percent: 12, Reset: weekly},
	}}}

	rows := presenterFor(t, i18n.LangEnUS).Rows(state, now)
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
	for _, row := range presenterFor(t, i18n.LangEnUS).Rows(state, now) {
		if row.Detail == "" {
			t.Errorf("row %q lost its figure", row.Label)
		}
	}
}

// Suppression follows the rows the user reads, not the set of instants in the
// document: a window that resets at the same time as one further up, with another
// window between them, has to say so.
func TestRowsRepeatAResetThatIsNotConsecutive(t *testing.T) {
	shared := now.Add(3 * time.Hour)
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "a:first", Name: "first", Percent: 1, Reset: shared},
		&quota.Utilization{MeterID: "a:middle", Name: "middle", Percent: 2, Reset: now.Add(time.Hour)},
		&quota.Utilization{MeterID: "a:last", Name: "last", Percent: 3, Reset: shared},
	}}}

	for i, row := range presenterFor(t, i18n.LangEnUS).Rows(state, now) {
		if !strings.Contains(row.Label, "resets in") {
			t.Errorf("row %d = %q, want its own countdown", i, row.Label)
		}
	}
}

// A meter with no reset instant at all must neither state one nor let the next row
// inherit its silence.
func TestRowsAroundAMeterThatNeverResets(t *testing.T) {
	shared := now.Add(3 * time.Hour)
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "a:windowed", Name: "windowed", Percent: 1, Reset: shared},
		&quota.Balance{MeterID: "a:credits", Name: "credits",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2}},
		&quota.Utilization{MeterID: "a:again", Name: "again", Percent: 3, Reset: shared},
	}}}

	rows := presenterFor(t, i18n.LangEnUS).Rows(state, now)
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
func TestRowsOmitTheResetOfAMeterThatHasNone(t *testing.T) {
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{MeterID: "openrouter:credits", Name: "credits",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2}},
	}}}
	row := presenterFor(t, i18n.LangEnUS).Rows(state, now)[0]
	if row.Label != "credits" {
		t.Errorf("label = %q, want the bare name", row.Label)
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
	if !strings.Contains(tooltip, "Sessão (5h) · reset em 2h54  23%") {
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

func TestHeaderRowNamesTheProviderAndPlan(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for name, tc := range map[string]struct {
		state poll.State
		want  Row
	}{
		// The provider takes the position read first; the plan sits in the value
		// column every other row uses.
		"provider and plan": {liveState(), Row{Label: "Anthropic", Detail: "Claude Pro"}},
		// systray can hide an item but not a separator, so the heading is never
		// empty: before a provider has been reached it names the app.
		"the app before a poll": {poll.State{}, Row{Label: AppName}},
		"provider without a plan": {
			poll.State{Snapshot: &quota.Snapshot{Vendor: "anthropic", Product: "Claude"}},
			Row{Label: "Anthropic", Detail: "Claude"},
		},
		"provider naming no product": {
			poll.State{Snapshot: &quota.Snapshot{Vendor: "openrouter", Plan: "team"}},
			Row{Label: "Openrouter", Detail: "Team"},
		},
		// A provider that states no vendor key is still named by its product rather
		// than repeating it on both sides of the row.
		"product stands in for a missing vendor": {
			poll.State{Snapshot: &quota.Snapshot{Product: "Claude", Plan: "pro"}},
			Row{Label: "Claude", Detail: "Pro"},
		},
		// A plan alone heads the menu on the left: right-aligned, it would float in
		// the value column with nothing to qualify.
		"plan without either":     {poll.State{Snapshot: &quota.Snapshot{Plan: "max"}}, Row{Label: "Max"}},
		"snapshot naming neither": {poll.State{Snapshot: &quota.Snapshot{}}, Row{Label: AppName}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := presenter.HeaderRow(tc.state); got != tc.want {
				t.Errorf("HeaderRow() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHeaderRowCapitalizesOnlyTheFirstRune(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for plan, want := range map[string]string{
		"max":       "Claude Max",
		"pro":       "Claude Pro",
		"Max":       "Claude Max",
		"max_5x":    "Claude Max_5x",
		"étudiant":  "Claude Étudiant",
		"\xff":      "Claude \xff",
		"enterPris": "Claude EnterPris",
	} {
		state := poll.State{Snapshot: &quota.Snapshot{Vendor: "anthropic", Product: "Claude", Plan: plan}}
		if got := presenter.HeaderRow(state); got.Detail != want {
			t.Errorf("HeaderRow() for plan %q = %q, want %q", plan, got.Detail, want)
		}
	}
}

func TestAccountRowShowsWhoTheSubscriptionBelongsTo(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	full := &identity.Account{DisplayName: "Sample", Email: "sample@example.com", Organization: "Sample Org"}

	row := presenter.AccountRow(full)
	if row.Label != menuContinuationIndent+"Sample" {
		t.Errorf("account row = %q, want the indented display name", row.Label)
	}
	// It is a heading, not a reading: nothing belongs in its value column.
	if row.Detail != "" || row.Bar != "" {
		t.Errorf("account row = %+v, want no value", row)
	}
	// An address left permanently on screen is read by everyone watching a shared
	// screen, so it stays one level down while a name is available.
	if strings.Contains(row.Label, "@") {
		t.Errorf("account row = %q, want no e-mail at the first level", row.Label)
	}

	// An account the CLI cached without a name is still identified.
	nameless := presenter.AccountRow(&identity.Account{Email: "sample@example.com"})
	if nameless.Label != menuContinuationIndent+"sample@example.com" {
		t.Errorf("nameless account row = %q", nameless.Label)
	}

	for name, account := range map[string]*identity.Account{
		"unknown account":   nil,
		"empty document":    {},
		"organization only": {Organization: "Sample Org"},
	} {
		if row := presenter.AccountRow(account); row != (Row{}) {
			t.Errorf("%s: account row = %+v, want none", name, row)
		}
	}
}

func TestDetailRowsOmitFieldsTheDocumentDidNotSupply(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)

	full := presenter.DetailRows(&identity.Account{
		DisplayName: "Sample", Email: "sample@example.com", Organization: "Sample Org",
	})
	// The display name heads the menu, so repeating it here would show one value
	// twice in two levels of the same menu.
	if len(full) != 2 {
		t.Fatalf("rows = %+v, want the e-mail and the organization", full)
	}
	if full[0].Label != "E-mail" || full[0].Detail != "sample@example.com" {
		t.Errorf("row 0 = %+v", full[0])
	}
	if full[1].Label != "Organization" || full[1].Detail != "Sample Org" {
		t.Errorf("row 1 = %+v", full[1])
	}

	// With no name cached the e-mail heads the menu instead, and drops from here.
	nameless := presenter.DetailRows(&identity.Account{Email: "sample@example.com", Organization: "Sample Org"})
	if len(nameless) != 1 || nameless[0].Detail != "Sample Org" {
		t.Errorf("rows = %+v, want the organization alone", nameless)
	}

	if rows := presenter.DetailRows(&identity.Account{DisplayName: "Sample"}); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the name is already in the header", rows)
	}
	if rows := presenter.DetailRows(nil); rows != nil {
		t.Errorf("rows = %+v, want nil while the account is unknown", rows)
	}
}

// The platform layer pre-allocates exactly maxAccountRows submenu items and can
// never add another, so no combination of fields may ask for more.
func TestDetailRowsNeverExceedThePreAllocatedRows(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	values := []string{"", "Sample"}
	for _, name := range values {
		for _, email := range values {
			for _, org := range values {
				account := &identity.Account{DisplayName: name, Email: email, Organization: org}
				if rows := presenter.DetailRows(account); len(rows) > maxAccountRows {
					t.Errorf("%+v produced %d rows, over the %d allocated", account, len(rows), maxAccountRows)
				}
			}
		}
	}
}

func TestDetailRowLabelsAreLocalized(t *testing.T) {
	account := &identity.Account{DisplayName: "Sample", Email: "sample@example.com", Organization: "Sample Org"}
	rows := presenterFor(t, i18n.LangPtBR).DetailRows(account)
	if len(rows) != 2 || rows[1].Label != "Organização" {
		t.Errorf("pt-BR rows = %+v", rows)
	}
}

// A poll that has just landed must not be described by composing a zero-length
// span into "%s ago": that produced "Atualizado há agora" and "Updated now ago".
func TestStatusTextReadsNaturallyRightAfterAPoll(t *testing.T) {
	justPolled := liveState()
	justPolled.UpdatedAt = now
	justPolled.NextPollAt = now.Add(5 * time.Minute)

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
	overdue := liveState()
	overdue.UpdatedAt = now
	overdue.NextPollAt = now
	overdue.LastError = &quota.FetchError{Kind: quota.Transient, Err: errors.New("offline")}
	overdue.Snapshot.Meters[0].(*quota.Utilization).Reset = now

	for _, lang := range i18n.Available() {
		presenter := presenterFor(t, lang)
		texts := []string{presenter.StatusText(overdue, now), presenter.Tooltip(overdue, now)}
		for _, row := range presenter.Rows(overdue, now) {
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
