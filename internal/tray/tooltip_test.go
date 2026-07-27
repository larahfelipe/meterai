package tray

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

// crowdedSubscription is a plan reporting more windows than the tooltip budget
// can hold, which is where every truncation rule is actually exercised.
func crowdedSubscription() Subscription {
	sub := live()
	weekly := now.Add(4*24*time.Hour + 6*time.Hour)
	for _, kind := range []string{"weekly_opus", "weekly_sonnet", "weekly_cowork"} {
		sub.State.Snapshot.Meters = append(sub.State.Snapshot.Meters, &quota.Utilization{
			MeterID: quota.MeterID("anthropic:" + kind), Name: kind, Percent: 51, Reset: weekly,
		})
	}
	return sub
}

// meterLines are the tooltip lines that describe a meter: everything except the
// trailing status line, which is prose and is the only line allowed to be cut.
func meterLines(tooltip string) []string {
	lines := strings.Split(tooltip, "\n")
	if len(lines) == 0 {
		return nil
	}
	return lines[:len(lines)-1]
}

// The defect this guards: the shell cut the tooltip mid-line, and the fragment
// that survived — a window name and its reset countdown — reads exactly like a
// meter that reported no figure at all. Every meter line the tooltip shows
// carries its own reading.
func TestTooltipNeverShowsAMeterWithoutItsFigure(t *testing.T) {
	for _, lang := range i18n.Available() {
		presenter := presenterFor(t, lang)
		for name, sub := range map[string]Subscription{
			"two windows":   live(),
			"five windows":  crowdedSubscription(),
			"before a poll": {Vendor: "anthropic"},
		} {
			tooltip := presenter.Tooltip(sub, now)
			for _, line := range meterLines(tooltip) {
				if !strings.HasSuffix(line, "%") {
					t.Errorf("%s/%s: meter line %q lost its figure", lang, name, line)
				}
			}
		}
	}
}

func TestTooltipStatesEveryWindowItHasRoomFor(t *testing.T) {
	for lang, want := range map[i18n.Lang][]string{
		i18n.LangEnUS: {"Session (5h)  23%", "Weekly (7d)  74%", "Updated 1m ago"},
		i18n.LangPtBR: {"Sessão (5h)  23%", "Semanal (7d)  74%", "Atualizado há 1min"},
	} {
		got := strings.Split(presenterFor(t, lang).Tooltip(live(), now), "\n")
		if len(got) != len(want) {
			t.Fatalf("%s: tooltip = %q, want %d lines", lang, got, len(want))
		}
		for i, line := range want {
			if got[i] != line {
				t.Errorf("%s: line %d = %q, want %q", lang, i, got[i], line)
			}
		}
	}
}

// The budget is a platform constant, not a per-language one: a translation with
// longer words must still fit rather than be discarded by the shell.
func TestTooltipFitsThePlatformLimit(t *testing.T) {
	for _, lang := range i18n.Available() {
		presenter := presenterFor(t, lang)
		failing := crowdedSubscription()
		failing.State.LastError = &quota.FetchError{Kind: quota.Unauthorized, Err: errors.New("expired")}

		for name, sub := range map[string]Subscription{
			"two windows":     live(),
			"five windows":    crowdedSubscription(),
			"five and failed": failing,
			"before a poll":   {Vendor: "anthropic"},
			"nothing wired":   {},
		} {
			tooltip := presenter.Tooltip(sub, now)
			if n := utf8.RuneCountInString(tooltip); n > maxTooltipRunes {
				t.Errorf("%s/%s: tooltip is %d runes, over the %d the shell reads: %q",
					lang, name, n, maxTooltipRunes, tooltip)
			}
		}
	}
}

// A vendor naming a window at length must cost that window's row, not the rows
// of the windows above it: the order is the provider's own and the first rows
// are the ones the user came to read.
func TestTooltipDropsLaterWindowsRatherThanEarlierOnes(t *testing.T) {
	sub := live()
	sub.State.Snapshot.Meters = append(sub.State.Snapshot.Meters, &quota.Utilization{
		MeterID: "anthropic:monthly", Name: "an extra window with a deliberately long name",
		Percent: 50, Reset: now.Add(time.Hour),
	})

	tooltip := presenterFor(t, i18n.LangEnUS).Tooltip(sub, now)
	if !strings.HasPrefix(tooltip, "Session (5h)  23%\nWeekly (7d)  74%") {
		t.Errorf("tooltip = %q, want the first two windows intact", tooltip)
	}
	if strings.Contains(tooltip, "deliberately long") {
		t.Errorf("tooltip = %q, want the oversized window dropped whole", tooltip)
	}
}

// The status line is prose: cut short it still reads, so it is elided rather
// than dropped, and the ellipsis says the sentence continues.
func TestTooltipElidesTheStatusLineRatherThanDroppingIt(t *testing.T) {
	sub := live()
	sub.State.LastError = &quota.FetchError{Kind: quota.Unauthorized, Err: errors.New("expired")}

	tooltip := presenterFor(t, i18n.LangEnUS).Tooltip(sub, now)
	status := strings.Split(tooltip, "\n")
	last := status[len(status)-1]
	if !strings.HasPrefix(last, "Credential expired") {
		t.Errorf("last line = %q, want the failure stated first", last)
	}
	if !strings.HasSuffix(last, ellipsis) {
		t.Errorf("last line = %q, want the elision marked", last)
	}
	if utf8.RuneCountInString(tooltip) > maxTooltipRunes {
		t.Errorf("tooltip = %q, over the limit", tooltip)
	}
}

// Truncation cuts on a rune boundary: a byte-boundary cut would corrupt the
// accented characters that appear throughout the pt-BR catalogue.
func TestTooltipTruncationPreservesMultiByteCharacters(t *testing.T) {
	sub := crowdedSubscription()
	sub.State.LastError = &quota.FetchError{Kind: quota.Protocol, Err: errors.New("changed")}

	tooltip := presenterFor(t, i18n.LangPtBR).Tooltip(sub, now)
	if !utf8.ValidString(tooltip) {
		t.Errorf("tooltip = %q is not valid UTF-8", tooltip)
	}
	if !strings.ContainsRune(tooltip, 'ã') {
		t.Errorf("tooltip = %q lost its multi-byte characters", tooltip)
	}
}

// Before the first poll there are no figures, so the whole budget goes to saying
// what the app is doing.
func TestTooltipBeforeTheFirstPoll(t *testing.T) {
	for lang, want := range map[i18n.Lang]string{
		i18n.LangEnUS: "Polling…",
		i18n.LangPtBR: "Consultando…",
	} {
		if got := presenterFor(t, lang).Tooltip(Subscription{Vendor: "anthropic"}, now); got != want {
			t.Errorf("%s: tooltip = %q, want %q", lang, got, want)
		}
	}
}

// The gauges are deliberately absent: ten cells per meter would cost more of the
// budget than the figures they illustrate, and the icon already draws the same
// reading at the same quantization.
func TestTooltipCarriesNoGauges(t *testing.T) {
	tooltip := presenterFor(t, i18n.LangEnUS).Tooltip(live(), now)
	if strings.ContainsRune(tooltip, barFilledCell) || strings.ContainsRune(tooltip, barEmptyCell) {
		t.Errorf("tooltip = %q, want no gauge", tooltip)
	}
}

func TestJoinWithinBudgetLeavesNoDanglingSeparator(t *testing.T) {
	for name, tc := range map[string]struct {
		lines    []string
		trailing string
		limit    int
		want     string
	}{
		"everything fits":      {[]string{"a", "b"}, "c", 10, "a\nb\nc"},
		"no room for trailing": {[]string{"aaaa", "bbbb"}, "cc", 9, "aaaa\nbbbb"},
		"trailing elided":      {[]string{"aaaa"}, "ccccc", 8, "aaaa\ncc…"},
		"nothing fits at all":  {[]string{"aaaaaaaaaa"}, "bbbb", 3, "bb…"},
		"no lines at all":      {nil, "status", 10, "status"},
		"no trailing at all":   {[]string{"a", "b"}, "", 10, "a\nb"},
	} {
		t.Run(name, func(t *testing.T) {
			got := joinWithinBudget(tc.lines, tc.trailing, tc.limit)
			if got != tc.want {
				t.Errorf("joinWithinBudget = %q, want %q", got, tc.want)
			}
			if n := utf8.RuneCountInString(got); n > tc.limit {
				t.Errorf("joinWithinBudget = %q, %d runes over the %d limit", got, n, tc.limit)
			}
			if strings.HasSuffix(got, "\n") || strings.HasPrefix(got, "\n") {
				t.Errorf("joinWithinBudget = %q, want no dangling newline", got)
			}
		})
	}
}

// A budget too small for even one meter still says something rather than
// rendering an empty tooltip, which the shell shows as no tooltip at all.
func TestJoinWithinBudgetKeepsTheTrailingLineWhenNoLineFits(t *testing.T) {
	got := joinWithinBudget([]string{"a very long meter line"}, "Polling…", 10)
	if got != "Polling…" {
		t.Errorf("joinWithinBudget = %q, want the status alone", got)
	}
}

// A stale reading is disclosed in full in the menu; the tooltip has room only
// for the failure itself, so it must be the failure and not the freshness.
func TestTooltipStatusStatesTheFailureFirst(t *testing.T) {
	sub := metering(poll.State{LastError: errors.New("boom")})
	got := presenterFor(t, i18n.LangEnUS).tooltipStatus(sub, now)
	if !strings.HasPrefix(got, "Unexpected failure") {
		t.Errorf("tooltip status = %q, want the failure", got)
	}
}
