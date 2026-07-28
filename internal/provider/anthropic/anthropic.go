// Package anthropic polls the undocumented consumer-subscription usage
// endpoint that the official Claude CLI uses. There is no public API for
// Claude Pro/Max quota; this endpoint's shape is confirmed only by
// observation and may change without notice, so every field is optional and
// an unrecognized document degrades to a Protocol error rather than a panic.
//
// The package is the vendor half of a provider: which URL, which headers,
// which document shape. The HTTP conversation around it — the token, the
// bounded read, the error taxonomy — belongs to internal/provider/usageapi and
// is identical for every vendor.
package anthropic

import (
	"net/http"

	"github.com/larahfelipe/meterai/internal/credential"
	"github.com/larahfelipe/meterai/internal/provider/usageapi"
)

const (
	// VendorKey namespaces this provider's MeterIDs and must stay stable.
	VendorKey = "anthropic"

	// productName is what the subscription is sold as. It is not derived from
	// VendorKey: the company and the product have different names, and only the
	// key has to stay stable across releases.
	productName = "Claude"

	usageEndpoint = "https://api.anthropic.com/api/oauth/usage"

	// oauthBetaHeader gates the OAuth-token path on Anthropic's edge. Omitting
	// it is not known to work.
	oauthBetaHeader = "anthropic-beta"
	oauthBetaValue  = "oauth-2025-04-20"

	// renewHint names the CLI command that clears an Unauthorized failure.
	renewHint = "claude"
)

// Endpoint describes this vendor to the shared client. It is a function rather
// than a package variable so no caller can mutate the URL a bearer token is
// sent to after the process has started.
func Endpoint() usageapi.Endpoint {
	return usageapi.Endpoint{
		Vendor:    VendorKey,
		URL:       usageEndpoint,
		RenewHint: renewHint,
		Headers: func(_ *credential.Credentials, header http.Header) {
			header.Set(oauthBetaHeader, oauthBetaValue)
		},
		Decode: decode,
	}
}

// New builds this vendor's provider. A nil httpClient selects a default with a
// bounded timeout.
func New(creds usageapi.CredentialSource, httpClient *http.Client) *usageapi.Client {
	return usageapi.New(Endpoint(), creds, httpClient)
}
