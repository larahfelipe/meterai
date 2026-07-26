//go:build !windows

package tray

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
)

// Run prints each state change to stderr instead of drawing a tray icon.
//
// The shipping target is Windows; this exists so the full pipeline —
// credential discovery, polling, backoff, formatting — can be exercised on the
// Linux development host, which is also where the credentials actually live.
func Run(ctx context.Context, cfg config.Config, updates <-chan struct{}, controller Controller) error {
	presenter := NewPresenter(cfg)
	render(os.Stderr, presenter, controller.State(), time.Now())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-updates:
			render(os.Stderr, presenter, controller.State(), time.Now())
		}
	}
}

// render takes its destination as a parameter so the headless output can be
// asserted without capturing the process's stderr.
func render(w io.Writer, presenter *Presenter, state PollState, now time.Time) {
	fmt.Fprintf(w, "\n[%s] meterAI\n", now.Format(time.RFC3339))
	for _, row := range presenter.Rows(state, now) {
		fmt.Fprintf(w, "  %-16s %s\n", row.Label, row.Detail)
	}
	fmt.Fprintf(w, "  %s\n", presenter.StatusText(state, now))
}
