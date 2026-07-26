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
func Run(ctx context.Context, cfg config.Config, updates <-chan struct{}, controller Controller, accounts AccountReader) error {
	// The exit callback is empty on purpose: systray removes the icon itself, and
	// the poller and the single-instance guard are unwound by the caller once
	// this returns.
	systray.Run(func() { onReady(ctx, cfg, updates, controller, accounts) }, func() {})
	return nil
}

func onReady(ctx context.Context, cfg config.Config, updates <-chan struct{}, controller Controller, accounts AccountReader) {
	systray.SetTitle(appTitle)

	view := &menuView{presenter: NewPresenter(cfg), accounts: accounts}

	view.header = systray.AddMenuItem("", "")
	view.header.Disable()
	view.header.Hide()
	systray.AddSeparator()

	view.rows = make([]*systray.MenuItem, maxMeterRows)
	for i := range view.rows {
		item := systray.AddMenuItem("", "")
		// Meter rows are readouts, not commands.
		item.Disable()
		item.Hide()
		view.rows[i] = item
	}
	systray.AddSeparator()
	view.status = systray.AddMenuItem("", "")
	view.status.Disable()

	catalog := view.presenter.catalog
	view.details = systray.AddMenuItem(catalog.Text(i18n.MenuDetails), catalog.Text(i18n.MenuDetailsTooltip))
	view.details.Hide()
	view.detailRows = make([]*systray.MenuItem, maxDetailRows)
	for i := range view.detailRows {
		item := view.details.AddSubMenuItem("", "")
		item.Disable()
		item.Hide()
		view.detailRows[i] = item
	}

	refreshItem := systray.AddMenuItem(catalog.Text(i18n.MenuRefresh), catalog.Text(i18n.MenuRefreshTooltip))
	systray.AddSeparator()
	quitItem := systray.AddMenuItem(catalog.Text(i18n.MenuQuit), catalog.Text(i18n.MenuQuitTooltip))

	view.apply(controller.State(), time.Now())

	go func() {
		for {
			select {
			case <-ctx.Done():
				systray.Quit()
				return
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			case <-refreshItem.ClickedCh:
				if !controller.Refresh() {
					// Replacing the status line acknowledges the click, which
					// would otherwise look like nothing happened.
					view.status.SetTitle(catalog.Text(i18n.RefreshRejected))
				}
			case <-updates:
				view.apply(controller.State(), time.Now())
			}
		}
	}()
}

// menuView owns the mutable tray widgets. Every method runs on the single
// goroutine started in onReady, so the fields need no synchronization.
type menuView struct {
	presenter *Presenter
	accounts  AccountReader
	header    *systray.MenuItem
	rows      []*systray.MenuItem
	status    *systray.MenuItem
	// details is the submenu parent; it is hidden until there is at least one
	// row to put under it, so the user never opens an empty submenu.
	details    *systray.MenuItem
	detailRows []*systray.MenuItem
	// lastIcon suppresses redundant SetIcon calls: the gauge quantizes to whole
	// rows, so most polls render identical bytes.
	lastIcon []byte
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
