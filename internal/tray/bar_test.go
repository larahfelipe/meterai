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
		{0, "░░░░░░░░░░"},
		{0.4, "█░░░░░░░░░"},
		{5, "█░░░░░░░░░"},
		{10, "█░░░░░░░░░"},
		{23, "██░░░░░░░░"},
		{45, "█████░░░░░"},
		{50, "█████░░░░░"},
		{74, "███████░░░"},
		{99.9, "██████████"},
		{100, "██████████"},
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

	rows := presenterFor(t, i18n.LangEnUS).Rows(state, now)
	if rows[0].Bar != "██░░░░░░░░" {
		t.Errorf("utilization gauge = %q", rows[0].Bar)
	}
	if rows[1].Bar != "██░░░░░░░░" {
		t.Errorf("capped balance gauge = %q", rows[1].Bar)
	}
	// An uncapped balance reports zero percent because no limit exists, not
	// because nothing was spent: an empty gauge there would invent an allowance.
	if rows[2].Bar != "" {
		t.Errorf("uncapped balance gauge = %q, want none", rows[2].Bar)
	}
}

// Ten cells per meter would cost more of the 127-rune budget than the figures,
// pushing the status line out of the tooltip.
func TestTooltipCarriesNoGauges(t *testing.T) {
	for _, lang := range i18n.Available() {
		tooltip := presenterFor(t, lang).Tooltip(liveState(), now)
		if strings.ContainsRune(tooltip, barFilledCell) || strings.ContainsRune(tooltip, barEmptyCell) {
			t.Errorf("%s: tooltip carries a gauge: %q", lang, tooltip)
		}
	}
}

func TestMenuRowTitleOmitsFieldsTheMeterLacks(t *testing.T) {
	for name, tc := range map[string]struct {
		row  Row
		want string
	}{
		"complete row": {
			Row{Label: "Session (5h)", Bar: "██░░░░░░░░", Detail: "23% · resets in 2h54"},
			"Session (5h)   23% · resets in 2h54\t██░░░░░░░░",
		},
		"no gauge": {
			Row{Label: "Credits", Detail: "7.50 USD"},
			"Credits   7.50 USD",
		},
		"no figures": {
			Row{Label: "Weekly", Bar: "███████░░░"},
			"Weekly\t███████░░░",
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

// Labels differ in length, so spaces cannot line the gauges up in a proportional
// menu font. The tab is what Windows treats as a column break: everything after it
// is drawn flush right, which puts every gauge in the same column.
func TestMenuRowTitlePushesTheGaugeToOneColumn(t *testing.T) {
	rows := presenterFor(t, i18n.LangPtBR).Rows(liveState(), now)
	if len(rows) < 2 {
		t.Fatalf("rows = %d, want at least two meters", len(rows))
	}
	for _, row := range rows {
		title := MenuRowTitle(row)
		if strings.Count(title, menuRightAlign) != 1 {
			t.Errorf("title %q must carry exactly one column break", title)
		}
		left, right, _ := strings.Cut(title, menuRightAlign)
		if right != row.Bar {
			t.Errorf("field after the column break = %q, want the gauge %q", right, row.Bar)
		}
		// The label and the figures stay on the left of the break, in reading order.
		if !strings.HasPrefix(left, row.Label) || !strings.HasSuffix(left, row.Detail) {
			t.Errorf("left of the break = %q, want %q then %q", left, row.Label, row.Detail)
		}
	}
}

func TestMenuRowTitleHasNoColumnBreakWithoutAGauge(t *testing.T) {
	// A row with nothing to right-align must not open an empty column, which would
	// widen every other row in the menu.
	for _, row := range []Row{
		{Label: "E-mail", Detail: "sample@example.com"},
		{Label: "Credits", Detail: "7.50 USD"},
		{Label: "Weekly"},
	} {
		if title := MenuRowTitle(row); strings.Contains(title, menuRightAlign) {
			t.Errorf("MenuRowTitle(%+v) = %q, want no column break", row, title)
		}
	}
}
