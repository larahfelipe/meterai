package tray

import (
	"fmt"
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
func IntervalPresets() []time.Duration {
	presets := make([]time.Duration, len(intervalPresets))
	copy(presets, intervalPresets)
	return presets
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

// WithLanguage returns cfg with a new interface language.
func WithLanguage(cfg config.Config, lang i18n.Lang) (config.Config, error) {
	changed := cfg
	changed.Language = string(lang)
	if err := changed.Validate(); err != nil {
		return cfg, fmt.Errorf("language %s: %w", lang, err)
	}
	return changed, nil
}
