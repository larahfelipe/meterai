//go:build !windows

package tray

import (
	"context"
	"errors"
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
		subs := make(Subscriptions, 0, len(wiring.Providers))
		for _, provider := range wiring.Providers {
			// Unlike the tray, the development host reports why a document could
			// not be read: it is the only place those failures are diagnosable.
			account, err := provider.CLI.Account()
			if isDiagnosable(err) {
				fmt.Fprintf(w, "meterAI: account details unavailable: %v\n", err)
			}
			prefs, err := provider.CLI.Preferences()
			if isDiagnosable(err) {
				fmt.Fprintf(w, "meterAI: CLI preferences unavailable: %v\n", err)
			}
			subs = append(subs, Subscription{
				Vendor:      provider.Controller.Vendor(),
				State:       provider.Controller.State(),
				Account:     account,
				Preferences: prefs,
			})
		}
		render(w, presenter, subs, time.Now())
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

// isDiagnosable reports whether a CLIReader failure is worth a line of output.
//
// "This document declares nothing" is not a failure to diagnose: it is the
// steady state for a CLI installed but never signed in, and the permanent one
// for a vendor that keeps no such document at all. Since renderNow runs on every
// update, reporting those would emit the same two lines every poll for the life
// of the process — which buries the failures that are real.
func isDiagnosable(err error) bool {
	return err != nil &&
		!errors.Is(err, identity.ErrNoAccount) &&
		!errors.Is(err, identity.ErrNoPreferences)
}

// render takes its destination as a parameter so the headless output can be
// asserted without capturing the process's stderr.
//
// The column widths are this file's own: a terminal is monospaced, so it lines
// rows up with padding where the menu has to use a tab. What it reproduces is the
// order and the grouping of the menu, which is what the pipeline is being
// exercised for — including the provider list, so a second vendor shows up here
// before it can be seen on Windows.
func render(w io.Writer, presenter *Presenter, subs Subscriptions, now time.Time) {
	// The instant heads the block because this output is a stream: unlike the
	// menu, which is read once and replaced, every render here stays on screen
	// above the next one.
	fmt.Fprintf(w, "\n[%s]\n", now.Format(time.RFC3339))
	writeRow(w, presenter.HeaderRow())

	active := subs.Active()
	writeRow(w, presenter.ProviderRow(active))
	writeRow(w, presenter.PreferencesRow(active))
	for _, row := range presenter.MeterRows(active, now) {
		writeRow(w, row)
	}
	fmt.Fprintf(w, "  %s\n", presenter.StatusText(active, now))

	// The provider list, and behind each entry the submenu it opens: the vendor
	// with what it sells, then that account's fields. A terminal has no submenus,
	// so nesting is the one thing this has to express with indentation.
	for _, sub := range subs {
		writeRow(w, presenter.ProviderListRow(sub))
		writeRow(w, indent(presenter.ProviderRow(sub)))
		for _, row := range presenter.AccountRows(sub) {
			writeRow(w, indent(row))
		}
	}
	writeRow(w, presenter.UsageAlertsRow())
	writeRow(w, indent(presenter.WarnThresholdRow()))
	writeRow(w, indent(presenter.CriticalThresholdRow()))
	writeRow(w, presenter.IntervalRow())
	writeRow(w, presenter.LanguageRow())
}

// indent marks a row as belonging to the one above it. The menu expresses that
// with a submenu, which a stream of lines cannot; the label column absorbs the
// prefix, so the values stay in the same column as every other row.
func indent(row Row) Row {
	const nestedIndent = "    "
	if row == (Row{}) {
		return row
	}
	row.Label = nestedIndent + row.Label
	return row
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
