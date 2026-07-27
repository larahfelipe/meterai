package tray

import (
	"fmt"
	"strings"
	"testing"

	"github.com/larahfelipe/meterai/internal/buildinfo"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/identity"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

// The heading names the app, not whatever is being monitored, so it is the same
// row before the first poll as after it — which is what keeps the separator
// under it from ever opening a group with nothing above.
func TestHeaderRowNamesTheAppAndItsRelease(t *testing.T) {
	want := Row{Label: AppName, Detail: versionPrefix + buildinfo.Version}
	for _, lang := range i18n.Available() {
		if got := presenterFor(t, lang).HeaderRow(); got != want {
			t.Errorf("%s: HeaderRow() = %+v, want %+v", lang, got, want)
		}
	}
	if MenuRowTitle(want) == "" {
		t.Error("the heading row must never render empty")
	}
}

func TestProviderRowNamesTheVendorAndPlan(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for name, tc := range map[string]struct {
		sub  Subscription
		want Row
	}{
		// The vendor takes the position read first; the product and plan sit in
		// the value column every other row uses.
		"vendor and plan": {live(), Row{Label: "Anthropic", Detail: "Claude Pro"}},
		// The vendor key answers before the first poll, so a provider that has not
		// reported yet is still named rather than listed blank.
		"before the first poll": {
			Subscription{Vendor: "anthropic"}, Row{Label: "Anthropic"},
		},
		"vendor without a plan": {
			Subscription{Vendor: "anthropic", State: poll.State{
				Snapshot: &quota.Snapshot{Vendor: "anthropic", Product: "Claude"}}},
			Row{Label: "Anthropic", Detail: "Claude"},
		},
		"vendor naming no product": {
			Subscription{Vendor: "openrouter", State: poll.State{
				Snapshot: &quota.Snapshot{Vendor: "openrouter", Plan: "team"}}},
			Row{Label: "Openrouter", Detail: "Team"},
		},
		// A snapshot that states no vendor key is still named by its product rather
		// than repeating it on both sides of the row.
		"product stands in for a missing vendor": {
			Subscription{State: poll.State{Snapshot: &quota.Snapshot{Product: "Claude", Plan: "pro"}}},
			Row{Label: "Claude", Detail: "Pro"},
		},
		// A plan alone goes on the left: right-aligned, it would float in the value
		// column with nothing to qualify.
		"plan without either": {
			Subscription{State: poll.State{Snapshot: &quota.Snapshot{Plan: "max"}}}, Row{Label: "Max"},
		},
		// Unlike the heading, a provider row that can name nothing is hidden: it is
		// an item, and hiding one leaves no divider behind.
		"nothing known at all": {Subscription{}, Row{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := presenter.ProviderRow(tc.sub); got != tc.want {
				t.Errorf("ProviderRow() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProviderRowCapitalizesOnlyTheFirstRune(t *testing.T) {
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
		sub := Subscription{Vendor: "anthropic", State: poll.State{Snapshot: &quota.Snapshot{
			Vendor: "anthropic", Product: "Claude", Plan: plan}}}
		if got := presenter.ProviderRow(sub); got.Detail != want {
			t.Errorf("ProviderRow() for plan %q = %q, want %q", plan, got.Detail, want)
		}
	}
}

// The list is a column of names: the entry carries the vendor alone, and what
// the vendor sells is stated once, on the row heading the submenu it opens.
func TestProviderListEntryCarriesTheVendorAlone(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	sub := live()

	entry := presenter.ProviderListRow(sub)
	if entry != (Row{Label: "Anthropic"}) {
		t.Errorf("list entry = %+v, want the vendor alone", entry)
	}
	if head := presenter.ProviderRow(sub); head.Detail != "Claude Pro" {
		t.Errorf("submenu heading = %+v, want the plan the entry left out", head)
	}
}

func TestProviderListRowNamesEveryProviderItCanName(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for name, tc := range map[string]struct {
		sub  Subscription
		want Row
	}{
		"before the first poll": {Subscription{Vendor: "anthropic"}, Row{Label: "Anthropic"}},
		"vendor and plan":       {live(), Row{Label: "Anthropic"}},
		// A snapshot naming no vendor key is listed by whatever it did name, so the
		// entry is never a blank row that opens a submenu.
		"product only": {
			Subscription{State: poll.State{Snapshot: &quota.Snapshot{Product: "Claude"}}},
			Row{Label: "Claude"},
		},
		"plan only": {
			Subscription{State: poll.State{Snapshot: &quota.Snapshot{Plan: "max"}}},
			Row{Label: "Max"},
		},
		"nothing known at all": {Subscription{}, Row{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := presenter.ProviderListRow(tc.sub); got != tc.want {
				t.Errorf("ProviderListRow() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPreferencesRowSummarizesTheConfiguredDefaults(t *testing.T) {
	for lang, want := range map[i18n.Lang]string{
		i18n.LangEnUS: "Opus • High Effort",
		i18n.LangPtBR: "Opus • Esforço High",
	} {
		row := presenterFor(t, lang).PreferencesRow(live())
		if row.Label != want {
			t.Errorf("%s: preferences row = %q, want %q", lang, row.Label, want)
		}
		// It is a label, not a reading: nothing belongs in its value column, and a
		// gauge would imply an allowance.
		if row.Detail != "" || row.Bar != "" {
			t.Errorf("%s: preferences row = %+v, want no value", lang, row)
		}
	}
}

// Every row of the first group starts at the same margin: an indent under the
// provider would step the column in and back out again for one row.
func TestPreferencesRowSharesTheMarginOfTheRowsAroundIt(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	sub := live()
	for _, row := range append([]Row{
		presenter.HeaderRow(),
		presenter.ProviderRow(sub),
		presenter.PreferencesRow(sub),
	}, presenter.MeterRows(sub, now)...) {
		if strings.HasPrefix(row.Label, " ") {
			t.Errorf("row %q is indented out of the first group's margin", row.Label)
		}
	}
}

// Both values are title-cased for the caption, and only their first rune is:
// the rest is what the user wrote and may carry capitals that matter. The effort
// is additionally qualified, since on its own the recorded value names nothing.
func TestPreferencesRowTitleCasesTheValuesAndQualifiesTheEffort(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for name, tc := range map[string]struct {
		prefs *identity.Preferences
		want  string
	}{
		"both":        {&identity.Preferences{Model: "claude-opus-4-8", EffortLevel: "high"}, "Claude-opus-4-8 • High Effort"},
		"model only":  {&identity.Preferences{Model: "claude-opus-4-8"}, "Claude-opus-4-8"},
		"effort only": {&identity.Preferences{EffortLevel: "medium"}, "Medium Effort"},
		"neither":     {&identity.Preferences{}, ""},
		"unread file": {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			row := presenter.PreferencesRow(Subscription{Vendor: "anthropic", Preferences: tc.prefs})
			if tc.want == "" {
				if row != (Row{}) {
					t.Errorf("preferences row = %+v, want none", row)
				}
				return
			}
			if row.Label != tc.want {
				t.Errorf("preferences row = %q, want %q", row.Label, tc.want)
			}
		})
	}
}

func TestAccountRowsDescribeOneProvidersAccount(t *testing.T) {
	rows := presenterFor(t, i18n.LangEnUS).AccountRows(live())
	want := []Row{
		{Label: "Account", Detail: "Sample"},
		{Label: "E-mail", Detail: "sample@example.com"},
		{Label: "Organization", Detail: "Sample Org"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %d", rows, len(want))
	}
	for i, row := range rows {
		if row != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, row, want[i])
		}
	}
}

func TestAccountRowLabelsAreLocalized(t *testing.T) {
	rows := presenterFor(t, i18n.LangPtBR).AccountRows(live())
	for i, want := range []string{"Conta", "E-mail", "Organização"} {
		if rows[i].Label != want {
			t.Errorf("pt-BR row %d = %+v, want the label %q", i, rows[i], want)
		}
	}
}

// A field the CLI never recorded hides its own row and leaves the rest alone; a
// document that could not be read at all hides them together.
func TestAccountRowsOmitFieldsTheCLINeverRecorded(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	for name, tc := range map[string]struct {
		account *identity.Account
		want    []string
	}{
		"unreadable document": {nil, nil},
		"empty document":      {&identity.Account{}, nil},
		"organization only":   {&identity.Account{Organization: "Sample Org"}, []string{"Sample Org"}},
		"no name cached": {
			&identity.Account{Email: "sample@example.com", Organization: "Sample Org"},
			[]string{"sample@example.com", "Sample Org"},
		},
		"name only": {&identity.Account{DisplayName: "Sample"}, []string{"Sample"}},
	} {
		t.Run(name, func(t *testing.T) {
			rows := presenter.AccountRows(Subscription{Vendor: "anthropic", Account: tc.account})
			if len(rows) != len(tc.want) {
				t.Fatalf("rows = %+v, want %v", rows, tc.want)
			}
			// An empty result must be nil, since that is what hides the submenu.
			if len(tc.want) == 0 && rows != nil {
				t.Errorf("rows = %+v, want nil", rows)
			}
			for i, value := range tc.want {
				if rows[i].Detail != value {
					t.Errorf("row %d = %+v, want %q", i, rows[i], value)
				}
			}
		})
	}
}

// The platform layer pre-allocates exactly maxAccountRows items per provider and
// can never add another, so no combination of fields may ask for more.
func TestAccountRowsNeverExceedThePreAllocatedRows(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)

	// Every combination of the three optional fields being present or absent, as
	// the bits of a counter.
	const fieldCount = 3
	field := func(mask, bit int) string {
		if mask&(1<<bit) == 0 {
			return ""
		}
		return fmt.Sprintf("value-%d", bit)
	}
	widest := 0
	for mask := 0; mask < 1<<fieldCount; mask++ {
		account := &identity.Account{
			DisplayName: field(mask, 0), Email: field(mask, 1), Organization: field(mask, 2),
		}
		rows := presenter.AccountRows(Subscription{Vendor: "anthropic", Account: account})
		if len(rows) > maxAccountRows {
			t.Fatalf("%+v produced %d rows, over the %d allocated", account, len(rows), maxAccountRows)
		}
		widest = max(widest, len(rows))
	}
	// A ceiling nothing reaches is a row allocated at startup and never shown.
	if widest != maxAccountRows {
		t.Errorf("the widest combination produced %d rows, but %d are allocated", widest, maxAccountRows)
	}
}

// An account field is not a quantity, so nothing in that submenu carries a gauge.
func TestAccountRowsCarryNoGauge(t *testing.T) {
	for _, row := range presenterFor(t, i18n.LangEnUS).AccountRows(live()) {
		if row.Bar != "" {
			t.Errorf("row %+v carries a gauge", row)
		}
	}
}

// Account detail belongs behind the provider entry, never on the first level:
// an address left permanently on screen is read by everyone watching a shared
// screen, and the first level is what a user opens the menu to see.
func TestNoAccountDetailReachesTheFirstLevel(t *testing.T) {
	presenter := presenterFor(t, i18n.LangEnUS)
	sub := live()
	first := []Row{
		presenter.HeaderRow(),
		presenter.ProviderRow(sub),
		presenter.PreferencesRow(sub),
	}
	first = append(first, presenter.MeterRows(sub, now)...)

	for _, row := range first {
		title := MenuRowTitle(row)
		for _, secret := range []string{sub.Account.Email, sub.Account.Organization} {
			if strings.Contains(title, secret) {
				t.Errorf("first-level row %q carries the account field %q", title, secret)
			}
		}
	}
}

func TestSubscriptionsActive(t *testing.T) {
	second := Subscription{Vendor: "openrouter"}
	subs := Subscriptions{live(), second}
	if got := subs.Active(); got.Vendor != "anthropic" {
		t.Errorf("Active() = %q, want the first configured provider", got.Vendor)
	}
	// An empty list still renders: the zero value is what the menu draws before
	// any provider has been wired, rather than a nil dereference.
	if got := (Subscriptions{}).Active(); got != (Subscription{}) {
		t.Errorf("Active() on an empty list = %+v, want the zero subscription", got)
	}
}
