// Package tray renders the notification-area presence. This file holds the
// pure formatting layer, deliberately separated from the platform glue so the
// user-visible text is testable on any host rather than only on Windows.
package tray

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/identity"
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
	// menuFieldGap separates a menu row's fields. Three spaces is what reads as a
	// column break in the shell's proportional menu font.
	menuFieldGap = "   "
	// menuRightAlign is the tab Windows treats as a column break in a menu item:
	// everything after it is drawn flush against the right edge of the menu. It is
	// what keeps the gauges in one column when the labels beside them differ in
	// length; padding with spaces cannot, because the menu font is proportional.
	menuRightAlign = "\t"
	// headerFieldGap joins the product name to the plan on the header row. They
	// read as one product name ("Claude Max"), so the separator is a plain space.
	headerFieldGap = " "
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
	// SetInterval applies a cadence chosen in the settings menu. The poller
	// enforces its own floor, so the UI cannot configure a faster one.
	SetInterval(interval time.Duration)
}

// Wiring is everything the tray needs from the rest of the app. It is a struct
// because the platform entry point takes all of it and would otherwise be a
// six-argument call whose order carries no meaning.
type Wiring struct {
	Config config.Config
	// Updates signals that the poller published a new state. It is a signal, not
	// a queue: the UI re-reads Controller.State(), so a coalesced send cannot
	// leave a stale reading on screen.
	Updates    <-chan struct{}
	Controller Controller
	Accounts   AccountReader
	// SaveSettings persists a changed document. It is a function so the tray
	// never learns where the config file lives, and a failure to write leaves the
	// running configuration untouched.
	SaveSettings func(config.Config) error
}

// AccountReader reports the account being monitored. It is consulted on every
// update rather than once at startup because the credential path — and therefore
// the account — is not known until the first successful poll.
//
// A nil account with a nil error means "not known yet". A non-nil error means the
// account cannot be shown at all; the caller keeps rendering quota figures either
// way, since neither case affects polling.
type AccountReader interface {
	Account() (*identity.Account, error)
}

// Row is one meter as presented in the menu: a name, a gauge, and its current
// figures. Bar is empty for a meter with no bounded percentage, such as an
// uncapped balance, where a gauge would imply a limit the vendor never stated.
type Row struct {
	Label  string
	Bar    string
	Detail string
}

// MenuRowTitle lays out one row as a single menu caption: the label and its
// figures on the left, the gauge pushed to the right edge. A field the meter does
// not have is skipped, so an uncapped balance shows no gap where a gauge would be,
// and a row with no gauge carries no column break at all.
//
// It lives here rather than in the platform glue because it is the one part of the
// menu's appearance that can be asserted on any host.
func MenuRowTitle(row Row) string {
	left := make([]string, 0, 2)
	for _, field := range []string{row.Label, row.Detail} {
		if field != "" {
			left = append(left, field)
		}
	}
	title := strings.Join(left, menuFieldGap)
	if row.Bar == "" {
		return title
	}
	return title + menuRightAlign + row.Bar
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

// HeaderText names what is being monitored, in one row: the provider and the
// plan. It returns an empty string before the first successful poll, so the row
// can be hidden rather than showing a bare separator.
//
// Who the account belongs to is deliberately not here — it is one level down, in
// DetailRows, because the row read at a glance should answer "which service and
// which allowance", not "which person".
func (p *Presenter) HeaderText(state poll.State) string {
	if state.Snapshot == nil {
		return ""
	}
	fields := make([]string, 0, 2)
	// Product is the vendor's own name for what is metered; Vendor is only a key,
	// so it stands in just when a provider states no product.
	if product := state.Snapshot.Product; product != "" {
		fields = append(fields, product)
	} else if state.Snapshot.Vendor != "" {
		fields = append(fields, capitalizeFirst(state.Snapshot.Vendor))
	}
	if state.Snapshot.Plan != "" {
		fields = append(fields, capitalizeFirst(state.Snapshot.Plan))
	}
	return strings.Join(fields, headerFieldGap)
}

// DetailRows are the account facts that answer "which subscription am I looking
// at" without crowding the first level of the menu. Rows for fields the vendor
// never supplied are omitted rather than rendered empty.
func (p *Presenter) DetailRows(account *identity.Account) []Row {
	if account == nil {
		return nil
	}
	rows := make([]Row, 0, 3)
	for _, field := range []struct {
		key   i18n.Key
		value string
	}{
		{i18n.AccountName, account.DisplayName},
		{i18n.AccountEmail, account.Email},
		{i18n.AccountOrganization, account.Organization},
	} {
		if field.value == "" {
			continue
		}
		rows = append(rows, Row{Label: p.catalog.Text(field.key), Detail: field.value})
	}
	return rows
}

// capitalizeFirst title-cases a vendor's plan label, which arrives lowercase
// ("max", "pro"). Only the first rune is touched: the rest is the vendor's own
// vocabulary and may carry capitals that matter.
func capitalizeFirst(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	if first == utf8.RuneError {
		return text
	}
	return string(unicode.ToUpper(first)) + text[size:]
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
			Bar:    meterBar(meter),
			Detail: p.detail(meter, now),
		})
	}
	return rows
}

// meterBar renders a gauge only for a meter whose percentage is bounded by an
// allowance. An uncapped balance reports a zero percent that means "no limit
// exists", not "nothing consumed", so it gets no gauge.
func meterBar(meter quota.Meter) string {
	switch m := meter.(type) {
	case *quota.Utilization:
		return progressBar(m.Percent)
	case *quota.Balance:
		if m.Limit == nil {
			return ""
		}
		return progressBar(m.Percent)
	}
	return ""
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
//
// Gauges are deliberately left out: at maxTooltipRunes, ten cells per meter
// would cost more of the budget than the figures themselves and push the status
// line out of the tooltip entirely.
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
