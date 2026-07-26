//go:build windows

package tray

import (
	"bytes"
	"context"
	"time"

	"fyne.io/systray"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/identity"
	"github.com/larahfelipe/meterai/internal/trayicon"
)

const (
	// maxDetailRows bounds the pre-allocated rows of the details submenu. It is
	// the ceiling DetailRows is tested against; like the meter rows, they exist
	// from startup because systray cannot remove an item.
	maxDetailRows = maxAccountRows

	// maxMeterRows bounds the pre-allocated menu entries. systray can add items
	// but never remove them, so the rows are created once at startup and hidden
	// while unused; a provider reporting more meters than this shows the first
	// maxMeterRows, which is why provider order is significant.
	maxMeterRows = 6
)

// Run displays the tray icon and blocks until the user quits or ctx is
// cancelled. It must be called from the main goroutine: systray locks the OS
// thread that owns the Windows message loop.
func Run(ctx context.Context, wiring Wiring) error {
	systray.Run(func() { onReady(ctx, wiring) }, func() {
		// Nothing to undo here: systray removes the icon itself, and the poller
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
	for i, preset := range IntervalPresets() {
		forwardChoice(ctx, view.intervalItems[i].ClickedCh, intervalChosen, preset)
	}
	for i, lang := range i18n.Available() {
		forwardChoice(ctx, view.languageItems[i].ClickedCh, languageChosen, lang)
	}

	view.apply(wiring.Controller.State(), time.Now())

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
				if !wiring.Controller.Refresh() {
					// Replacing the status line acknowledges the click, which
					// would otherwise look like nothing happened.
					view.status.SetTitle(view.presenter.catalog.Text(i18n.RefreshRejected))
				}
			case interval := <-intervalChosen:
				view.changeSettings(WithInterval(view.presenter.Config(), interval))
			case lang := <-languageChosen:
				view.changeSettings(WithLanguage(view.presenter.Config(), lang))
			case <-wiring.Updates:
				view.apply(wiring.Controller.State(), time.Now())
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

// menuView owns the mutable tray widgets. Every method runs on the single
// goroutine started in onReady, so the fields need no synchronization.
type menuView struct {
	presenter *Presenter
	accounts  AccountReader
	// controller and save apply a settings change to the running poller and to
	// disk; the tray never learns where the config file lives.
	controller Controller
	save       func(config.Config) error

	// header and account are the two rows of the heading: the provider with its
	// plan, and whose subscription it is under it.
	header  *systray.MenuItem
	account *systray.MenuItem
	rows    []*systray.MenuItem
	status  *systray.MenuItem
	// details is the submenu parent; it stays hidden until there is at least one
	// row under it, so the user never opens an empty submenu.
	details    *systray.MenuItem
	detailRows []*systray.MenuItem

	settings      *systray.MenuItem
	intervalMenu  *systray.MenuItem
	intervalItems []*systray.MenuItem
	languageMenu  *systray.MenuItem
	languageItems []*systray.MenuItem

	refresh *systray.MenuItem
	quit    *systray.MenuItem

	// lastIcon suppresses redundant SetIcon calls: the gauge quantizes to whole
	// rows, so most polls render identical bytes.
	lastIcon []byte
}

// newMenuView allocates every widget the menu will ever show. Nothing is created
// afterwards: systray can add an item but never remove one, so a row with nothing
// to say is hidden instead.
func newMenuView(wiring Wiring) *menuView {
	view := &menuView{
		presenter:  NewPresenter(wiring.Config),
		accounts:   wiring.Accounts,
		controller: wiring.Controller,
		save:       wiring.SaveSettings,
	}
	// The menu reads in four groups, one per separator: what is being monitored,
	// what it currently reads, what can be done about it, and — held apart because
	// it ends the process — Quit. A separator marks nothing else, and none of them
	// can be hidden, which is why the heading always has a caption.
	view.header = addReadout()
	view.account = addReadout()
	systray.AddSeparator()

	view.rows = make([]*systray.MenuItem, maxMeterRows)
	for i := range view.rows {
		view.rows[i] = addReadout()
	}
	systray.AddSeparator()

	// The freshness of the data and the action that changes it belong together:
	// the status line is the reason anyone reaches for Refresh.
	view.status = systray.AddMenuItem("", "")
	view.status.Disable()
	view.refresh = systray.AddMenuItem("", "")

	view.details = systray.AddMenuItem("", "")
	view.details.Hide()
	view.detailRows = make([]*systray.MenuItem, maxDetailRows)
	for i := range view.detailRows {
		item := view.details.AddSubMenuItem("", "")
		item.Disable()
		item.Hide()
		view.detailRows[i] = item
	}

	view.settings = systray.AddMenuItem("", "")
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

	systray.AddSeparator()
	view.quit = systray.AddMenuItem("", "")

	// Every fixed caption is written in one place, by the same call that rewrites
	// them all when the language changes.
	view.retitle()
	return view
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

func (v *menuView) currentInterval() time.Duration {
	return time.Duration(v.presenter.Config().PollInterval)
}

// changeSettings persists a settings change and applies it to the running app. A
// rejected or unsaveable change leaves the poller and the file untouched and says
// so in the status line, so the menu never displays a state that is not on disk.
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
	v.controller.SetInterval(time.Duration(changed.PollInterval))
	v.retitle()
	v.syncSettingChecks()
	v.apply(v.controller.State(), time.Now())
}

// retitle rewrites every caption that is not rewritten by the render path: the
// fixed commands, and the two settings rows that carry the value in force beside
// their name. It runs at startup and after a settings change, because language is
// the one setting that alters text apply never touches and a new cadence has to
// reach the row that displays it.
func (v *menuView) retitle() {
	catalog := v.presenter.catalog
	for _, item := range []struct {
		widget *systray.MenuItem
		title  i18n.Key
	}{
		{v.details, i18n.MenuDetails},
		{v.settings, i18n.MenuSettings},
		{v.refresh, i18n.MenuRefresh},
		{v.quit, i18n.MenuQuit},
	} {
		item.widget.SetTitle(catalog.Text(item.title))
	}
	v.intervalMenu.SetTitle(MenuRowTitle(v.presenter.IntervalRow()))
	v.languageMenu.SetTitle(MenuRowTitle(v.presenter.LanguageRow()))
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
}

func setChecked(item *systray.MenuItem, checked bool) {
	if checked {
		item.Check()
		return
	}
	item.Uncheck()
}

func (v *menuView) apply(state PollState, now time.Time) {
	// A failure here is expected on a machine where the CLI has never signed in;
	// the account rows stay hidden and the quota figures are unaffected.
	account, _ := v.accounts.Account()
	v.applyAccount(state, account)

	percent, level, stale := v.presenter.IconState(state)
	if icon := trayicon.Render(percent, level, stale); !bytes.Equal(icon, v.lastIcon) {
		systray.SetIcon(icon)
		v.lastIcon = icon
	}
	systray.SetTooltip(v.presenter.Tooltip(state, now))

	applyRows(v.rows, v.presenter.Rows(state, now))
	v.status.SetTitle(v.presenter.StatusText(state, now))
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

func (v *menuView) applyAccount(state PollState, account *identity.Account) {
	setReadout(v.header, v.presenter.HeaderRow(state))
	setReadout(v.account, v.presenter.AccountRow(account))

	rows := v.presenter.DetailRows(account)
	applyRows(v.detailRows, rows)
	if len(rows) == 0 {
		v.details.Hide()
		return
	}
	v.details.Show()
}
