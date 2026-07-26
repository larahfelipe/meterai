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
)

func TestRenderWritesEveryMeterAndTheStatusLine(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), liveState(), nil, now)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	// A blank line, the timestamp banner, the provider header, one line per meter,
	// then the status line.
	if len(lines) != 6 {
		t.Fatalf("output has %d lines, want 6:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], "meterAI") || !strings.Contains(lines[1], now.Format("2006-01-02")) {
		t.Errorf("banner = %q, want the app name and the observation instant", lines[1])
	}
	if !strings.Contains(lines[3], "Session (5h)") || !strings.Contains(lines[3], "23%") {
		t.Errorf("meter line = %q", lines[3])
	}
	if !strings.Contains(lines[5], "Updated") {
		t.Errorf("status line = %q", lines[5])
	}
}

func TestRenderWritesTheAccountItIsGiven(t *testing.T) {
	var out bytes.Buffer
	account := &identity.Account{DisplayName: "Sample", Email: "sample@example.com", Organization: "Sample Org"}
	render(&out, presenterFor(t, i18n.LangEnUS), liveState(), account, now)

	for _, want := range []string{"Claude Pro", "Sample", "sample@example.com", "Sample Org"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not carry %q:\n%s", want, out.String())
		}
	}
}

func TestRenderBeforeTheFirstPollWritesOnlyTheStatusLine(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), poll.State{}, nil, now)

	if strings.Count(out.String(), "\n") != 3 {
		t.Errorf("output = %q, want a blank line, a header and a status line", out.String())
	}
	if !strings.Contains(out.String(), "Polling…") {
		t.Errorf("output = %q, does not disclose that no reading exists yet", out.String())
	}
}

// stubController and stubAccounts stand in for the poller and the identity cache.
type stubController struct{ state poll.State }

func (s stubController) State() poll.State       { return s.state }
func (stubController) Refresh() bool             { return true }
func (stubController) SetInterval(time.Duration) {}

type stubAccounts struct {
	account *identity.Account
	err     error
}

func (s stubAccounts) Account() (*identity.Account, error) { return s.account, s.err }

func TestRunRendersOnceAndUnwindsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	cfg := config.Default()
	err := run(ctx, &out, Wiring{
		Config:     cfg,
		Updates:    make(chan struct{}),
		Controller: stubController{state: liveState()},
		Accounts:   stubAccounts{account: &identity.Account{DisplayName: "Sample"}},
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() = %v, want context.Canceled", err)
	}
	// The first reading is printed before the loop, so a cancelled context still
	// produces output rather than exiting silently.
	if !strings.Contains(out.String(), "Claude Pro") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRunReportsAnUnreadableAccountWithoutStopping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := run(ctx, &out, Wiring{
		Config:     config.Default(),
		Updates:    make(chan struct{}),
		Controller: stubController{state: liveState()},
		Accounts:   stubAccounts{err: errors.New("state document is unreadable")},
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() = %v, want context.Canceled", err)
	}
	if !strings.Contains(out.String(), "account details unavailable") {
		t.Errorf("output does not disclose the failure: %q", out.String())
	}
	// Quota figures must survive an account that cannot be read.
	if !strings.Contains(out.String(), "Session (5h)") {
		t.Errorf("output lost the meter rows: %q", out.String())
	}
}
