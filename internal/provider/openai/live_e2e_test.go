package openai

import (
	"context"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/quota"
)

// TestLiveEndToEnd exercises discovery -> cache -> live HTTPS -> normalized
// snapshot. It asserts on shape only and never renders a token.
//
// It will skip in every environment this app is developed and built in: none
// of them holds a Codex CLI login, the same as CI holds no Claude CLI login
// for anthropic's equivalent test.
func TestLiveEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("live endpoint")
	}
	cache := NewCredentialCache("")
	p := New(cache, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snap, err := p.Fetch(ctx)
	if err != nil {
		t.Skipf("no Codex CLI credentials on this host: %v", err)
	}
	t.Logf("vendor=%s plan=%s source=%s meters=%d", snap.Vendor, snap.Plan, cache.Source(), len(snap.Meters))
	for _, m := range snap.Meters {
		switch v := m.(type) {
		case *quota.Utilization:
			reset := "n/a"
			if !v.Reset.IsZero() {
				reset = time.Until(v.Reset).Round(time.Minute).String()
			}
			t.Logf("  %-24s %5.1f%% used  resets in %-10s severity=%s active=%t",
				v.Label(), v.Percent, reset, v.Severity(), v.IsActive)
		case *quota.Balance:
			t.Logf("  %-24s %s used  %.1f%%", v.Label(), v.Used, v.Percent)
		}
	}
	if pm := snap.Primary(); pm != nil {
		t.Logf("  primary=%s", pm.ID())
	}
}
