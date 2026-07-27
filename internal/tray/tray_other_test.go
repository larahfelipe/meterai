//go:build !windows

package tray

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/config"
	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/identity"
	"github.com/larahfelipe/meterai/internal/poll"
	"github.com/larahfelipe/meterai/internal/quota"
)

// bare is the subscription of a provider that has polled but whose CLI documents
// were never read, which is the state on a machine where that CLI has never run.
func bare() Subscription {
	return Subscription{Vendor: "anthropic", State: liveState()}
}

func TestRenderWritesEveryMeterAndTheStatusLine(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), Subscriptions{bare()}, now)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	// A blank line, the instant, the app heading, the active provider, one line
	// per meter, the status line, that provider's entry in the list and the row
	// heading the submenu behind it, then the five settings rows. No CLI documents
	// are given here, so neither the preferences row nor any account row appears.
	if len(lines) != 14 {
		t.Fatalf("output has %d lines, want 14:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], now.Format("2006-01-02")) {
		t.Errorf("banner = %q, want the observation instant", lines[1])
	}
	if !strings.Contains(lines[2], AppName) || !strings.Contains(lines[2], versionPrefix) {
		t.Errorf("heading = %q, want the app and its release", lines[2])
	}
	if !strings.Contains(lines[3], "Anthropic") || !strings.Contains(lines[3], "Claude Pro") {
		t.Errorf("provider = %q, want the vendor and the plan", lines[3])
	}
	if !strings.Contains(lines[4], "Session (5h)") || !strings.Contains(lines[4], "23%") {
		t.Errorf("meter line = %q", lines[4])
	}
	if !strings.Contains(lines[6], "Updated") {
		t.Errorf("status line = %q", lines[6])
	}
	// The settings in force are readable without opening anything, which is what
	// the menu itself does with them.
	// The list entry names the vendor alone; the qualifier belongs to the row
	// heading its submenu, one level in.
	if lines[7] != "  Anthropic" {
		t.Errorf("list entry = %q, want the vendor alone", lines[7])
	}
	if !strings.Contains(lines[8], "Claude Pro") {
		t.Errorf("submenu heading = %q, want the plan", lines[8])
	}
	// The usage-alert group states the pair, and each threshold restates its own
	// half one level in, exactly as the menu does.
	if !strings.Contains(lines[9], "75%") || !strings.Contains(lines[9], "90%") {
		t.Errorf("usage alerts line = %q, want both thresholds", lines[9])
	}
	if !strings.Contains(lines[10], "75%") || !strings.Contains(lines[11], "90%") {
		t.Errorf("threshold lines = %q, %q", lines[10], lines[11])
	}
	if !strings.Contains(lines[12], "5 min") || !strings.Contains(lines[13], "English (US)") {
		t.Errorf("settings lines = %q, %q", lines[12], lines[13])
	}
}

func TestRenderWritesTheAccountBehindItsProvider(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), Subscriptions{live()}, now)

	for _, want := range []string{"Claude Pro", "Opus • High Effort", "Sample", "sample@example.com", "Sample Org"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not carry %q:\n%s", want, out.String())
		}
	}
}

// The provider list is what a second vendor appears in, so it is rendered from
// the whole slice rather than from the active subscription alone. The first
// level still describes one provider: only the list grows.
func TestRenderListsEveryConfiguredProviderAndReadsTheFirst(t *testing.T) {
	second := Subscription{
		Vendor:  "openrouter",
		State:   poll.State{Snapshot: &quota.Snapshot{Vendor: "openrouter", Plan: "team"}},
		Account: &identity.Account{DisplayName: "Second"},
	}

	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), Subscriptions{live(), second}, now)

	for _, want := range []string{"Openrouter", "Team", "Second"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not list the second provider's %q:\n%s", want, out.String())
		}
	}
	// The meters at the first level belong to the active provider alone, so the
	// second provider contributes no reading there.
	head, _, _ := strings.Cut(out.String(), "Updated")
	if strings.Contains(head, "Openrouter") {
		t.Errorf("the first level describes a provider other than the active one:\n%s", head)
	}
}

func TestRenderBeforeTheFirstPollWritesNoReading(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), Subscriptions{{Vendor: "anthropic"}}, now)

	// Nothing has been read yet, so every meter row is absent: a blank line, the
	// instant, the app heading, the provider named from its key, the status line,
	// the provider's list entry and submenu heading, and the five settings rows.
	if got := strings.Count(out.String(), "\n"); got != 12 {
		t.Errorf("output has %d lines: %q", got, out.String())
	}
	if !strings.Contains(out.String(), "Polling…") {
		t.Errorf("output = %q, does not disclose that no reading exists yet", out.String())
	}
	// The vendor key is known before the first poll; nothing the poll would have
	// supplied may be invented alongside it. The settings block below is not part
	// of that claim — its figures are the thresholds, which are known from the
	// config file and are the one place a percentage legitimately precedes a poll.
	readings, settings, found := strings.Cut(out.String(), "Usage alerts")
	if !found {
		t.Fatalf("output = %q, does not reach the settings block", out.String())
	}
	for _, unwanted := range []string{"Claude", "%"} {
		if strings.Contains(readings, unwanted) {
			t.Errorf("output = %q, invents %q before the first poll", out.String(), unwanted)
		}
	}
	// And the thresholds are stated in full even so, because they describe how the
	// first reading will be classified rather than a reading of their own.
	for _, want := range []string{"75%", "90%"} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings block %q does not state the threshold %q", settings, want)
		}
	}
}

// stubController and stubCLI stand in for the poller and the identity cache.
type stubController struct {
	vendor string
	state  poll.State
}

func (s stubController) Vendor() string          { return s.vendor }
func (s stubController) State() poll.State       { return s.state }
func (stubController) Refresh() bool             { return true }
func (stubController) SetInterval(time.Duration) {}

type stubCLI struct {
	account    *identity.Account
	accountErr error
	prefs      *identity.Preferences
	prefsErr   error
}

func (s stubCLI) Account() (*identity.Account, error) { return s.account, s.accountErr }

func (s stubCLI) Preferences() (*identity.Preferences, error) { return s.prefs, s.prefsErr }

// oneProvider is the wiring shape main.go builds, with the ports stubbed.
func oneProvider(cli stubCLI) []ProviderWiring {
	return []ProviderWiring{{
		Controller: stubController{vendor: "anthropic", state: liveState()},
		CLI:        cli,
	}}
}

func TestRunRendersOnceAndUnwindsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := run(ctx, &out, Wiring{
		Config:  config.Default(),
		Updates: make(chan struct{}),
		Providers: oneProvider(stubCLI{
			account: &identity.Account{DisplayName: "Sample"},
			prefs:   &identity.Preferences{Model: "opus", EffortLevel: "high"},
		}),
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() = %v, want context.Canceled", err)
	}
	// The first reading is printed before the loop, so a cancelled context still
	// produces output rather than exiting silently.
	for _, want := range []string{"Claude Pro", "Sample", "Opus", "High Effort"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not carry %q: %q", want, out.String())
		}
	}
}

// The two documents are read independently, so one failing must not hide the
// other's rows or stop the poll from being reported.
func TestRunReportsAnUnreadableDocumentWithoutStopping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := run(ctx, &out, Wiring{
		Config:  config.Default(),
		Updates: make(chan struct{}),
		Providers: oneProvider(stubCLI{
			accountErr: errors.New("state document is unreadable"),
			prefs:      &identity.Preferences{Model: "opus"},
		}),
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() = %v, want context.Canceled", err)
	}
	if !strings.Contains(out.String(), "account details unavailable") {
		t.Errorf("output does not disclose the failure: %q", out.String())
	}
	// The settings document was readable, so its row survives the other's failure.
	if !strings.Contains(out.String(), "Opus") {
		t.Errorf("output lost the preferences row: %q", out.String())
	}
	// Quota figures must survive an account that cannot be read.
	if !strings.Contains(out.String(), "Session (5h)") {
		t.Errorf("output lost the meter rows: %q", out.String())
	}
}

func TestRunReportsUnreadablePreferencesWithoutStopping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := run(ctx, &out, Wiring{
		Config:  config.Default(),
		Updates: make(chan struct{}),
		Providers: oneProvider(stubCLI{
			account:  &identity.Account{DisplayName: "Sample", Email: "sample@example.com"},
			prefsErr: errors.New("settings document is unreadable"),
		}),
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() = %v, want context.Canceled", err)
	}
	if !strings.Contains(out.String(), "CLI preferences unavailable") {
		t.Errorf("output does not disclose the failure: %q", out.String())
	}
	for _, want := range []string{"Sample", "sample@example.com", "Session (5h)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lost %q to the other document's failure: %q", want, out.String())
		}
	}
}
