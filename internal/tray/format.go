// Package tray renders the notification-area presence. This file holds the
// pure formatting layer, deliberately separated from the platform glue so the
// user-visible text is testable on any host rather than only on Windows.
package tray

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"meterAI/internal/config"
	"meterAI/internal/poll"
	"meterAI/internal/quota"
)

// maxTooltipRunes is the Shell_NotifyIcon limit: NOTIFYICONDATA.szTip holds 128
// WCHARs including the terminator. Exceeding it truncates unpredictably or
// drops the tooltip entirely depending on the shell version, so the text is
// bounded here instead.
const maxTooltipRunes = 127

const ellipsis = "…"

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

// Rows renders one line per meter in the last known snapshot, in the order the
// provider reported them. It returns nil before the first successful poll.
func Rows(state poll.State, now time.Time) []Row {
	if state.Snapshot == nil {
		return nil
	}
	rows := make([]Row, 0, len(state.Snapshot.Meters))
	for _, meter := range state.Snapshot.Meters {
		rows = append(rows, Row{Label: meter.Label(), Detail: detail(meter, now)})
	}
	return rows
}

func detail(meter quota.Meter, now time.Time) string {
	var text string
	switch m := meter.(type) {
	case *quota.Utilization:
		text = fmt.Sprintf("%.0f%%", m.Percent)
	case *quota.Balance:
		if m.Limit != nil {
			text = fmt.Sprintf("%s de %s", m.Used, *m.Limit)
		} else {
			text = m.Used.String()
		}
	}
	if reset := meter.ResetsAt(); !reset.IsZero() {
		text += " · reset em " + formatCountdown(reset.Sub(now))
	}
	return text
}

// formatCountdown renders a duration at the precision a user actually reads:
// minutes near the boundary, hours and minutes within a day, days beyond it.
func formatCountdown(d time.Duration) string {
	switch {
	case d <= 0:
		return "agora"
	case d < time.Minute:
		return "<1min"
	case d < time.Hour:
		return fmt.Sprintf("%dmin", int(d.Minutes()))
	case d < 24*time.Hour:
		hours := int(d.Hours())
		return fmt.Sprintf("%dh%02d", hours, int(d.Minutes())-hours*60)
	default:
		hours := int(d.Hours())
		return fmt.Sprintf("%dd%02dh", hours/24, hours%24)
	}
}

// StatusText is the single line describing the health of polling itself, as
// opposed to the quota figures.
func StatusText(state poll.State, now time.Time) string {
	if state.LastError != nil {
		text := humanizeError(state.LastError)
		if state.IsStale() {
			text += fmt.Sprintf(" (dados de %s atrás)", formatCountdown(now.Sub(state.UpdatedAt)))
		}
		return text
	}
	if state.Snapshot == nil {
		return "Consultando…"
	}
	return fmt.Sprintf("Atualizado há %s · próxima em %s",
		formatCountdown(now.Sub(state.UpdatedAt)),
		formatCountdown(state.NextPollAt.Sub(now)))
}

// humanizeError maps a failure to what the user can do about it. The
// distinction that matters is whether waiting will fix it.
func humanizeError(err error) string {
	var fe *quota.FetchError
	if !errors.As(err, &fe) {
		return "Falha inesperada ao consultar"
	}
	switch fe.Kind {
	case quota.Unauthorized:
		return "Credencial expirada — rode `claude` uma vez para renovar"
	case quota.RateLimited:
		return "Limite de requisições atingido — aguardando"
	case quota.Transient:
		return "Sem resposta da API — tentando novamente"
	case quota.Protocol:
		return "A API mudou de formato — o app precisa ser atualizado"
	default:
		return "Falha ao consultar"
	}
}

// Tooltip renders the hover text. Every line is included until the platform
// limit is reached, so the most important meters must come first, which is why
// provider order is preserved rather than sorted.
func Tooltip(state poll.State, now time.Time) string {
	lines := make([]string, 0, 4)
	if state.Snapshot != nil {
		for _, row := range Rows(state, now) {
			lines = append(lines, row.Label+"  "+row.Detail)
		}
	}
	lines = append(lines, StatusText(state, now))
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
func IconState(state poll.State, cfg config.Config) (percent float64, level quota.Severity, stale bool) {
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
	return percent, cfg.SeverityFor(percent, primary.Severity()), state.LastError != nil
}
