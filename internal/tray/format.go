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

// maxTooltipRunes is what the shell actually reads out of NOTIFYICONDATA.szTip.
// The field is declared as 128 WCHARs, but the shell only honours that for an
// icon that has announced itself through NIM_SETVERSION; systray never issues
// it, so the icon stays at the default version and the shell reads the legacy
// 64-WCHAR field — 63 characters plus the terminator. Text past that is
// discarded silently, mid-line, which is why the budget is enforced here.
const maxTooltipRunes = 63

// AppName is the product name, and the one user-visible string that resolves
// outside i18n because it is identical in every language. Windows announces it
// as the accessible name of the tray icon, and it heads the menu.
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
	// headerFieldGap joins the product name to the plan. They read as one product
	// name ("Claude Max"), so the separator is a plain space.
	headerFieldGap = " "
	// preferenceSeparator joins two independent settings into one row. Unlike
	// headerFieldGap the parts do not compose into a single name, so the bullet
	// marks them as a list rather than a phrase.
	preferenceSeparator = " • "
	// versionPrefix marks the release as a version rather than as a bare number
	// sitting in the value column of the heading row.
	versionPrefix = "v"
	// menuMnemonicMarker is the character Windows consumes to mark the next one as
	// an item's keyboard mnemonic. Doubling it is how a literal one is drawn.
	menuMnemonicMarker = '&'
)

// PollState aliases the poller's state so the platform-specific files in this
// package do not each need to import poll.
type PollState = poll.State

// Controller is one provider's poller as the UI sees it. State is read on every
// update rather than delivered through the channel, so a coalesced signal can
// never leave the UI showing a reading older than the poller's own.
type Controller interface {
	// Vendor is the provider's stable key. It answers before the first poll, so
	// the provider list can name a provider that has not reported yet.
	Vendor() string
	State() poll.State
	Refresh() bool
	// SetInterval applies a cadence chosen in the settings menu. The poller
	// enforces its own floor, so the UI cannot configure a faster one.
	SetInterval(interval time.Duration)
}

// CLIReader reports what a vendor's official CLI has recorded locally about the
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

// ProviderWiring is one monitored provider's two ports: the poller driving it,
// and the local documents describing the account that poller authenticates as.
// They are paired because every row the UI draws about a provider needs both,
// and pairing them is what keeps a second vendor from touching the menu's shape.
type ProviderWiring struct {
	Controller Controller
	CLI        CLIReader
}

// Wiring is everything the tray needs from the rest of the app. It is a struct
// because the platform entry point takes all of it and would otherwise be a
// five-argument call whose order carries no meaning.
type Wiring struct {
	Config config.Config
	// Updates signals that some poller published a new state. It is a signal, not
	// a queue, and it is shared by every provider: the UI re-reads each
	// Controller.State(), so neither a coalesced send nor a send from a provider
	// other than the one that changed can leave a stale reading on screen.
	Updates   <-chan struct{}
	Providers []ProviderWiring
	// SaveSettings persists a changed document. It is a function so the tray
	// never learns where the config file lives, and a failure to write leaves the
	// running configuration untouched.
	SaveSettings func(config.Config) error
}

// Subscription is one monitored provider as the UI sees it: the poller's last
// reading for it, together with what that vendor's own CLI recorded locally
// about the account being polled. Every per-provider renderer takes one, which
// is what makes a second vendor an extra element rather than a change to the
// menu's shape.
type Subscription struct {
	// Vendor is the provider's stable key, known before the first poll. It is
	// what lets the provider list name a provider that has not answered yet.
	Vendor string
	State  poll.State
	// Account and Preferences are nil until that vendor's CLI documents have been
	// read, and stay nil when they cannot be. Neither ever hides a quota figure.
	Account     *identity.Account
	Preferences *identity.Preferences
}

// Subscriptions is every monitored provider, in configuration order. The order
// is the user's own and is never sorted: it decides which provider holds the
// active position and the order of the provider list.
type Subscriptions []Subscription

// Active is the subscription the icon, the tooltip and the top of the menu
// describe.
//
// It is the first configured provider rather than, say, the busiest one, for a
// structural reason: stacking several providers' meters at the first level would
// need a separator between the groups, and a separator is the one widget systray
// cannot hide, so a provider with nothing to report would leave a divider above
// nothing. One provider holds that position and the rest are reached through the
// provider list, which grows without the first level changing shape.
//
// The zero Subscription stands in for an empty list, and renders as a menu that
// is present but has nothing to say yet.
func (s Subscriptions) Active() Subscription {
	if len(s) == 0 {
		return Subscription{}
	}
	return s[0]
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

// HeaderRow identifies the application: the product on the left, the release in
// the value column every other row uses. It names the app rather than whatever
// is being monitored, so what the menu is stays fixed while what it reports
// changes underneath — which is also what lets the first separator always have
// something above it, since systray can hide an item but never a separator.
func (p *Presenter) HeaderRow() Row {
	return Row{Label: AppName, Detail: versionPrefix + buildinfo.Version}
}

// ProviderRow names the subscription the rows under it describe: the vendor on
// the left, what it sells on the right. Splitting them is the only hierarchy a
// Windows menu affords — the vendor holds the position the eye lands on first
// and the product and plan sit in the value column — so "which service" and
// "which allowance" are read in one pass instead of parsed out of one phrase.
//
// It is also the row grammar the provider list reuses, which is what makes the
// active provider and its entry in that list read as the same thing.
func (p *Presenter) ProviderRow(sub Subscription) Row {
	vendor, plan := providerNames(sub)
	if vendor == "" {
		// A provider that named neither itself nor its product still gets one
		// legible row, rather than a plan floating alone in the value column.
		return Row{Label: plan}
	}
	return Row{Label: vendor, Detail: plan}
}

// providerNames splits a subscription into the two halves every provider row
// uses. The vendor key is the only half known before the first poll; the product
// and the plan are the vendor's own display text and arrive with the snapshot.
func providerNames(sub Subscription) (vendor, plan string) {
	vendor = capitalizeFirst(sub.Vendor)
	snapshot := sub.State.Snapshot
	if snapshot == nil {
		return vendor, ""
	}
	if vendor == "" {
		vendor = capitalizeFirst(snapshot.Vendor)
	}
	// Product is the vendor's own display name for what is metered. It qualifies
	// the vendor, and stands in as the left half for a provider that named no
	// vendor at all rather than repeating itself on both sides of the row.
	product := snapshot.Product
	if vendor == "" {
		vendor, product = product, ""
	}

	qualifiers := make([]string, 0, 2)
	if product != "" && product != vendor {
		qualifiers = append(qualifiers, product)
	}
	if snapshot.Plan != "" {
		qualifiers = append(qualifiers, capitalizeFirst(snapshot.Plan))
	}
	return vendor, strings.Join(qualifiers, headerFieldGap)
}

// ProviderListRow is a provider's entry in the provider list: its name alone.
// What the vendor sells is stated once, on the row that heads that provider's
// own submenu, rather than on both the row that opens it and the row behind it.
// The list is then a column of names, which is what a list is read for.
func (p *Presenter) ProviderListRow(sub Subscription) Row {
	vendor, plan := providerNames(sub)
	if vendor == "" {
		// A provider that named neither itself nor its product is listed by the
		// plan rather than by a blank row that opens a submenu.
		return Row{Label: plan}
	}
	return Row{Label: vendor}
}

// PreferencesRow says what the provider's CLI is configured to use. It carries
// no label of its own — the values name themselves — and sits flush with the
// meter rows below it rather than indented under the provider above: every row
// in the group starts at one margin, so the eye reads a single column of names
// instead of stepping in and back out.
//
// It is operational state, not account detail, which is why it stays on the
// first level of the menu rather than moving into the provider's own submenu.
func (p *Presenter) PreferencesRow(sub Subscription) Row {
	return Row{Label: p.preferencesSummary(sub.Preferences)}
}

// preferencesSummary joins the configured model and effort into one phrase, and
// yields whichever is present when the other is not.
//
// Both values are title-cased. They arrive from the CLI's settings document in
// whatever case the user typed, and a menu row is a label rather than a quotation
// of that file: dropping "opus" beside a column of capitalized window names reads
// as a defect. The effort is additionally interpolated into a catalogue entry,
// because on its own the value the CLI records names no setting at all.
func (p *Presenter) preferencesSummary(prefs *identity.Preferences) string {
	if prefs == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if prefs.Model != "" {
		parts = append(parts, capitalizeFirst(prefs.Model))
	}
	if prefs.EffortLevel != "" {
		parts = append(parts, p.catalog.Text(i18n.EffortLevel, capitalizeFirst(prefs.EffortLevel)))
	}
	return strings.Join(parts, preferenceSeparator)
}

// AccountRows describe whose subscription one provider is. They are the only
// rows in a provider's own submenu: everything a user reads at a glance — the
// meters, the configured model — is operational state and stays on the first
// level, while the fields that identify an account are consulted rarely and
// would be read by anyone watching a shared screen.
//
// Rows for fields the CLI never recorded are omitted rather than rendered empty,
// and a nil account yields none at all.
func (p *Presenter) AccountRows(sub Subscription) []Row {
	if sub.Account == nil {
		return nil
	}
	fields := []struct {
		key   i18n.Key
		value string
	}{
		{i18n.AccountName, sub.Account.DisplayName},
		{i18n.AccountEmail, sub.Account.Email},
		{i18n.AccountOrganization, sub.Account.Organization},
	}

	// Left nil when nothing has a value: the submenu hides on an empty result.
	var rows []Row
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		rows = append(rows, Row{Label: p.catalog.Text(field.key), Detail: field.value})
	}
	return rows
}

// maxAccountRows is the most rows AccountRows can produce, one per field of
// identity.Account. The platform layer pre-allocates exactly this many rows per
// provider and can never add another, so a test asserts the ceiling rather than
// trusting it.
const maxAccountRows = 3

// capitalizeFirst title-cases the values that reach a caption lowercase: a vendor
// key, a plan label, a configured model or effort ("anthropic", "max", "opus",
// "high"). Only the first rune is touched, because the rest is a vendor's own
// vocabulary or a user's own configuration and may carry capitals that matter.
func capitalizeFirst(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	if first == utf8.RuneError {
		return text
	}
	return string(unicode.ToUpper(first)) + text[size:]
}

// MeterRows renders one line per meter in the last known snapshot, in the order
// the provider reported them. It returns nil before the first successful poll.
func (p *Presenter) MeterRows(sub Subscription, now time.Time) []Row {
	if sub.State.Snapshot == nil {
		return nil
	}
	rows := make([]Row, 0, len(sub.State.Snapshot.Meters))
	var stated time.Time
	for _, meter := range sub.State.Snapshot.Meters {
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
		return percentText(m.Percent)
	case *quota.Balance:
		if m.Limit != nil {
			return p.catalog.Text(i18n.BalanceUsedOfLimit, m.Used, *m.Limit)
		}
		return m.Used.String()
	}
	return ""
}

// percentText renders a percentage at the whole-point precision every figure in
// this UI is read at, for a meter's own reading and for the thresholds it is
// escalated against alike — the two are compared by eye, so they are written
// the same way.
//
// The percent sign is not translated: it is universal in both catalogues, and a
// sign lost in a future translation would corrupt the figure rather than reword
// it.
func percentText(percent float64) string {
	return fmt.Sprintf("%.0f%%", percent)
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
func (p *Presenter) StatusText(sub Subscription, now time.Time) string {
	if sub.State.LastError != nil {
		text := p.humanizeError(sub.State.LastError)
		if sub.State.IsStale() {
			text += fieldSeparator + p.catalog.Text(i18n.StatusStale, p.countdown(now.Sub(sub.State.UpdatedAt)))
		}
		return text
	}
	if sub.State.Snapshot == nil {
		return p.catalog.Text(i18n.StatusFirstPoll)
	}
	return p.catalog.Text(i18n.StatusUpdated,
		p.countdown(now.Sub(sub.State.UpdatedAt)),
		p.countdown(sub.State.NextPollAt.Sub(now)))
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

// Tooltip renders the hover text for the active subscription.
//
// It is a different composition from the menu rather than a copy of it, because
// maxTooltipRunes leaves room for roughly two menu rows. What survives is the
// pair a glance is for — which window, how full — so the reset countdown and the
// gauges are dropped: at this budget ten cells per meter would cost more than
// the figures themselves, and a countdown would cost the second meter entirely.
//
// A meter line is included whole or not at all. Cutting one mid-way removes the
// figure at the end of it and leaves a line that reads exactly like a meter that
// reported none, which is the one misreading this text must not produce.
func (p *Presenter) Tooltip(sub Subscription, now time.Time) string {
	var lines []string
	if snapshot := sub.State.Snapshot; snapshot != nil {
		lines = make([]string, 0, len(snapshot.Meters))
		for _, meter := range snapshot.Meters {
			label := p.catalog.MeterLabel(meter.ID(), meter.Label())
			lines = append(lines, label+labelDetailSeparator+p.figure(meter))
		}
	}

	// The trailing status line is prose and is elided instead of dropped: a
	// sentence still reads when cut short, and on a failure it is the one line
	// that explains why the figures above it stopped moving.
	return joinWithinBudget(lines, p.tooltipStatus(sub, now), maxTooltipRunes)
}

// tooltipStatus is the status line at tooltip width. A healthy poll states only
// how old the reading is, since the cadence that produced it is one settings
// submenu away and does not survive the budget; a failure states itself in full
// and is elided if it has to be.
func (p *Presenter) tooltipStatus(sub Subscription, now time.Time) string {
	if sub.State.LastError != nil || sub.State.Snapshot == nil {
		return p.StatusText(sub, now)
	}
	return p.catalog.Text(i18n.StatusUpdatedBrief, p.countdown(now.Sub(sub.State.UpdatedAt)))
}

// joinWithinBudget assembles the tooltip under a hard rune limit: whole lines
// first, in the order they were given, then the trailing line in whatever room
// is left. A line that does not fit whole stops the loop rather than being
// skipped, because the lines are ordered by importance and showing a later one
// in place of an earlier one would misrepresent which meter is which.
func joinWithinBudget(lines []string, trailing string, limit int) string {
	var text strings.Builder
	used := 0
	for _, line := range lines {
		cost := utf8.RuneCountInString(line)
		if used > 0 {
			cost++ // the newline that joins it to the line above
		}
		if used+cost > limit {
			break
		}
		if used > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(line)
		used += cost
	}

	if trailing == "" {
		return text.String()
	}
	room := limit - used
	if used > 0 {
		room-- // the newline that joins it to the block above
	}
	// Below two runes there is room for the ellipsis alone, which says nothing.
	if room < 2 {
		return text.String()
	}
	if used > 0 {
		text.WriteByte('\n')
	}
	text.WriteString(truncateRunes(trailing, room))
	return text.String()
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

// IconState reduces a subscription to the three inputs the icon renderer takes.
// A state with no snapshot at all still renders an icon, empty and greyed, so
// the app is visibly present while it is still starting up.
func (p *Presenter) IconState(sub Subscription) (percent float64, level quota.Severity, stale bool) {
	if sub.State.Snapshot == nil {
		return 0, quota.SeverityNormal, true
	}
	primary := sub.State.Snapshot.Primary()
	if primary == nil {
		return 0, quota.SeverityNormal, true
	}
	switch m := primary.(type) {
	case *quota.Utilization:
		percent = m.Percent
	case *quota.Balance:
		percent = m.Percent
	}
	return percent, p.cfg.UsageAlerts.SeverityFor(percent, primary.Severity()), sub.State.LastError != nil
}
