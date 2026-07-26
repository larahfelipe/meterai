//go:build !windows

package tray

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/larahfelipe/meterai/internal/identity"
)

// Run prints each state change to stderr instead of drawing a tray icon.
//
// The shipping target is Windows; this exists so the full pipeline —
// credential discovery, polling, backoff, formatting — can be exercised on the
// Linux development host, which is also where the credentials actually live.
func Run(ctx context.Context, wiring Wiring) error {
	return run(ctx, os.Stderr, wiring)
}

// run takes its destination as a parameter so the loop can be exercised without
// capturing the process's stderr. There is no interactive menu here, so the
// settings entry points of Wiring go unused: this path exists to exercise the
// pipeline, not to configure it.
func run(ctx context.Context, w io.Writer, wiring Wiring) error {
	presenter := NewPresenter(wiring.Config)
	renderNow := func() {
		// Unlike the tray, the development host reports why an account could not
		// be read: it is the only place that failure is diagnosable.
		account, err := wiring.Accounts.Account()
		if err != nil {
			fmt.Fprintf(w, "meterAI: account details unavailable: %v\n", err)
		}
		render(w, presenter, wiring.Controller.State(), account, time.Now())
	}
	renderNow()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wiring.Updates:
			renderNow()
		}
	}
}

// render takes its destination as a parameter so the headless output can be
// asserted without capturing the process's stderr.
func render(w io.Writer, presenter *Presenter, state PollState, account *identity.Account, now time.Time) {
	fmt.Fprintf(w, "\n[%s] meterAI\n", now.Format(time.RFC3339))
	if header := presenter.HeaderText(state, account); header != "" {
		fmt.Fprintf(w, "  %s\n", header)
	}
	for _, row := range presenter.DetailRows(account) {
		fmt.Fprintf(w, "  %-16s %s\n", row.Label, row.Detail)
	}
	for _, row := range presenter.Rows(state, now) {
		fmt.Fprintf(w, "  %-16s %-10s %s\n", row.Label, row.Bar, row.Detail)
	}
	fmt.Fprintf(w, "  %s\n", presenter.StatusText(state, now))
}
