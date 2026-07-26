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
	// appTitle is the accessible name Windows announces for the icon. It is the
	// product name and therefore not translated.
	appTitle = "meterAI"

	// maxDetailRows bounds the pre-allocated rows of the details submenu. It
	// matches the number of account fields DetailRows can produce; like the meter
	// rows, they exist from startup because systray cannot remove an item.
	maxDetailRows = 2

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
	systray.SetTitle(appTitle)
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

	header *systray.MenuItem
	rows   []*systray.MenuItem
	status *systray.MenuItem
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
	catalog := view.presenter.catalog

	view.header = addReadout()
	systray.AddSeparator()

	view.rows = make([]*systray.MenuItem, maxMeterRows)
	for i := range view.rows {
		view.rows[i] = addReadout()
	}
	systray.AddSeparator()
	view.status = systray.AddMenuItem("", "")
	view.status.Disable()

	view.details = systray.AddMenuItem(catalog.Text(i18n.MenuDetails), catalog.Text(i18n.MenuDetailsTooltip))
	view.details.Hide()
	view.detailRows = make([]*systray.MenuItem, maxDetailRows)
	for i := range view.detailRows {
		item := view.details.AddSubMenuItem("", "")
		item.Disable()
		item.Hide()
		view.detailRows[i] = item
	}

	view.settings = systray.AddMenuItem(catalog.Text(i18n.MenuSettings), catalog.Text(i18n.MenuSettingsTooltip))
	view.intervalMenu = view.settings.AddSubMenuItem(catalog.Text(i18n.MenuInterval), catalog.Text(i18n.MenuIntervalTooltip))
	presets := IntervalPresets()
	view.intervalItems = make([]*systray.MenuItem, len(presets))
	for i, preset := range presets {
		view.intervalItems[i] = view.intervalMenu.AddSubMenuItemCheckbox(
			view.presenter.IntervalLabel(preset), "", preset == view.currentInterval())
	}
	view.languageMenu = view.settings.AddSubMenuItem(catalog.Text(i18n.MenuLanguage), catalog.Text(i18n.MenuLanguageTooltip))
	languages := i18n.Available()
	view.languageItems = make([]*systray.MenuItem, len(languages))
	for i, lang := range languages {
		view.languageItems[i] = view.languageMenu.AddSubMenuItemCheckbox(
			lang.NativeName(), "", lang == catalog.Lang())
	}

	view.refresh = systray.AddMenuItem(catalog.Text(i18n.MenuRefresh), catalog.Text(i18n.MenuRefreshTooltip))
	systray.AddSeparator()
	view.quit = systray.AddMenuItem(catalog.Text(i18n.MenuQuit), catalog.Text(i18n.MenuQuitTooltip))
	return view
}

// addReadout creates a row that displays a value rather than accepting a click.
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

// retitle rewrites every caption fixed at startup. It runs after a settings
// change because language is the one setting that alters text the render path
// does not rewrite on its own.
func (v *menuView) retitle() {
	catalog := v.presenter.catalog
	for _, item := range []struct {
		widget  *systray.MenuItem
		title   i18n.Key
		tooltip i18n.Key
	}{
		{v.details, i18n.MenuDetails, i18n.MenuDetailsTooltip},
		{v.settings, i18n.MenuSettings, i18n.MenuSettingsTooltip},
		{v.intervalMenu, i18n.MenuInterval, i18n.MenuIntervalTooltip},
		{v.languageMenu, i18n.MenuLanguage, i18n.MenuLanguageTooltip},
		{v.refresh, i18n.MenuRefresh, i18n.MenuRefreshTooltip},
		{v.quit, i18n.MenuQuit, i18n.MenuQuitTooltip},
	} {
		item.widget.SetTitle(catalog.Text(item.title))
		item.widget.SetTooltip(catalog.Text(item.tooltip))
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

	rows := v.presenter.Rows(state, now)
	for i, item := range v.rows {
		if i >= len(rows) {
			item.Hide()
			continue
		}
		item.SetTitle(MenuRowTitle(rows[i]))
		item.Show()
	}
	v.status.SetTitle(v.presenter.StatusText(state, now))
}

func (v *menuView) applyAccount(state PollState, account *identity.Account) {
	if header := v.presenter.HeaderText(state, account); header != "" {
		v.header.SetTitle(header)
		v.header.Show()
	} else {
		v.header.Hide()
	}

	rows := v.presenter.DetailRows(account)
	for i, item := range v.detailRows {
		if i >= len(rows) {
			item.Hide()
			continue
		}
		item.SetTitle(MenuRowTitle(rows[i]))
		item.Show()
	}
	if len(rows) == 0 {
		v.details.Hide()
		return
	}
	v.details.Show()
}
