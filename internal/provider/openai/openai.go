// Package openai polls the undocumented usage endpoint the official Codex CLI
// uses. There is no public API for ChatGPT/Codex quota; the endpoint's shape
// is confirmed only by observation (cross-checked against two independent
// open-source implementations, github.com/akitaonrails/ai-usagebar and
// github.com/mryll/codexbar, both of which read the same document) and may
// change without notice, so every field is optional and an unrecognized
// document degrades to a Protocol error rather than a panic.
//
// The package is the vendor half of a provider: which URL, which headers,
// which document shape. The HTTP conversation around it — the token, the
// bounded read, the error taxonomy — belongs to internal/provider/usageapi and
// is identical for every vendor.
package openai

import (
	"net/http"

	"github.com/larahfelipe/meterai/internal/credential"
	"github.com/larahfelipe/meterai/internal/provider/usageapi"
)

const (
	// VendorKey namespaces this provider's MeterIDs and must stay stable.
	VendorKey = "openai"

	// productName is what the subscription is sold as. Codex is the CLI; the
	// subscription behind it is ChatGPT, the same relationship VendorKey and
	// productName carry for Anthropic/Claude.
	productName = "ChatGPT"

	usageEndpoint = "https://chatgpt.com/backend-api/wham/usage"

	// accountHeader carries the account id the usage endpoint requires
	// alongside the bearer token.
	accountHeader = "ChatGPT-Account-Id"

	// renewHint names the CLI command that clears an Unauthorized failure.
	renewHint = "codex login"
)

// Endpoint describes this vendor to the shared client. It is a function rather
// than a package variable so no caller can mutate the URL a bearer token is
// sent to after the process has started.
func Endpoint() usageapi.Endpoint {
	return usageapi.Endpoint{
		Vendor:    VendorKey,
		URL:       usageEndpoint,
		RenewHint: renewHint,
		Headers: func(creds *credential.Credentials, header http.Header) {
			// Omitted rather than sent empty: the endpoint reads the header's
			// presence, and a blank one is not the same as none.
			if creds.AccountID != "" {
				header.Set(accountHeader, creds.AccountID)
			}
		},
		Decode: decode,
	}
}

// New builds this vendor's provider. A nil httpClient selects a default with a
// bounded timeout.
func New(creds usageapi.CredentialSource, httpClient *http.Client) *usageapi.Client {
	return usageapi.New(Endpoint(), creds, httpClient)
}
