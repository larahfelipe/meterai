// Package tray renders the notification-area presence. This file holds the
// pure formatting layer, deliberately separated from the platform glue so the
// user-visible text is testable on any host rather than only on Windows.
package tray

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

// maxTooltipRunes is the Shell_NotifyIcon limit: NOTIFYICONDATA.szTip holds 128
// WCHARs including the terminator. Exceeding it truncates unpredictably or
// drops the tooltip entirely depending on the shell version, so the text is
// bounded here instead.
const maxTooltipRunes = 127

const (
	ellipsis = "…"
	// fieldSeparator joins a message to the fragment that follows it. Catalogue
	// entries carry no padding of their own, so the spacing lives here and stays
	// identical in every language.
	fieldSeparator = " "
	// labelDetailSeparator sets a meter's figures apart from its name inside one
	// tooltip line, where a single space reads as part of the label.
	labelDetailSeparator = "  "
)

// PollState aliases the poller's state so the platform-specific files in this
// package do not each need to import poll.
type PollState = poll.State

// Controller is the poller as the UI sees it. State is read on every update
// rather than delivered through the channel, so a coalesced signal can never
// leave the UI showing a reading older than the poller's own.
type Controller interface {
	State() poll.State
	Refresh() bool
}

// Row is one meter as presented in the menu: a name and its current figures.
type Row struct {
	Label  string
	Detail string
}

// Presenter turns poll state into the text and icon inputs the platform layer
// draws. It holds the two things every rendering decision needs — the local
// thresholds and the active language — so the platform glue carries neither.
type Presenter struct {
	cfg     config.Config
	catalog *i18n.Catalog
}

// NewPresenter builds a Presenter for a validated config.
func NewPresenter(cfg config.Config) *Presenter {
	return &Presenter{cfg: cfg, catalog: cfg.Catalog()}
}

// Rows renders one line per meter in the last known snapshot, in the order the
// provider reported them. It returns nil before the first successful poll.
func (p *Presenter) Rows(state poll.State, now time.Time) []Row {
	if state.Snapshot == nil {
		return nil
	}
	rows := make([]Row, 0, len(state.Snapshot.Meters))
	for _, meter := range state.Snapshot.Meters {
		rows = append(rows, Row{
			Label:  p.catalog.MeterLabel(meter.ID(), meter.Label()),
			Detail: p.detail(meter, now),
		})
	}
	return rows
}

func (p *Presenter) detail(meter quota.Meter, now time.Time) string {
	var text string
	switch m := meter.(type) {
	case *quota.Utilization:
		// The percent sign is not translated: it is universal in both catalogues
		// and a verb lost in a future translation would corrupt the figure.
		text = fmt.Sprintf("%.0f%%", m.Percent)
	case *quota.Balance:
		if m.Limit != nil {
			text = p.catalog.Text(i18n.BalanceUsedOfLimit, m.Used, *m.Limit)
		} else {
			text = m.Used.String()
		}
	}
	if reset := meter.ResetsAt(); !reset.IsZero() {
		text += fieldSeparator + p.catalog.Text(i18n.MeterResetSuffix, p.countdown(reset.Sub(now)))
	}
	return text
}

// countdown renders a duration at the precision a user actually reads: minutes
// near the boundary, hours and minutes within a day, days beyond it.
func (p *Presenter) countdown(d time.Duration) string {
	switch {
	case d <= 0:
		return p.catalog.Text(i18n.CountdownNow)
	case d < time.Minute:
		return p.catalog.Text(i18n.CountdownUnderMinute)
	case d < time.Hour:
		return p.catalog.Text(i18n.CountdownMinutes, int(d.Minutes()))
	case d < 24*time.Hour:
		hours := int(d.Hours())
		return p.catalog.Text(i18n.CountdownHours, hours, int(d.Minutes())-hours*60)
	default:
		hours := int(d.Hours())
		return p.catalog.Text(i18n.CountdownDays, hours/24, hours%24)
	}
}

// StatusText is the single line describing the health of polling itself, as
// opposed to the quota figures.
func (p *Presenter) StatusText(state poll.State, now time.Time) string {
	if state.LastError != nil {
		text := p.humanizeError(state.LastError)
		if state.IsStale() {
			text += fieldSeparator + p.catalog.Text(i18n.StatusStale, p.countdown(now.Sub(state.UpdatedAt)))
		}
		return text
	}
	if state.Snapshot == nil {
		return p.catalog.Text(i18n.StatusFirstPoll)
	}
	return p.catalog.Text(i18n.StatusUpdated,
		p.countdown(now.Sub(state.UpdatedAt)),
		p.countdown(state.NextPollAt.Sub(now)))
}

// humanizeError maps a failure to what the user can do about it. The
// distinction that matters is whether waiting will fix it.
func (p *Presenter) humanizeError(err error) string {
	var fe *quota.FetchError
	if !errors.As(err, &fe) {
		return p.catalog.Text(i18n.ErrorUnexpected)
	}
	switch fe.Kind {
	case quota.Unauthorized:
		return p.catalog.Text(i18n.ErrorUnauthorized)
	case quota.RateLimited:
		return p.catalog.Text(i18n.ErrorRateLimited)
	case quota.Transient:
		return p.catalog.Text(i18n.ErrorTransient)
	case quota.Protocol:
		return p.catalog.Text(i18n.ErrorProtocol)
	default:
		// A kind added by a newer vendor integration and not yet handled here:
		// generic, but still distinguishable from a failure that carried no kind.
		return p.catalog.Text(i18n.ErrorUnrecognized)
	}
}

// Tooltip renders the hover text. Every line is included until the platform
// limit is reached, so the most important meters must come first, which is why
// provider order is preserved rather than sorted.
func (p *Presenter) Tooltip(state poll.State, now time.Time) string {
	lines := make([]string, 0, 4)
	if state.Snapshot != nil {
		for _, row := range p.Rows(state, now) {
			lines = append(lines, row.Label+labelDetailSeparator+row.Detail)
		}
	}
	lines = append(lines, p.StatusText(state, now))
	return truncateRunes(strings.Join(lines, "\n"), maxTooltipRunes)
}

// truncateRunes cuts on a rune boundary. Cutting on a byte boundary would
// split the multi-byte characters that appear in every label here.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + ellipsis
}

// IconState reduces a poll state to the three inputs the icon renderer takes.
// A state with no snapshot at all still renders an icon, empty and greyed, so
// the app is visibly present while it is still starting up.
func (p *Presenter) IconState(state poll.State) (percent float64, level quota.Severity, stale bool) {
	if state.Snapshot == nil {
		return 0, quota.SeverityNormal, true
	}
	primary := state.Snapshot.Primary()
	if primary == nil {
		return 0, quota.SeverityNormal, true
	}
	switch m := primary.(type) {
	case *quota.Utilization:
		percent = m.Percent
	case *quota.Balance:
		percent = m.Percent
	}
	return percent, p.cfg.SeverityFor(percent, primary.Severity()), state.LastError != nil
}
