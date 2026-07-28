package anthropic

import (
	"context"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
)

// TestDiscoverLive exercises real discovery against the host. It asserts only
// on shape, never on secret values, and is skipped in -short mode.
func TestDiscoverLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live host probe")
	}
	c, err := credential.Discover(context.Background(), "", credentialRelPath, DecodeCredentials)
	if err != nil {
		t.Skipf("no credentials on this host: %v", err)
	}
	t.Logf("source=%s plan=%s expires=%s usable=%t",
		c.Source, c.SubscriptionType,
		c.ExpiresAt.Format(time.RFC3339), c.IsUsableAt(time.Now(), 5*time.Minute))
	if len(c.AccessToken.Reveal()) < 20 {
		t.Errorf("access token implausibly short")
	}
}
