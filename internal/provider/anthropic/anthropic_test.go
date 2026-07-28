package anthropic

import (
	"net/http"
	"strings"
	"testing"

	"github.com/larahfelipe/meterai/internal/credential"
)

// The HTTP conversation itself is asserted once, in internal/provider/usageapi.
// What is this vendor's own is the Endpoint it declares, and these tests cover
// exactly that: a wrong URL, a missing beta gate or a crossed renewal hint are
// the failures no shared test can see.

func TestEndpointIsWellFormed(t *testing.T) {
	endpoint := Endpoint()
	if endpoint.Vendor != VendorKey {
		t.Errorf("Vendor = %q, want the stable key %q", endpoint.Vendor, VendorKey)
	}
	// A bearer token is about to be sent here. Plaintext would put it on the
	// wire, and a host other than the vendor's would put it in someone else's log.
	if !strings.HasPrefix(endpoint.URL, "https://api.anthropic.com/") {
		t.Errorf("URL = %q, want an HTTPS URL on the vendor's own host", endpoint.URL)
	}
	if endpoint.RenewHint != renewHint {
		t.Errorf("RenewHint = %q, want %q", endpoint.RenewHint, renewHint)
	}
	if endpoint.Decode == nil {
		t.Error("Decode must be set; a nil decoder fails every poll at the last step")
	}
}

func TestEndpointHeadersGateTheOAuthPath(t *testing.T) {
	header := http.Header{}
	Endpoint().Headers(&credential.Credentials{}, header)
	if got := header.Get(oauthBetaHeader); got != oauthBetaValue {
		t.Errorf("%s = %q, want %q", oauthBetaHeader, got, oauthBetaValue)
	}
}

// This vendor's usage response names no subscription, so the plan reaching the
// snapshot has to be the one its credential file carried.
func TestEndpointDecodeTakesThePlanFromTheCredential(t *testing.T) {
	snap, err := Endpoint().Decode([]byte(liveShapedResponse), "max", observedAt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if snap.Plan != "max" {
		t.Errorf("Plan = %q, want the credential file's subscription label", snap.Plan)
	}
}

func TestNewCredentialCachePairsThisVendorsFileWithThisVendorsDecoder(t *testing.T) {
	cache := NewCredentialCache("")
	if cache == nil {
		t.Fatal("NewCredentialCache returned nil")
	}
	// The pairing is the point: a cache built with another vendor's relative path
	// would look for this CLI's schema in a file that never holds it.
	if credentialRelPath.Dir != ".claude" || credentialRelPath.File != ".credentials.json" {
		t.Errorf("credentialRelPath = %+v, want the Claude CLI's own layout", credentialRelPath)
	}
}
