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
		// Unlike the tray, the development host reports why a document could not
		// be read: it is the only place those failures are diagnosable.
		account, err := wiring.CLI.Account()
		if err != nil {
			fmt.Fprintf(w, "meterAI: account details unavailable: %v\n", err)
		}
		prefs, err := wiring.CLI.Preferences()
		if err != nil {
			fmt.Fprintf(w, "meterAI: CLI preferences unavailable: %v\n", err)
		}
		render(w, presenter, wiring.Controller.State(), account, prefs, time.Now())
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
//
// The column widths are this file's own: a terminal is monospaced, so it lines
// rows up with padding where the menu has to use a tab. What it reproduces is the
// order and the grouping, which is what the pipeline is being exercised for.
func render(w io.Writer, presenter *Presenter, state PollState, account *identity.Account,
	prefs *identity.Preferences, now time.Time) {
	fmt.Fprintf(w, "\n[%s] %s\n", now.Format(time.RFC3339), AppName)
	writeRow(w, presenter.HeaderRow(state))
	writeRow(w, presenter.AccountRow(account))
	for _, row := range presenter.Rows(state, now) {
		writeRow(w, row)
	}
	fmt.Fprintf(w, "  %s\n", presenter.StatusText(state, now))
	for _, row := range presenter.DetailRows(account, prefs) {
		writeRow(w, row)
	}
	writeRow(w, presenter.IntervalRow())
	writeRow(w, presenter.LanguageRow())
}

// writeRow prints one row, skipping an empty one exactly as the menu hides it.
func writeRow(w io.Writer, row Row) {
	// labelWidth pads the left column to where the widest label of a live poll
	// ends. A terminal is monospaced, so padding lines the values up here where the
	// menu needs a tab.
	const labelWidth = 32
	switch {
	case row == (Row{}):
	case row.Detail == "" && row.Bar == "":
		fmt.Fprintf(w, "  %s\n", row.Label)
	case row.Bar == "":
		fmt.Fprintf(w, "  %-*s %s\n", labelWidth, row.Label, row.Detail)
	default:
		fmt.Fprintf(w, "  %-*s %4s  %s\n", labelWidth, row.Label, row.Detail, row.Bar)
	}
}
