package tray

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

func TestProgressBarQuantizesByTenths(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		want    string
	}{
		{0, "▁▁▁▁▁▁▁▁▁▁"},
		{0.4, "▄▁▁▁▁▁▁▁▁▁"},
		{5, "▄▁▁▁▁▁▁▁▁▁"},
		{10, "▄▁▁▁▁▁▁▁▁▁"},
		{23, "▄▄▁▁▁▁▁▁▁▁"},
		{45, "▄▄▄▄▄▁▁▁▁▁"},
		{50, "▄▄▄▄▄▁▁▁▁▁"},
		{74, "▄▄▄▄▄▄▄▁▁▁"},
		{99.9, "▄▄▄▄▄▄▄▄▄▄"},
		{100, "▄▄▄▄▄▄▄▄▄▄"},
	} {
		if got := progressBar(tc.percent); got != tc.want {
			t.Errorf("progressBar(%v) = %q, want %q", tc.percent, got, tc.want)
		}
	}
}

// Any consumption at all has to look different from none, which is the same rule
// the icon gauge applies to its bottom pixel row.
func TestProgressBarGivesAnyUsageOneCell(t *testing.T) {
	for _, percent := range []float64{0.0001, 0.4, 1, 4.9} {
		bar := progressBar(percent)
		if filled := strings.Count(bar, string(barFilledCell)); filled != 1 {
			t.Errorf("progressBar(%v) filled %d cells, want exactly 1", percent, filled)
		}
	}
	if bar := progressBar(0); strings.ContainsRune(bar, barFilledCell) {
		t.Errorf("progressBar(0) = %q, want no filled cell", bar)
	}
}

// quota.Percent is not clamped in the model; a vendor permitting overage reports
// past 100 and only the display saturates.
func TestProgressBarClampsOverage(t *testing.T) {
	full := progressBar(100)
	for _, percent := range []float64{100.0001, 150, 1e9, math.Inf(1)} {
		if got := progressBar(percent); got != full {
			t.Errorf("progressBar(%v) = %q, want the saturated %q", percent, got, full)
		}
	}
}

func TestProgressBarRejectsUnusableFigures(t *testing.T) {
	empty := progressBar(0)
	for _, percent := range []float64{-0.0001, -50, math.Inf(-1), math.NaN()} {
		if got := progressBar(percent); got != empty {
			t.Errorf("progressBar(%v) = %q, want the empty %q", percent, got, empty)
		}
	}
}

// The gauge sits between two variable-width fields, so its own width must not
// vary with its value.
func TestProgressBarWidthIsConstant(t *testing.T) {
	for percent := -10.0; percent <= 210.0; percent += 0.37 {
		bar := progressBar(percent)
		if got := utf8.RuneCountInString(bar); got != meterBarCells {
			t.Fatalf("progressBar(%v) is %d runes, want %d", percent, got, meterBarCells)
		}
		if filled := strings.Count(bar, string(barFilledCell)); filled > meterBarCells {
			t.Fatalf("progressBar(%v) filled %d of %d cells", percent, filled, meterBarCells)
		}
	}
}

func TestRowsGaugeEveryBoundedMeter(t *testing.T) {
	limit := quota.Money{AmountMinor: 5000, Currency: "USD", Exponent: 2}
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Utilization{MeterID: "anthropic:session", Name: "session", Percent: 23},
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

	rows := presenterFor(t, i18n.LangEnUS).MeterRows(metering(state), now)
	if rows[0].Bar != "▄▄▁▁▁▁▁▁▁▁" {
		t.Errorf("utilization gauge = %q", rows[0].Bar)
	}
	if rows[1].Bar != "▄▄▁▁▁▁▁▁▁▁" {
		t.Errorf("capped balance gauge = %q", rows[1].Bar)
	}
	// An uncapped balance reports zero percent because no limit exists, not
	// because nothing was spent: an empty gauge there would invent an allowance.
	if rows[2].Bar != "" {
		t.Errorf("uncapped balance gauge = %q, want none", rows[2].Bar)
	}
}

func TestMenuRowTitleOmitsFieldsTheMeterLacks(t *testing.T) {
	for name, tc := range map[string]struct {
		row  Row
		want string
	}{
		"complete row": {
			Row{Label: "Session (5h) · resets in 2h54", Detail: "23%", Bar: "▄▄▁▁▁▁▁▁▁▁"},
			"Session (5h) · resets in 2h54\t23%  ▄▄▁▁▁▁▁▁▁▁",
		},
		"no gauge": {
			Row{Label: "Credits", Detail: "7.50 USD"},
			"Credits\t7.50 USD",
		},
		"no figures": {
			Row{Label: "Weekly", Bar: "▄▄▄▄▄▄▄▁▁▁"},
			"Weekly\t▄▄▄▄▄▄▄▁▁▁",
		},
		"label only": {
			Row{Label: "Weekly"},
			"Weekly",
		},
		"empty row": {Row{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := MenuRowTitle(tc.row); got != tc.want {
				t.Errorf("MenuRowTitle(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// Labels differ in length, so spaces cannot line the figures and gauges up in a
// proportional menu font. The tab is what Windows treats as a column break:
// everything after it is drawn flush right, which puts every value in the same
// column.
func TestMenuRowTitlePutsEveryValueInOneColumn(t *testing.T) {
	rows := presenterFor(t, i18n.LangPtBR).MeterRows(live(), now)
	if len(rows) < 2 {
		t.Fatalf("rows = %d, want at least two meters", len(rows))
	}
	for _, row := range rows {
		title := MenuRowTitle(row)
		if strings.Count(title, menuRightAlign) != 1 {
			t.Errorf("title %q must carry exactly one column break", title)
		}
		left, right, _ := strings.Cut(title, menuRightAlign)
		// The name and the reset countdown are what the row is about and stay left;
		// the figure and its gauge are what it reads and travel together, right.
		if left != row.Label {
			t.Errorf("left of the break = %q, want the label %q", left, row.Label)
		}
		if want := row.Detail + menuValueGap + row.Bar; right != want {
			t.Errorf("right of the break = %q, want %q", right, want)
		}
	}
}

// A gauge of fixed width is what aligns the figures beside it: with the whole
// right column flush right, the figures' right edges land at the same offset
// without a single space of padding.
func TestMenuRowTitleAlignsFiguresAgainstTheGauge(t *testing.T) {
	for _, percent := range []float64{0, 4, 47, 100} {
		state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
			&quota.Utilization{MeterID: "anthropic:session", Name: "session", Percent: percent},
		}}}
		row := presenterFor(t, i18n.LangEnUS).MeterRows(metering(state), now)[0]
		title := MenuRowTitle(row)
		_, right, _ := strings.Cut(title, menuRightAlign)
		if got, want := utf8.RuneCountInString(right)-utf8.RuneCountInString(row.Detail), meterBarCells+len(menuValueGap); got != want {
			t.Errorf("%v%%: %d runes follow the figure, want a constant %d", percent, got, want)
		}
	}
}

func TestMenuRowTitleHasNoColumnBreakWithoutAValue(t *testing.T) {
	// A row with nothing to right-align must not open an empty column, which would
	// widen every other row in the menu.
	for _, row := range []Row{
		{Label: "Refresh now"},
		{Label: "Opus • High Effort"},
		{},
	} {
		if title := MenuRowTitle(row); strings.Contains(title, menuRightAlign) {
			t.Errorf("MenuRowTitle(%+v) = %q, want no column break", row, title)
		}
	}
}

// Menu captions are the app's last hop before the shell, and the account name,
// the organization and a vendor's own meter label all arrive from documents this
// app only reads.
func TestMenuRowTitleNeutralizesWhatTheShellWouldInterpret(t *testing.T) {
	for name, tc := range map[string]struct {
		row  Row
		want string
	}{
		// An unescaped ampersand is consumed as a mnemonic marker and the character
		// after it is underlined instead of drawn: "R&D" would appear as "RD".
		"ampersand is doubled": {
			Row{Label: "R&D", Detail: "a&b"},
			"R&&D\ta&&b",
		},
		// A tab inside a field would open a second column break and throw the value
		// column of every other row off.
		"tab becomes a space": {
			Row{Label: "Sample\tOrg"},
			"Sample Org",
		},
		"newline becomes a space": {
			Row{Label: "first\r\nsecond"},
			"first  second",
		},
		// A right-to-left override draws nothing and reverses everything after it,
		// which is enough to make a row read as another account entirely.
		"format characters are dropped": {
			Row{Label: "Sample‮Org​"},
			"SampleOrg",
		},
		"ordinary text is untouched": {
			Row{Label: "Ação · reset em 2h54", Detail: "23%"},
			"Ação · reset em 2h54\t23%",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := MenuRowTitle(tc.row); got != tc.want {
				t.Errorf("MenuRowTitle(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// Whatever a provider or the CLI's own state document puts in a row, the caption
// must still carry exactly the one column break the layout puts there.
func TestMenuRowTitleKeepsOneColumnBreakUnderHostileInput(t *testing.T) {
	hostile := "\t\t&&‮\n\t"
	for _, row := range []Row{
		{Label: hostile, Detail: hostile, Bar: "▄▁▁▁▁▁▁▁▁▁"},
		{Label: hostile, Detail: hostile},
	} {
		if got := strings.Count(MenuRowTitle(row), menuRightAlign); got != 1 {
			t.Errorf("MenuRowTitle(%+v) carries %d column breaks, want 1", row, got)
		}
	}
}
