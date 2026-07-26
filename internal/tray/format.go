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

	"github.com/larahfelipe/meterai/internal/buildinfo"
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

// AppName is the product name, and the one user-visible string that resolves
// outside i18n because it is identical in every language. Windows announces it as
// the accessible name of the tray icon, and it heads the menu until a provider
// has been reached.
const AppName = buildinfo.Name

const (
	ellipsis = "…"
	// fieldSeparator joins a message to the fragment that follows it. Catalogue
	// entries carry no padding of their own, so the spacing lives here and stays
	// identical in every language.
	fieldSeparator = " "
	// labelDetailSeparator sets a meter's figures apart from its name inside one
	// tooltip line, where a single space reads as part of the label.
	labelDetailSeparator = "  "
	// menuRightAlign is the tab Windows treats as a column break in a menu item:
	// everything after it is drawn flush against the right edge of the menu. It is
	// what keeps every value in one column when the labels beside them differ in
	// length; padding with spaces cannot, because the menu font is proportional.
	//
	// Every row in the menu obeys the same grammar because of it: what it is on
	// the left, what it currently reads on the right.
	menuRightAlign = "\t"
	// menuValueGap keeps a figure and its gauge apart inside the right column
	// while still reading as one component. Both are flush right, so a gauge of
	// fixed width puts the figures that precede it in a column of their own with
	// no padding: their right edges land at the same offset whatever the value.
	menuValueGap = "  "
	// menuContinuationIndent subordinates a row to the one above it. Windows
	// menus offer no other way to express depth at the same level: there is no
	// per-item font weight, size or colour without owner-draw, which systray does
	// not do.
	menuContinuationIndent = "    "
	// headerFieldGap joins the product name to the plan. They read as one product
	// name ("Claude Max"), so the separator is a plain space.
	headerFieldGap = " "
	// menuMnemonicMarker is the character Windows consumes to mark the next one as
	// an item's keyboard mnemonic. Doubling it is how a literal one is drawn.
	menuMnemonicMarker = '&'
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
	CLI        CLIReader
	// SaveSettings persists a changed document. It is a function so the tray
	// never learns where the config file lives, and a failure to write leaves the
	// running configuration untouched.
	SaveSettings func(config.Config) error
}

// CLIReader reports what the official CLI has recorded locally about the
// installation being polled. It is consulted on every update rather than once at
// startup because the credential path — and therefore which installation is being
// described — is not known until the first successful poll.
//
// A nil result with a nil error means "not known yet". A non-nil error means that
// document cannot be shown at all; the caller keeps rendering quota figures in
// either case, since neither affects polling, and one document failing never hides
// the other.
type CLIReader interface {
	Account() (*identity.Account, error)
	// Preferences reports the configured model and effort. They are the user-level
	// default, not the model a session is running: the CLI resolves that per
	// session from sources this app cannot observe.
	Preferences() (*identity.Preferences, error)
}

// Row is one line of the menu under a single grammar: Label names the thing on
// the left, Detail is its current value on the right, and Bar is the gauge drawn
// beside that value. Bar is empty for anything with no bounded percentage — an
// uncapped balance, a setting, an account field — where a gauge would imply a
// limit that does not exist.
type Row struct {
	Label  string
	Detail string
	Bar    string
}

// MenuRowTitle lays out one row as a single menu caption: the label on the left,
// the value and its gauge flush right. A row with nothing to put on the right
// carries no column break at all, because an empty right column widens every
// other row in the same menu.
//
// Every field is neutralized on the way through, since this is the last point
// before an account name, an organization or a vendor's own meter label reaches
// the shell.
//
// It lives here rather than in the platform glue because it is the one part of the
// menu's appearance that can be asserted on any host.
func MenuRowTitle(row Row) string {
	right := sanitizeMenuField(row.Detail)
	if row.Bar != "" {
		if right != "" {
			right += menuValueGap
		}
		right += sanitizeMenuField(row.Bar)
	}
	label := sanitizeMenuField(row.Label)
	if right == "" {
		return label
	}
	return label + menuRightAlign + right
}

// sanitizeMenuField makes one field safe to hand to a Win32 menu item. A title
// reaches the shell verbatim, where an ampersand selects the character after it
// as the item's keyboard mnemonic and vanishes from the caption, a tab opens a
// column break of its own, and a bidirectional format character reorders the row
// around it. None of that may be reachable from a document this app only reads.
func sanitizeMenuField(field string) string {
	var safe strings.Builder
	safe.Grow(len(field))
	for _, r := range field {
		switch {
		case r == menuMnemonicMarker:
			safe.WriteRune(menuMnemonicMarker)
			safe.WriteRune(menuMnemonicMarker)
		case unicode.IsControl(r):
			// Includes the tab: the only column break in a caption is the one
			// MenuRowTitle puts there.
			safe.WriteRune(' ')
		case unicode.Is(unicode.Cf, r):
			// Format characters draw nothing and can reorder what follows them.
		default:
			safe.WriteRune(r)
		}
	}
	return safe.String()
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

// HeaderRow names what is being monitored: the provider on the left, the plan it
// qualifies on the right. Splitting them is the only hierarchy a Windows menu
// affords — the provider holds the position the eye lands on first and the plan
// sits in the value column every other row uses, so "which service" and "which
// allowance" are read in one pass instead of being parsed out of one phrase.
//
// Before a provider has been reached it names the app itself. The row cannot be
// left empty: systray can hide an item but not a separator, so an empty heading
// would open the menu with a divider above nothing.
func (p *Presenter) HeaderRow(state poll.State) Row {
	if state.Snapshot == nil {
		return Row{Label: AppName}
	}
	// Vendor is only a persisted key, but it is the provider's name; Product is
	// the vendor's own display name for what is metered and stands in when a
	// provider states no vendor at all.
	brand := capitalizeFirst(state.Snapshot.Vendor)
	if brand == "" {
		brand = state.Snapshot.Product
	}

	qualifiers := make([]string, 0, 2)
	if product := state.Snapshot.Product; product != "" && product != brand {
		qualifiers = append(qualifiers, product)
	}
	if state.Snapshot.Plan != "" {
		qualifiers = append(qualifiers, capitalizeFirst(state.Snapshot.Plan))
	}
	plan := strings.Join(qualifiers, headerFieldGap)

	switch {
	case brand != "":
		return Row{Label: brand, Detail: plan}
	case plan != "":
		// A provider that named neither itself nor its product still gets one legible
		// row, rather than a plan floating alone in the value column.
		return Row{Label: plan}
	default:
		return Row{Label: AppName}
	}
}

// AccountRow subordinates the account to the header above it: whose subscription
// this is, indented, with no label of its own because a person's name needs none.
// It is a Row so it passes through the same sanitizing as every other caption.
//
// The e-mail deliberately stays one level down. The name is what identifies the
// account at a glance, and an address left permanently on screen is read by
// everyone watching a shared screen.
func (p *Presenter) AccountRow(account *identity.Account) Row {
	headline := accountHeadline(account)
	if headline == "" {
		return Row{}
	}
	return Row{Label: menuContinuationIndent + headline}
}

// accountHeadline is the one account field shown at the first level of the menu.
// The e-mail stands in when the CLI cached no name, which is the case for an
// account that has never had one set.
func accountHeadline(account *identity.Account) string {
	if account == nil {
		return ""
	}
	if account.DisplayName != "" {
		return account.DisplayName
	}
	return account.Email
}

// DetailRows describe the installation being polled without crowding the first
// level of the menu: who the subscription belongs to, then what the CLI is
// configured to prefer. Rows for fields the CLI never recorded are omitted rather
// than rendered empty, and so is whatever AccountRow already shows: the same value
// twice in two levels of one menu reads as a bug.
//
// Either argument may be nil, which is the state before the credential path is
// known and after a document that could not be read.
//
// The two preference values are shown verbatim. They are configuration the user
// wrote, so changing their case would make the menu disagree with the file it is
// reporting — which is the one thing a reader consults this row to check.
func (p *Presenter) DetailRows(account *identity.Account, prefs *identity.Preferences) []Row {
	shown := accountHeadline(account)
	fields := make([]detailField, 0, maxDetailRows+1)
	if account != nil {
		fields = append(fields,
			detailField{i18n.AccountName, account.DisplayName},
			detailField{i18n.AccountEmail, account.Email},
			detailField{i18n.AccountOrganization, account.Organization},
		)
	}
	if prefs != nil {
		fields = append(fields,
			detailField{i18n.PreferredModel, prefs.Model},
			detailField{i18n.PreferredEffort, prefs.EffortLevel},
		)
	}

	// Left nil when nothing has a value: the submenu hides on an empty result.
	var rows []Row
	for _, field := range fields {
		if field.value == "" || field.value == shown {
			continue
		}
		rows = append(rows, Row{Label: p.catalog.Text(field.key), Detail: field.value})
	}
	return rows
}

// detailField pairs a caption key with the value it labels, so the fields of two
// separate documents can be assembled into one ordered list.
type detailField struct {
	key   i18n.Key
	value string
}

// maxDetailRows is the most rows DetailRows can produce: the account contributes
// at most two, because one of its three fields always heads the menu instead, and
// the settings document contributes two. The platform layer pre-allocates exactly
// this many submenu rows and can never add another, so a test asserts the ceiling
// rather than trusting it.
const maxDetailRows = 4

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
	var stated time.Time
	for _, meter := range state.Snapshot.Meters {
		// Windows that empty together say so once. Anthropic reports every weekly
		// window with the same reset instant, and three consecutive rows repeating
		// one countdown bury the figures they exist to show; the rows without it read
		// as belonging to the one above, which is what they are.
		reset := meter.ResetsAt()
		statesReset := !reset.IsZero() && !reset.Equal(stated)
		stated = reset

		rows = append(rows, Row{
			Label:  p.meterTitle(meter, now, statesReset),
			Detail: p.figure(meter),
			Bar:    meterBar(meter),
		})
	}
	return rows
}

// meterTitle names a window and, unless the row above already did, says when it
// resets. Both belong on the left, away from the figure: they are what the row is
// about, read once, while the figure is what changes between polls.
func (p *Presenter) meterTitle(meter quota.Meter, now time.Time, statesReset bool) string {
	label := p.catalog.MeterLabel(meter.ID(), meter.Label())
	if !statesReset {
		return label
	}
	countdown := p.countdown(meter.ResetsAt().Sub(now))
	return label + fieldSeparator + p.catalog.Text(i18n.MeterResetSuffix, countdown)
}

// figure is the single reading the row exists to show, and the only field in it
// that shares the right column with the gauge.
func (p *Presenter) figure(meter quota.Meter) string {
	switch m := meter.(type) {
	case *quota.Utilization:
		// The percent sign is not translated: it is universal in both catalogues
		// and a verb lost in a future translation would corrupt the figure.
		return fmt.Sprintf("%.0f%%", m.Percent)
	case *quota.Balance:
		if m.Limit != nil {
			return p.catalog.Text(i18n.BalanceUsedOfLimit, m.Used, *m.Limit)
		}
		return m.Used.String()
	}
	return ""
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
