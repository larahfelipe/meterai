// Command meterAI is a Windows notification-area monitor for AI subscription
// usage quotas.
//
// It is strictly a reader of credentials the official vendor CLIs already
// wrote: it never performs an OAuth flow, never refreshes a token, and never
// writes a credential file back. See package credential for why.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/identity"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/provider/anthropic"
	"github.com/larahfelipe/meterai/internal/provider/openai"
	"github.com/larahfelipe/meterai/internal/singleton"
	"github.com/larahfelipe/meterai/internal/tray"
)

// exitAlreadyRunning is distinct from a generic failure so a launcher or
// shortcut can tell "already up" from "broken".
const exitAlreadyRunning = 3

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "meterAI: %v\n", err)
	}
	os.Exit(code)
}

func run() (int, error) {
	// The guard comes first: two instances would double the request rate
	// against undocumented, rate-limited endpoints.
	release, err := singleton.Acquire()
	if errors.Is(err, singleton.ErrAlreadyRunning) {
		// Not worth reporting: the running instance already owns a tray icon.
		return exitAlreadyRunning, nil
	}
	if err != nil {
		return 1, fmt.Errorf("acquire single-instance guard: %w", err)
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			fmt.Fprintf(os.Stderr, "meterAI: release single-instance guard: %v\n", releaseErr)
		}
	}()

	configPath, err := config.DefaultPath()
	if err != nil {
		return 1, err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return 1, err
	}

	// Cancellation stops the poller. The tray's own Quit item instead unwinds
	// tray.Run, which returns here and triggers the deferred stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// updates is a signal, not a queue: the UI always reads the newest state
	// from its sources, so a dropped signal can never show a stale reading.
	updates := make(chan struct{}, 1)
	redraw := func() {
		select {
		case updates <- struct{}{}:
		default:
		}
	}

	// Every poller signals the UI the same way; the state itself is re-read from
	// the Controller, so the callback carries nothing.
	onPoll := func(poll.State) { redraw() }
	interval := time.Duration(cfg.PollInterval)

	// Each vendor package pairs its own credential file with its own decoder, so
	// the only thing that reaches this file is the pinned path the user
	// configured under that vendor's key — which is the key the provider already
	// namespaces its meters with (§3.6).
	claudeCredentials := anthropic.NewCredentialCache(cfg.Providers[anthropic.VendorKey].CredentialPath)
	claudePoller := poll.New(anthropic.New(claudeCredentials, nil), interval, onPoll)
	// Account details follow whichever credential the cache resolved, so the name
	// on screen always belongs to the subscription being polled. The cache reads
	// off the UI's goroutine and announces itself here, on the same signal the
	// poller uses: a document that lands between two polls is drawn when it lands
	// rather than at the next one.
	claudeAccounts := identity.NewCache(claudeCredentials, redraw)
	go claudePoller.Run(ctx)

	codexCredentials := openai.NewCredentialCache(cfg.Providers[openai.VendorKey].CredentialPath)
	codexPoller := poll.New(openai.New(codexCredentials, nil), interval, onPoll)
	go codexPoller.Run(ctx)

	// tray.Run blocks and, on Windows, must own the main goroutine.
	//
	// Providers is a list because the menu is built from one: a second vendor is
	// another poller and another CLI reader appended here, and nothing in the UI
	// changes shape to accommodate it.
	wiring := tray.Wiring{
		Config:  cfg,
		Updates: updates,
		Providers: []tray.ProviderWiring{
			{Controller: claudePoller, CLI: claudeAccounts},
			{Controller: codexPoller, CLI: openai.NoIdentity{}},
		},
		// The tray hands back a whole validated document; only the path stays here.
		SaveSettings: func(changed config.Config) error { return config.Save(configPath, changed) },
	}
	if err := tray.Run(ctx, wiring); err != nil && !errors.Is(err, context.Canceled) {
		return 1, err
	}
	return 0, nil
}
