package tray

import (
	"fmt"
	"slices"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
)

// intervalPresets are the cadences the menu offers. The first is poll floor
// itself: nothing faster can be offered, because config.Validate rejects it and
// the poller would raise it anyway. The rest climb to an hour, past which the
// figures shown would be older than the five-hour window they describe.
var intervalPresets = []time.Duration{
	poll.DefaultInterval,
	10 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
}

// IntervalPresets lists the cadences a settings menu should offer, in ascending
// order.
func IntervalPresets() []time.Duration { return slices.Clone(intervalPresets) }

// alertPresets are the utilization figures the usage-alert submenus offer, and
// the one list both thresholds are chosen from: a warning and a critical level
// are read against each other, and two scales would make that comparison a
// conversion.
//
// It spans exactly what quota.Thresholds.Validate accepts — up to the ceiling,
// which is where a critical level means "only once it is spent" — and starts at
// the half mark, below which a threshold names a reading nobody escalates on.
// The step tightens from ten points to five above 70 because that is the part of
// the range the choice is actually made in; ten points apart down at 50 is a
// distinction nobody is drawing.
var alertPresets = []float64{50, 60, 70, 75, 80, 85, 90, 95, 100}

// WarnPresets and CriticalPresets are what each submenu offers. They are the
// same list with the one end each cannot hold removed: nothing can sit above
// the highest preset to keep a warning below it, and nothing below the lowest
// to keep a critical above it. Offering those two would put an item in the menu
// whose only possible outcome is a rejection, which is a worse answer than not
// offering it — a Windows menu item cannot explain why it is greyed out, since
// popup menus carry no per-item hover text.
func WarnPresets() []float64 { return slices.Clone(alertPresets[:len(alertPresets)-1]) }

func CriticalPresets() []float64 { return slices.Clone(alertPresets[1:]) }

// presetAbove and presetBelow find the adjacent preset in one direction, which
// is how far a threshold moves when the other one is set past it. Adjacent
// rather than proportional: the displaced setting ends up at the nearest value
// that keeps the pair valid, so the user recognizes the result as their own
// choice plus the least the app could do to it.
func presetAbove(percent float64) (float64, bool) {
	for _, preset := range alertPresets {
		if preset > percent {
			return preset, true
		}
	}
	return 0, false
}

func presetBelow(percent float64) (float64, bool) {
	for i := len(alertPresets) - 1; i >= 0; i-- {
		if alertPresets[i] < percent {
			return alertPresets[i], true
		}
	}
	return 0, false
}

// IntervalLabel names a cadence in whole minutes or whole hours. A duration that
// is neither falls back to Go's own formatting rather than being rounded into a
// label that misstates it.
func (p *Presenter) IntervalLabel(interval time.Duration) string {
	switch {
	case interval <= 0:
		return interval.String()
	case interval%time.Hour == 0:
		return p.catalog.Text(i18n.IntervalHours, int(interval/time.Hour))
	case interval%time.Minute == 0:
		return p.catalog.Text(i18n.IntervalMinutes, int(interval/time.Minute))
	default:
		return interval.String()
	}
}

// Every settings submenu is named by a row of this shape: the setting on the
// left, the value in force on the right. Reading a setting then costs no
// navigation — the submenu is only needed to change one — and the ticks inside
// it stay the control, so the row and the submenu can never disagree.
func (p *Presenter) IntervalRow() Row {
	return Row{
		Label:  p.catalog.Text(i18n.MenuInterval),
		Detail: p.IntervalLabel(time.Duration(p.cfg.PollInterval)),
	}
}

func (p *Presenter) LanguageRow() Row {
	return Row{
		Label:  p.catalog.Text(i18n.MenuLanguage),
		Detail: p.catalog.Lang().NativeName(),
	}
}

// UsageAlertsRow heads the group of consumption settings with both thresholds
// in force, ascending, so the pair is read where it is meant — against each
// other — without opening anything. The bullet marks them as two settings
// rather than one range, which is what they are: each is chosen on its own row.
func (p *Presenter) UsageAlertsRow() Row {
	alerts := p.cfg.UsageAlerts
	return Row{
		Label: p.catalog.Text(i18n.MenuUsageAlerts),
		Detail: percentText(alerts.WarnAtPercent) + preferenceSeparator +
			percentText(alerts.CriticalAtPercent),
	}
}

func (p *Presenter) WarnThresholdRow() Row {
	return Row{
		Label:  p.catalog.Text(i18n.MenuWarnThreshold),
		Detail: percentText(p.cfg.UsageAlerts.WarnAtPercent),
	}
}

func (p *Presenter) CriticalThresholdRow() Row {
	return Row{
		Label:  p.catalog.Text(i18n.MenuCriticalThreshold),
		Detail: percentText(p.cfg.UsageAlerts.CriticalAtPercent),
	}
}

// Config reports the settings this Presenter renders with, so the platform layer
// can derive a changed document from it without keeping a second copy.
func (p *Presenter) Config() config.Config { return p.cfg }

// WithInterval returns cfg with a new cadence, or an error naming why the value
// is unusable. It validates the whole document rather than the one field, so a
// change can never persist a document the next start would reject.
func WithInterval(cfg config.Config, interval time.Duration) (config.Config, error) {
	changed := cfg
	changed.PollInterval = config.Duration(interval)
	if err := changed.Validate(); err != nil {
		return cfg, fmt.Errorf("poll interval %s: %w", interval, err)
	}
	return changed, nil
}

// WithWarnThreshold and WithCriticalThreshold return cfg with one usage-alert
// threshold set, carrying the other one along when it has to move.
//
// The two thresholds are an ordered pair, and a menu cannot present an ordered
// pair as two independent choices without eventually being handed an invalid
// one. Rejecting that click is the worst of the options: the user picked a value
// the menu offered and got an error line instead. Neither is greying the item
// out, which a Windows popup menu cannot explain (§ no per-item tooltips).
//
// So the choice the user made is always taken, and the other threshold steps to
// the adjacent preset that keeps the pair valid — the same behaviour as the two
// handles of a range control, which is what this pair is. It is visible rather
// than silent: both parent rows carry their value in the right column, so the
// companion is seen to have moved on the way back out of the submenu.
func WithWarnThreshold(cfg config.Config, percent float64) (config.Config, error) {
	changed := cfg
	changed.UsageAlerts.WarnAtPercent = percent
	if changed.UsageAlerts.CriticalAtPercent <= percent {
		above, ok := presetAbove(percent)
		if !ok {
			return cfg, fmt.Errorf("warning threshold %s: no preset above it can carry the critical threshold",
				percentText(percent))
		}
		changed.UsageAlerts.CriticalAtPercent = above
	}
	if err := changed.Validate(); err != nil {
		return cfg, fmt.Errorf("warning threshold %s: %w", percentText(percent), err)
	}
	return changed, nil
}

func WithCriticalThreshold(cfg config.Config, percent float64) (config.Config, error) {
	changed := cfg
	changed.UsageAlerts.CriticalAtPercent = percent
	if changed.UsageAlerts.WarnAtPercent >= percent {
		below, ok := presetBelow(percent)
		if !ok {
			return cfg, fmt.Errorf("critical threshold %s: no preset below it can carry the warning threshold",
				percentText(percent))
		}
		changed.UsageAlerts.WarnAtPercent = below
	}
	if err := changed.Validate(); err != nil {
		return cfg, fmt.Errorf("critical threshold %s: %w", percentText(percent), err)
	}
	return changed, nil
}

// WithLanguage returns cfg with a new interface language.
func WithLanguage(cfg config.Config, lang i18n.Lang) (config.Config, error) {
	changed := cfg
	changed.Language = string(lang)
	if err := changed.Validate(); err != nil {
		return cfg, fmt.Errorf("language %s: %w", lang, err)
	}
	return changed, nil
}
