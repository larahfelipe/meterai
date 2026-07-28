// Package usageapi holds the one HTTP conversation every vendor's usage
// endpoint turns out to be: take a bearer token out of that vendor's CLI
// credential file, refuse to send an expired one, GET a fixed URL, read a
// bounded body, and hand the bytes to a vendor-supplied decoder.
//
// It exists because the two things a vendor genuinely owns — which URL, which
// extra headers, which document shape — are a small fraction of that sequence,
// while the rest is where the mistakes that matter live: classifying a status
// into the wrong quota.FetchErrorKind drives both the retry cadence and the
// instruction the user is given (§3.3), an unbounded read is a memory
// exhaustion surface, and an undrained body leaks a pooled connection. Written
// once, those are asserted once and cannot drift between vendors; copied per
// vendor, every new provider is a fresh opportunity to get one of them wrong.
//
// It knows no vendor: Endpoint is the whole of what a caller contributes.
package usageapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/larahfelipe/meterai/internal/buildinfo"
	"github.com/larahfelipe/meterai/internal/credential"
	"github.com/larahfelipe/meterai/internal/quota"
)

const (
	// clientUserAgent identifies this app to every vendor endpoint. It names the
	// product and its version from one place, so the traffic cannot claim a
	// version the executable does not declare.
	clientUserAgent = buildinfo.Name + "/" + buildinfo.Version + " (usage-monitor)"

	// requestTimeout bounds one poll end to end. It is well under
	// poll.DefaultInterval, so a wedged endpoint can never overlap two polls.
	requestTimeout = 20 * time.Second

	// maxResponseBytes bounds the read. These endpoints are undocumented, which
	// makes them unbounded producers: the real documents are a few kilobytes.
	maxResponseBytes = 1 << 20

	// credentialSkewGuard is the headroom left on a token's remaining life before
	// it is treated as unusable, absorbing clock drift between this host and the
	// vendor's edge plus the round trip of a poll issued at the boundary.
	credentialSkewGuard = 5 * time.Minute
)

// CredentialSource supplies a currently-valid access token. It is an interface
// so that re-reading the credential file, which may live inside WSL, stays out
// of this package and out of every provider package.
type CredentialSource interface {
	Token(ctx context.Context) (*credential.Credentials, error)
}

// Decoder maps one vendor's usage document onto the neutral model.
//
// plan is the subscription label that vendor's credential file carried, passed
// through for the vendors whose usage response omits it; one whose response
// names its own plan ignores it. Only the label is passed, never the
// credentials: a decoder of an undocumented remote document has no business
// holding a bearer token.
//
// observedAt is the injected clock, so a decoder resolving a relative reset
// instant does not reach for time.Now and become untestable.
type Decoder func(raw []byte, plan string, observedAt time.Time) (*quota.Snapshot, error)

// Endpoint is everything that differs between vendors, and nothing that does
// not. A new provider is one of these plus a Decoder.
type Endpoint struct {
	// Vendor is the stable key namespacing that provider's MeterIDs.
	Vendor string
	// URL is the vendor's usage endpoint. It is a constant per vendor and is
	// never built from anything a response or a config file supplies, which is
	// what keeps the bearer token from reaching a destination the user's
	// settings could redirect it to.
	URL string
	// RenewHint names the vendor's own CLI command that clears an Unauthorized
	// failure ("claude", "codex login"). It reaches the user through
	// quota.FetchError.RenewHint and is set on no other kind, so the message the
	// UI builds can never name another vendor's CLI.
	RenewHint string
	// Headers contributes the request headers only this vendor needs — a beta
	// gate, an account id required beside the bearer. It may be nil.
	//
	// It runs before the fixed headers are applied, so a vendor cannot displace
	// the Authorization, User-Agent or Accept this package is responsible for.
	Headers func(creds *credential.Credentials, header http.Header)
	// Decode turns that vendor's document into a snapshot.
	Decode Decoder
}

// Client polls one Endpoint. It implements quota.Provider, is safe for
// concurrent use, and does not block past the context deadline.
type Client struct {
	endpoint Endpoint
	creds    CredentialSource
	client   *http.Client
	// now is injected for deterministic tests.
	now func() time.Time
}

// New builds a Client. A nil httpClient selects one with a bounded timeout;
// Go's default redirect policy already strips the Authorization header on a
// cross-host redirect, which is what prevents the token from reaching a third
// party if an endpoint is ever redirected.
func New(endpoint Endpoint, creds CredentialSource, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Client{endpoint: endpoint, creds: creds, client: httpClient, now: time.Now}
}

func (c *Client) Vendor() string { return c.endpoint.Vendor }

func (c *Client) Fetch(ctx context.Context) (*quota.Snapshot, error) {
	creds, err := c.creds.Token(ctx)
	if err != nil {
		return nil, c.fail(quota.Unauthorized, 0, err)
	}
	if !creds.IsUsableAt(c.now(), credentialSkewGuard) {
		// The expiry instant is not a secret and is what makes this diagnosable;
		// the token itself never appears, here or anywhere else.
		return nil, c.fail(quota.Unauthorized, 0, fmt.Errorf(
			"access token expired at %s; run %s to renew it",
			creds.ExpiresAt.Format(time.RFC3339), c.endpoint.RenewHint))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint.URL, nil)
	if err != nil {
		return nil, c.fail(quota.Protocol, 0, err)
	}
	if c.endpoint.Headers != nil {
		c.endpoint.Headers(creds, req.Header)
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken.Reveal())
	req.Header.Set("User-Agent", clientUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, c.fail(quota.Transient, 0, err)
	}
	defer func() {
		// Drain before close so the connection returns to the pool, bounded so a
		// body this app already refused to read cannot be read anyway.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		failure := c.fail(classify(resp.StatusCode), resp.StatusCode,
			errors.New(http.StatusText(resp.StatusCode)))
		// Recorded whenever the vendor supplied one, whatever the status. Only
		// poll.backoff's RateLimited branch acts on it (§3.3); the rest of the
		// time it is what makes a failure legible after the fact.
		failure.RetryAfter = quota.ParseRetryAfter(resp.Header.Get("Retry-After"), c.now())
		return nil, failure
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, c.fail(quota.Transient, resp.StatusCode, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, c.fail(quota.Protocol, resp.StatusCode,
			fmt.Errorf("response exceeds %d byte limit", maxResponseBytes))
	}

	snapshot, err := c.endpoint.Decode(raw, creds.SubscriptionType, c.now().UTC())
	if err != nil {
		return nil, c.fail(quota.Protocol, resp.StatusCode, err)
	}
	return snapshot, nil
}

// fail builds this vendor's FetchError. The renewal hint is attached to
// Unauthorized and to nothing else, which is the invariant that keeps a
// rate-limit message from telling the user to re-authenticate.
func (c *Client) fail(kind quota.FetchErrorKind, status int, err error) *quota.FetchError {
	failure := &quota.FetchError{Kind: kind, Vendor: c.endpoint.Vendor, Status: status, Err: err}
	if kind == quota.Unauthorized {
		failure.RenewHint = c.endpoint.RenewHint
	}
	return failure
}

// classify maps a non-200 status onto the failure kind. Anything unrecognized
// is Protocol rather than Transient: an endpoint that is neither succeeding nor
// failing in a known way is not something retrying will fix.
func classify(status int) quota.FetchErrorKind {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return quota.Unauthorized
	case status == http.StatusTooManyRequests:
		return quota.RateLimited
	case status >= 500:
		return quota.Transient
	default:
		return quota.Protocol
	}
}
