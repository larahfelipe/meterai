//go:build !windows

package tray

import (
	"bytes"
	"strings"
	"testing"

	"github.com/larahfelipe/meterai/internal/i18n"
	"github.com/larahfelipe/meterai/internal/poll"
)

func TestRenderWritesEveryMeterAndTheStatusLine(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), liveState(), now)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	// A blank line, the header, one line per meter, then the status line.
	if len(lines) != 5 {
		t.Fatalf("output has %d lines, want 5:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], "meterAI") || !strings.Contains(lines[1], now.Format("2006-01-02")) {
		t.Errorf("header = %q, want the app name and the observation instant", lines[1])
	}
	if !strings.Contains(lines[2], "Session (5h)") || !strings.Contains(lines[2], "23%") {
		t.Errorf("meter line = %q", lines[2])
	}
	if !strings.Contains(lines[4], "Updated") {
		t.Errorf("status line = %q", lines[4])
	}
}

func TestRenderBeforeTheFirstPollWritesOnlyTheStatusLine(t *testing.T) {
	var out bytes.Buffer
	render(&out, presenterFor(t, i18n.LangEnUS), poll.State{}, now)

	if strings.Count(out.String(), "\n") != 3 {
		t.Errorf("output = %q, want a blank line, a header and a status line", out.String())
	}
	if !strings.Contains(out.String(), "Polling…") {
		t.Errorf("output = %q, does not disclose that no reading exists yet", out.String())
	}
}
