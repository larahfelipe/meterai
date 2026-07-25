//go:build !windows

package tray

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
)

// Run prints each state change to stderr instead of drawing a tray icon.
//
// The shipping target is Windows; this exists so the full pipeline —
// credential discovery, polling, backoff, formatting — can be exercised on the
// Linux development host, which is also where the credentials actually live.
func Run(ctx context.Context, _ config.Config, updates <-chan struct{}, controller Controller) error {
	render(controller.State(), time.Now())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-updates:
			render(controller.State(), time.Now())
		}
	}
}

func render(state PollState, now time.Time) {
	fmt.Fprintf(os.Stderr, "\n[%s] meterAI\n", now.Format(time.RFC3339))
	for _, row := range Rows(state, now) {
		fmt.Fprintf(os.Stderr, "  %-16s %s\n", row.Label, row.Detail)
	}
	fmt.Fprintf(os.Stderr, "  %s\n", StatusText(state, now))
}
