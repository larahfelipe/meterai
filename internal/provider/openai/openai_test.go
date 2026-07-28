package openai

import (
	"net/http"
	"strings"
	"testing"

	"github.com/larahfelipe/meterai/internal/credential"
)

// The HTTP conversation itself is asserted once, in internal/provider/usageapi.
// What is this vendor's own is the Endpoint it declares, and these tests cover
// exactly that.

func TestEndpointIsWellFormed(t *testing.T) {
	endpoint := Endpoint()
	if endpoint.Vendor != VendorKey {
		t.Errorf("Vendor = %q, want the stable key %q", endpoint.Vendor, VendorKey)
	}
	// A bearer token is about to be sent here. Plaintext would put it on the
	// wire, and a host other than the vendor's would put it in someone else's log.
	if !strings.HasPrefix(endpoint.URL, "https://chatgpt.com/") {
		t.Errorf("URL = %q, want an HTTPS URL on the vendor's own host", endpoint.URL)
	}
	if endpoint.RenewHint != renewHint {
		t.Errorf("RenewHint = %q, want %q", endpoint.RenewHint, renewHint)
	}
	if endpoint.Decode == nil {
		t.Error("Decode must be set; a nil decoder fails every poll at the last step")
	}
}

func TestEndpointHeadersCarryTheAccountID(t *testing.T) {
	header := http.Header{}
	Endpoint().Headers(&credential.Credentials{AccountID: "acct-123"}, header)
	if got := header.Get(accountHeader); got != "acct-123" {
		t.Errorf("%s = %q, want the credential's account id", accountHeader, got)
	}
}

// The endpoint reads the header's presence, so an account-less credential must
// leave it off entirely rather than send it blank.
func TestEndpointHeadersOmitTheAccountIDWhenTheCredentialCarriesNone(t *testing.T) {
	header := http.Header{}
	Endpoint().Headers(&credential.Credentials{}, header)
	if _, present := header[accountHeader]; present {
		t.Errorf("%s must be absent, got %q", accountHeader, header.Get(accountHeader))
	}
}

// This vendor's usage response names its own subscription, so the label cached
// beside the token is deliberately ignored rather than allowed to contradict it.
func TestEndpointDecodePrefersThePlanInTheResponse(t *testing.T) {
	snap, err := Endpoint().Decode([]byte(liveShapedResponse), "stale-label", observedAt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if snap.Plan != "plus" {
		t.Errorf("Plan = %q, want plan_type from the response", snap.Plan)
	}
}

func TestNewCredentialCachePairsThisVendorsFileWithThisVendorsDecoder(t *testing.T) {
	if cache := NewCredentialCache(""); cache == nil {
		t.Fatal("NewCredentialCache returned nil")
	}
	if credentialRelPath.Dir != ".codex" || credentialRelPath.File != "auth.json" {
		t.Errorf("credentialRelPath = %+v, want the Codex CLI's own layout", credentialRelPath)
	}
}
