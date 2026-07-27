//go:build windows

package tray

import (
	"bytes"
	"context"
	"time"

	"fyne.io/systray"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/quota"
	"github.com/larahfelipe/meterai/internal/trayicon"
)

// maxMeterRows bounds the pre-allocated meter entries. systray can add items but
// never remove them, so the rows are created once at startup and hidden while
// unused; a provider reporting more meters than this shows the first
// maxMeterRows, which is why provider order is significant.
const maxMeterRows = 6

// Run displays the tray icon and blocks until the user quits or ctx is
// cancelled. It must be called from the main goroutine: systray locks the OS
// thread that owns the Windows message loop.
func Run(ctx context.Context, wiring Wiring) error {
	systray.Run(func() { onReady(ctx, wiring) }, func() {
		// Nothing to undo here: systray removes the icon itself, and the pollers
		// and the single-instance guard are unwound by the caller once Run
		// returns.
	})
	return nil
}

func onReady(ctx context.Context, wiring Wiring) {
	systray.SetTitle(AppName)
	view := newMenuView(wiring)

	// Choices arrive on their own channels because one select cannot span a
	// variable number of ClickedCh. A forwarding goroutine per item, all unwound
	// by ctx, keeps the event loop a fixed shape.
	intervalChosen := make(chan time.Duration)
	languageChosen := make(chan i18n.Lang)
	warnChosen := make(chan float64)
	criticalChosen := make(chan float64)
	for i, preset := range IntervalPresets() {
		forwardChoice(ctx, view.intervalItems[i].ClickedCh, intervalChosen, preset)
	}
	for i, lang := range i18n.Available() {
		forwardChoice(ctx, view.languageItems[i].ClickedCh, languageChosen, lang)
	}
	for i, preset := range WarnPresets() {
		forwardChoice(ctx, view.warnItems[i].ClickedCh, warnChosen, preset)
	}
	for i, preset := range CriticalPresets() {
		forwardChoice(ctx, view.criticalItems[i].ClickedCh, criticalChosen, preset)
	}

	view.apply(time.Now())

	go func() {
		for {
			select {
			case <-ctx.Done():
				systray.Quit()
				return
			case <-view.quit.ClickedCh:
				systray.Quit()
				return
			case <-view.refresh.ClickedCh:
				if !view.refreshAll() {
					// Replacing the status line acknowledges the click, which
					// would otherwise look like nothing happened.
					view.status.SetTitle(view.presenter.catalog.Text(i18n.RefreshRejected))
				}
			case interval := <-intervalChosen:
				view.changeSettings(WithInterval(view.presenter.Config(), interval))
			case lang := <-languageChosen:
				view.changeSettings(WithLanguage(view.presenter.Config(), lang))
			case percent := <-warnChosen:
				view.changeSettings(WithWarnThreshold(view.presenter.Config(), percent))
			case percent := <-criticalChosen:
				view.changeSettings(WithCriticalThreshold(view.presenter.Config(), percent))
			case <-wiring.Updates:
				view.apply(time.Now())
			}
		}
	}()
}

// forwardChoice relays one menu item's clicks as the value that item stands for.
// The goroutine lives as long as the item, which is the whole process, so it
// exits only with ctx.
func forwardChoice[T any](ctx context.Context, clicked <-chan struct{}, chosen chan<- T, value T) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-clicked:
				select {
				case chosen <- value:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

// providerEntry is one provider's row in the provider list, and the account
// submenu behind it. Exactly one is allocated per configured provider, so a
// second vendor adds an entry to this slice and changes nothing else about the
// menu.
type providerEntry struct {
	// row opens the submenu and carries the provider's own name and plan, the
	// same grammar the active provider uses at the first level.
	row *systray.MenuItem
	// context repeats that name inside the submenu, so a list of several
	// providers cannot leave the user reading account fields with no idea whose.
	context     *systray.MenuItem
	accountRows []*systray.MenuItem
}

// menuView owns the mutable tray widgets. Every method runs on the single
// goroutine started in onReady, so the fields need no synchronization.
type menuView struct {
	presenter *Presenter
	providers []ProviderWiring
	// save persists a settings change; the tray never learns where the config
	// file lives.
	save func(config.Config) error

	// The first group states what the app is and what it is currently reading:
	// the app and its release, the active provider, what that provider's CLI is
	// configured to use, then one row per quota window.
	header   *systray.MenuItem
	provider *systray.MenuItem
	prefs    *systray.MenuItem
	meters   []*systray.MenuItem

	// The second group is the freshness of those figures and the one action that
	// changes it. They belong together: the status line is the reason anyone
	// reaches for Refresh.
	status  *systray.MenuItem
	refresh *systray.MenuItem

	// The third group is navigation, and everything in it opens somewhere else or
	// ends the session.
	providersMenu *systray.MenuItem
	providerRows  []providerEntry

	settings *systray.MenuItem
	// The usage-alert thresholds are one group of their own inside Settings:
	// they are read against each other, and grouping them keeps the settings
	// list one entry per topic as more consumption settings arrive.
	usageAlertsMenu *systray.MenuItem
	warnMenu        *systray.MenuItem
	warnItems       []*systray.MenuItem
	criticalMenu    *systray.MenuItem
	criticalItems   []*systray.MenuItem
	intervalMenu    *systray.MenuItem
	intervalItems   []*systray.MenuItem
	languageMenu    *systray.MenuItem
	languageItems   []*systray.MenuItem

	quit *systray.MenuItem

	// lastIcon suppresses redundant SetIcon calls: the gauge quantizes to whole
	// rows, so most polls render identical bytes.
	lastIcon []byte
}

// newMenuView allocates every widget the menu will ever show. Nothing is created
// afterwards: systray can add an item but never remove one, so a row with nothing
// to say is hidden instead.
//
// The provider list is the one part sized from configuration rather than from a
// constant, because the set of providers is fixed for the life of the process
// while the meters each of them reports is not.
func newMenuView(wiring Wiring) *menuView {
	view := &menuView{
		presenter: NewPresenter(wiring.Config),
		providers: wiring.Providers,
		save:      wiring.SaveSettings,
	}

	// Four separators, four groups: what the app is, what it is reading, how
	// fresh that reading is, and where to go next. None of them can be hidden,
	// which is why the heading names the app — always present — and why the group
	// under it always opens with a provider named from its own key.
	view.header = addReadout()
	systray.AddSeparator()

	view.provider = addReadout()
	view.prefs = addReadout()
	view.meters = make([]*systray.MenuItem, maxMeterRows)
	for i := range view.meters {
		view.meters[i] = addReadout()
	}
	systray.AddSeparator()

	view.status = systray.AddMenuItem("", "")
	view.status.Disable()
	view.refresh = systray.AddMenuItem("", "")
	systray.AddSeparator()

	view.providersMenu = systray.AddMenuItem("", "")
	view.providerRows = make([]providerEntry, len(wiring.Providers))
	for i := range view.providerRows {
		view.providerRows[i] = newProviderEntry(view.providersMenu)
	}

	view.settings = systray.AddMenuItem("", "")
	// What the figures mean comes before how often they are fetched, and both
	// before the language the whole menu is written in: the list descends from
	// the reading, through the reading's freshness, to the app itself.
	view.usageAlertsMenu = view.settings.AddSubMenuItem("", "")
	view.warnMenu = view.usageAlertsMenu.AddSubMenuItem("", "")
	view.warnItems = addPercentChoices(view.warnMenu, WarnPresets(), view.currentAlerts().WarnAtPercent)
	view.criticalMenu = view.usageAlertsMenu.AddSubMenuItem("", "")
	view.criticalItems = addPercentChoices(view.criticalMenu, CriticalPresets(), view.currentAlerts().CriticalAtPercent)

	view.intervalMenu = view.settings.AddSubMenuItem("", "")
	presets := IntervalPresets()
	view.intervalItems = make([]*systray.MenuItem, len(presets))
	for i, preset := range presets {
		view.intervalItems[i] = view.intervalMenu.AddSubMenuItemCheckbox(
			view.presenter.IntervalLabel(preset), "", preset == view.currentInterval())
	}
	view.languageMenu = view.settings.AddSubMenuItem("", "")
	languages := i18n.Available()
	view.languageItems = make([]*systray.MenuItem, len(languages))
	for i, lang := range languages {
		view.languageItems[i] = view.languageMenu.AddSubMenuItemCheckbox(
			lang.NativeName(), "", lang == view.presenter.catalog.Lang())
	}

	view.quit = systray.AddMenuItem("", "")

	// Every fixed caption is written in one place, by the same call that rewrites
	// them all when the language changes.
	view.retitle()
	return view
}

// newProviderEntry allocates one provider's list row and the account submenu
// under it. The separator below the context row is added here and never removed,
// which is safe because a provider entry always has that row above it.
func newProviderEntry(parent *systray.MenuItem) providerEntry {
	entry := providerEntry{row: parent.AddSubMenuItem("", "")}
	entry.context = entry.row.AddSubMenuItem("", "")
	entry.context.Disable()
	entry.row.AddSeparator()

	entry.accountRows = make([]*systray.MenuItem, maxAccountRows)
	for i := range entry.accountRows {
		item := entry.row.AddSubMenuItem("", "")
		item.Disable()
		item.Hide()
		entry.accountRows[i] = item
	}
	return entry
}

// addReadout creates a row that displays a value rather than accepting a click.
//
// The tooltip argument is always empty: Windows popup menus have no per-item
// hover text, and systray's own Windows backend drops it. Hover help would have
// to be owner-drawn, which systray does not do, so every row has to say what it
// means in its caption.
func addReadout() *systray.MenuItem {
	item := systray.AddMenuItem("", "")
	item.Disable()
	item.Hide()
	return item
}

// addPercentChoices allocates one tickable item per preset under parent.
//
// Unlike the cadences, these captions are never rewritten: a percentage carries
// no translatable text, so the caption a preset gets here is the one it keeps
// for the life of the process whatever language the rest of the menu is in.
func addPercentChoices(parent *systray.MenuItem, presets []float64, current float64) []*systray.MenuItem {
	items := make([]*systray.MenuItem, len(presets))
	for i, preset := range presets {
		items[i] = parent.AddSubMenuItemCheckbox(percentText(preset), "", preset == current)
	}
	return items
}

func (v *menuView) currentInterval() time.Duration {
	return time.Duration(v.presenter.Config().PollInterval)
}

func (v *menuView) currentAlerts() quota.Thresholds {
	return v.presenter.Config().UsageAlerts
}

// subscriptions reads the current state of every configured provider, in
// configuration order. Both CLI documents are read per provider and per update,
// and a failure in either is expected on a machine where that vendor's CLI has
// never signed in: those rows stay hidden and the quota figures are unaffected.
// They are read independently so one failing does not hide the other.
func (v *menuView) subscriptions() Subscriptions {
	subs := make(Subscriptions, 0, len(v.providers))
	for _, provider := range v.providers {
		account, _ := provider.CLI.Account()
		prefs, _ := provider.CLI.Preferences()
		subs = append(subs, Subscription{
			Vendor:      provider.Controller.Vendor(),
			State:       provider.Controller.State(),
			Account:     account,
			Preferences: prefs,
		})
	}
	return subs
}

// refreshAll asks every provider for an immediate poll, and reports whether any
// of them accepted. One provider still inside its manual-refresh floor does not
// make the click a no-op for the others, so the rejection message is shown only
// when nothing at all was requested.
func (v *menuView) refreshAll() bool {
	accepted := false
	for _, provider := range v.providers {
		if provider.Controller.Refresh() {
			accepted = true
		}
	}
	return accepted
}

// changeSettings persists a settings change and applies it to the running app. A
// rejected or unsaveable change leaves every poller and the file untouched and
// says so in the status line, so the menu never displays a state that is not on
// disk.
func (v *menuView) changeSettings(changed config.Config, err error) {
	if err == nil {
		err = v.save(changed)
	}
	if err != nil {
		v.status.SetTitle(v.presenter.catalog.Text(i18n.SettingsSaveFailed))
		v.syncSettingChecks()
		return
	}

	v.presenter = NewPresenter(changed)
	for _, provider := range v.providers {
		provider.Controller.SetInterval(time.Duration(changed.PollInterval))
	}
	v.retitle()
	v.syncSettingChecks()
	v.apply(time.Now())
}

// retitle rewrites every caption that is not rewritten by the render path: the
// fixed commands, and the settings rows that carry the value in force beside
// their name. It runs at startup and after a settings change, because language
// is the one setting that alters text apply never touches, and because a changed
// value has to reach the row that displays it — including the companion
// threshold, which moves without having been clicked.
func (v *menuView) retitle() {
	catalog := v.presenter.catalog
	for _, item := range []struct {
		widget *systray.MenuItem
		title  i18n.Key
	}{
		{v.providersMenu, i18n.MenuProviders},
		{v.settings, i18n.MenuSettings},
		{v.refresh, i18n.MenuRefresh},
		{v.quit, i18n.MenuQuit},
	} {
		item.widget.SetTitle(catalog.Text(item.title))
	}
	for _, row := range []struct {
		widget *systray.MenuItem
		row    Row
	}{
		{v.usageAlertsMenu, v.presenter.UsageAlertsRow()},
		{v.warnMenu, v.presenter.WarnThresholdRow()},
		{v.criticalMenu, v.presenter.CriticalThresholdRow()},
		{v.intervalMenu, v.presenter.IntervalRow()},
		{v.languageMenu, v.presenter.LanguageRow()},
	} {
		row.widget.SetTitle(MenuRowTitle(row.row))
	}
	for i, preset := range IntervalPresets() {
		v.intervalItems[i].SetTitle(v.presenter.IntervalLabel(preset))
	}
}

// syncSettingChecks makes the ticks agree with the settings actually in force,
// including after a change that failed: systray ticks the item the user clicked
// on its own, so a rejected choice would otherwise stay marked as active.
func (v *menuView) syncSettingChecks() {
	current := v.currentInterval()
	for i, preset := range IntervalPresets() {
		setChecked(v.intervalItems[i], preset == current)
	}
	active := v.presenter.catalog.Lang()
	for i, lang := range i18n.Available() {
		setChecked(v.languageItems[i], lang == active)
	}
	// Both threshold lists are resynced after either change, because setting one
	// past the other moves the one the user did not click.
	alerts := v.currentAlerts()
	for i, preset := range WarnPresets() {
		setChecked(v.warnItems[i], preset == alerts.WarnAtPercent)
	}
	for i, preset := range CriticalPresets() {
		setChecked(v.criticalItems[i], preset == alerts.CriticalAtPercent)
	}
}

func setChecked(item *systray.MenuItem, checked bool) {
	if checked {
		item.Check()
		return
	}
	item.Uncheck()
}

func (v *menuView) apply(now time.Time) {
	subs := v.subscriptions()
	active := subs.Active()

	setReadout(v.header, v.presenter.HeaderRow())
	setReadout(v.provider, v.presenter.ProviderRow(active))
	setReadout(v.prefs, v.presenter.PreferencesRow(active))
	applyRows(v.meters, v.presenter.MeterRows(active, now))
	v.status.SetTitle(v.presenter.StatusText(active, now))
	v.applyProviders(subs)

	percent, level, stale := v.presenter.IconState(active)
	if icon := trayicon.Render(percent, level, stale); !bytes.Equal(icon, v.lastIcon) {
		systray.SetIcon(icon)
		v.lastIcon = icon
	}
	systray.SetTooltip(v.presenter.Tooltip(active, now))
}

// applyProviders fills the provider list. The slice was sized from the same
// configuration this reads, so the two cannot disagree; an entry whose account
// documents could not be read still lists the provider, because the entry names
// what is being monitored and only the fields under it come from those documents.
//
// An entry that cannot name itself at all is hidden rather than listed blank,
// and a list left with nothing in it hides the parent that opens it.
func (v *menuView) applyProviders(subs Subscriptions) {
	listed := 0
	for i, entry := range v.providerRows {
		sub := subs[i]
		name := MenuRowTitle(v.presenter.ProviderListRow(sub))
		if name == "" {
			entry.row.Hide()
			continue
		}
		entry.row.SetTitle(name)
		// The submenu heads itself with the vendor *and* what it sells, which is
		// the qualifier the list entry deliberately leaves out.
		entry.context.SetTitle(MenuRowTitle(v.presenter.ProviderRow(sub)))
		applyRows(entry.accountRows, v.presenter.AccountRows(sub))
		entry.row.Show()
		listed++
	}
	if listed == 0 {
		v.providersMenu.Hide()
		return
	}
	v.providersMenu.Show()
}

// applyRows fills a fixed block of pre-allocated items from a variable number of
// rows. A row with nothing to say is hidden rather than left blank, and a provider
// reporting more meters than the block holds shows the first of them, which is why
// provider order is significant.
func applyRows(items []*systray.MenuItem, rows []Row) {
	for i, item := range items {
		if i >= len(rows) {
			item.Hide()
			continue
		}
		setReadout(item, rows[i])
	}
}

// setReadout shows a row, or hides the item when the row has no content at all.
func setReadout(item *systray.MenuItem, row Row) {
	title := MenuRowTitle(row)
	if title == "" {
		item.Hide()
		return
	}
	item.SetTitle(title)
	item.Show()
}
