//go:build windows

package tray

import (
	"bytes"
	"context"
	"time"

	"fyne.io/systray"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/trayicon"
)

const (
	// appTitle is the accessible name Windows announces for the icon.
	appTitle = "meterAI"

	// maxMeterRows bounds the pre-allocated menu entries. systray can add items
	// but never remove them, so the rows are created once at startup and hidden
	// while unused; a provider reporting more meters than this shows the first
	// maxMeterRows, which is why provider order is significant.
	maxMeterRows = 6

	// refreshRejectedNotice replaces the status line when the user asks for a
	// refresh inside the floor, so the click is visibly acknowledged rather
	// than appearing to do nothing.
	refreshRejectedNotice = "Consulta recente demais — aguarde um instante"
)

// Run displays the tray icon and blocks until the user quits or ctx is
// cancelled. It must be called from the main goroutine: systray locks the OS
// thread that owns the Windows message loop.
func Run(ctx context.Context, cfg config.Config, updates <-chan struct{}, controller Controller) error {
	systray.Run(func() { onReady(ctx, cfg, updates, controller) }, func() {})
	return nil
}

func onReady(ctx context.Context, cfg config.Config, updates <-chan struct{}, controller Controller) {
	systray.SetTitle(appTitle)

	view := &menuView{cfg: cfg}
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

	refreshItem := systray.AddMenuItem("Atualizar agora", "Força uma consulta imediata")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Sair", "Encerra o monitor")

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
					view.status.SetTitle(refreshRejectedNotice)
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
	cfg    config.Config
	rows   []*systray.MenuItem
	status *systray.MenuItem
	// lastIcon suppresses redundant SetIcon calls: the gauge quantizes to whole
	// rows, so most polls render identical bytes.
	lastIcon []byte
}

func (v *menuView) apply(state PollState, now time.Time) {
	percent, level, stale := IconState(state, v.cfg)
	if icon := trayicon.Render(percent, level, stale); !bytes.Equal(icon, v.lastIcon) {
		systray.SetIcon(icon)
		v.lastIcon = icon
	}
	systray.SetTooltip(Tooltip(state, now))

	rows := Rows(state, now)
	for i, item := range v.rows {
		if i >= len(rows) {
			item.Hide()
			continue
		}
		item.SetTitle(rows[i].Label + "   " + rows[i].Detail)
		item.Show()
	}
	v.status.SetTitle(StatusText(state, now))
}
