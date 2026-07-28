package usageapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
	"github.com/larahfelipe/meterai/internal/quota"
)

// These tests assert the half of a provider that no vendor owns: the token
// handling, the status taxonomy, the read bound and the header discipline. They
// live here rather than in each vendor package because a copy per vendor is a
// copy that can drift, and every one of these behaviours drives either the
// retry cadence or what the user is told to do (§3.3).

var (
	observedAt   = time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)
	tokenExpiry  = observedAt.Add(time.Hour)
	testVendor   = "testvendor"
	testRenew    = "testvendor login"
	testSnapshot = &quota.Snapshot{Vendor: "testvendor"}
)

// stubCredentials serves a fixed credential without touching the filesystem.
type stubCredentials struct {
	creds *credential.Credentials
	err   error
}

func (s stubCredentials) Token(context.Context) (*credential.Credentials, error) {
	return s.creds, s.err
}

func validCredentials() *credential.Credentials {
	return &credential.Credentials{
		AccessToken:      credential.Secret("access-TEST"),
		ExpiresAt:        tokenExpiry,
		SubscriptionType: "pro",
		AccountID:        "acct-123",
		Source:           "stub",
	}
}

// recordingDecoder captures what Fetch hands a vendor decoder and returns a
// fixed snapshot, so the contract between the two can be asserted directly.
type recordingDecoder struct {
	raw        []byte
	plan       string
	observedAt time.Time
	err        error
}

func (d *recordingDecoder) decode(raw []byte, plan string, observedAt time.Time) (*quota.Snapshot, error) {
	d.raw, d.plan, d.observedAt = raw, plan, observedAt
	if d.err != nil {
		return nil, d.err
	}
	return testSnapshot, nil
}

// newTestClient points a Client at a test server without mutating the Endpoint's
// URL: the transport rewrites the host, exactly as the shipping client would be
// unable to.
func newTestClient(t *testing.T, endpoint Endpoint, handler http.HandlerFunc, creds CredentialSource) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New(endpoint, creds, &http.Client{Transport: rewriteHost{base: server.URL}})
	c.now = func() time.Time { return observedAt }
	return c
}

func testEndpoint(decode Decoder) Endpoint {
	return Endpoint{
		Vendor:    testVendor,
		URL:       "https://vendor.invalid/usage",
		RenewHint: testRenew,
		Decode:    decode,
	}
}

// rewriteHost redirects the Endpoint's fixed URL to the test server.
type rewriteHost struct {
	base string
	next http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := http.NewRequest(req.Method, r.base+req.URL.Path, req.Body)
	if err != nil {
		return nil, err
	}
	target.Header = req.Header
	next := r.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(target.WithContext(req.Context()))
}

func okHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestVendorReportsTheEndpointKey(t *testing.T) {
	if got := New(testEndpoint(nil), stubCredentials{}, nil).Vendor(); got != testVendor {
		t.Errorf("Vendor() = %q, want %q", got, testVendor)
	}
}

func TestNewAppliesABoundedTimeoutWhenNoClientIsGiven(t *testing.T) {
	c := New(testEndpoint(nil), stubCredentials{}, nil)
	if c.client == nil {
		t.Fatal("New(nil) must build its own client rather than leave it nil")
	}
	if c.client.Timeout != requestTimeout {
		t.Errorf("client.Timeout = %v, want %v", c.client.Timeout, requestTimeout)
	}
}

func TestFetchSurfacesCredentialSourceFailureAsUnauthorized(t *testing.T) {
	sourceErr := errors.New("no candidate path holds CLI credentials")
	c := newTestClient(t, testEndpoint(nil), func(http.ResponseWriter, *http.Request) {
		t.Error("a failed credential lookup must not reach the network")
	}, stubCredentials{err: sourceErr})

	_, err := c.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Unauthorized {
		t.Fatalf("want Unauthorized, got %v", err)
	}
	if fe.RenewHint != testRenew {
		t.Errorf("RenewHint = %q, want %q", fe.RenewHint, testRenew)
	}
	if fe.Vendor != testVendor {
		t.Errorf("Vendor = %q, want %q", fe.Vendor, testVendor)
	}
	if !errors.Is(err, sourceErr) {
		t.Error("the underlying credential failure must remain reachable through errors.Is")
	}
}

func TestFetchRefusesAnExpiredTokenWithoutReachingTheNetwork(t *testing.T) {
	c := newTestClient(t, testEndpoint(nil), func(http.ResponseWriter, *http.Request) {
		t.Error("expired credentials must not reach the network")
	}, stubCredentials{creds: validCredentials()})
	c.now = func() time.Time { return tokenExpiry.Add(time.Second) }

	_, err := c.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Unauthorized {
		t.Fatalf("want Unauthorized, got %v", err)
	}
	if fe.RenewHint != testRenew {
		t.Errorf("RenewHint = %q, want %q", fe.RenewHint, testRenew)
	}
}

// The guard has to fire before expiry, not at it: a token that survives the
// check but dies in flight costs a poll and a spurious Unauthorized.
func TestFetchRefusesATokenInsideTheSkewGuard(t *testing.T) {
	c := newTestClient(t, testEndpoint(nil), func(http.ResponseWriter, *http.Request) {
		t.Error("a token inside the skew guard must not reach the network")
	}, stubCredentials{creds: validCredentials()})
	c.now = func() time.Time { return tokenExpiry.Add(-credentialSkewGuard + time.Second) }

	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("want Unauthorized inside the skew guard")
	}
}

// The expiry instant is diagnostic and belongs in the message; the token is
// never allowed anywhere near one.
func TestFetchNeverPutsTheTokenInAnError(t *testing.T) {
	c := newTestClient(t, testEndpoint(nil), func(http.ResponseWriter, *http.Request) {},
		stubCredentials{creds: validCredentials()})
	c.now = func() time.Time { return tokenExpiry.Add(time.Second) }

	_, err := c.Fetch(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "access-TEST") {
		t.Errorf("the access token reached an error message: %v", err)
	}
	if !strings.Contains(err.Error(), testRenew) {
		t.Errorf("the message must name the vendor's own renewal command, got %v", err)
	}
}

func TestFetchSetsTheFixedHeaders(t *testing.T) {
	var got http.Header
	c := newTestClient(t, testEndpoint(new(recordingDecoder).decode), func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}, stubCredentials{creds: validCredentials()})

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Get("Authorization") != "Bearer access-TEST" {
		t.Errorf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", got.Get("Accept"))
	}
	if agent := got.Get("User-Agent"); agent != clientUserAgent {
		t.Errorf("User-Agent = %q, want %q", agent, clientUserAgent)
	}
}

func TestFetchAppliesTheVendorHeaderHook(t *testing.T) {
	endpoint := testEndpoint(new(recordingDecoder).decode)
	endpoint.Headers = func(creds *credential.Credentials, header http.Header) {
		header.Set("X-Vendor-Account", creds.AccountID)
	}
	var got http.Header
	c := newTestClient(t, endpoint, func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}, stubCredentials{creds: validCredentials()})

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Get("X-Vendor-Account") != "acct-123" {
		t.Errorf("X-Vendor-Account = %q, want the credential's account id", got.Get("X-Vendor-Account"))
	}
}

// The hook runs first precisely so it cannot displace the credentials this
// package is responsible for putting on the wire.
func TestFetchHeaderHookCannotDisplaceTheAuthorization(t *testing.T) {
	endpoint := testEndpoint(new(recordingDecoder).decode)
	endpoint.Headers = func(_ *credential.Credentials, header http.Header) {
		header.Set("Authorization", "Bearer attacker-supplied")
		header.Set("User-Agent", "something-else")
	}
	var got http.Header
	c := newTestClient(t, endpoint, func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}, stubCredentials{creds: validCredentials()})

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Get("Authorization") != "Bearer access-TEST" {
		t.Errorf("Authorization = %q, want the credential's own token", got.Get("Authorization"))
	}
	if got.Get("User-Agent") != clientUserAgent {
		t.Errorf("User-Agent = %q, want the build's own agent", got.Get("User-Agent"))
	}
}

func TestFetchAllowsANilHeaderHook(t *testing.T) {
	endpoint := testEndpoint(new(recordingDecoder).decode)
	endpoint.Headers = nil
	c := newTestClient(t, endpoint, okHandler(`{}`), stubCredentials{creds: validCredentials()})

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("a vendor needing no extra header must not have to supply a hook: %v", err)
	}
}

// failingTransport always fails at the transport level, regardless of the
// request, to exercise the Transient classification of a network error.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestFetchClassifiesATransportFailureAsTransient(t *testing.T) {
	c := New(testEndpoint(nil), stubCredentials{creds: validCredentials()},
		&http.Client{Transport: failingTransport{err: errors.New("connection refused")}})
	c.now = func() time.Time { return observedAt }

	_, err := c.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Transient {
		t.Fatalf("want Transient, got %v", err)
	}
}

func TestFetchClassifiesStatuses(t *testing.T) {
	cases := map[int]quota.FetchErrorKind{
		http.StatusUnauthorized:        quota.Unauthorized,
		http.StatusForbidden:           quota.Unauthorized,
		http.StatusTooManyRequests:     quota.RateLimited,
		http.StatusInternalServerError: quota.Transient,
		http.StatusBadGateway:          quota.Transient,
		http.StatusServiceUnavailable:  quota.Transient,
		http.StatusNotFound:            quota.Protocol,
		http.StatusBadRequest:          quota.Protocol,
		http.StatusMovedPermanently:    quota.Protocol,
	}
	for status, wantKind := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := newTestClient(t, testEndpoint(nil), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
			}, stubCredentials{creds: validCredentials()})

			_, err := c.Fetch(context.Background())
			var fe *quota.FetchError
			if !errors.As(err, &fe) {
				t.Fatalf("want *quota.FetchError, got %v", err)
			}
			if fe.Kind != wantKind {
				t.Errorf("Kind = %v, want %v", fe.Kind, wantKind)
			}
			if fe.Status != status {
				t.Errorf("Status = %d, want %d", fe.Status, status)
			}
			if fe.RetryAfter != 2*time.Minute {
				t.Errorf("RetryAfter = %v, want the header the vendor supplied", fe.RetryAfter)
			}
			// The renewal hint is what the UI turns into "run `x` to renew".
			// Attaching it to a rate limit or an outage would instruct the user to
			// re-authenticate over a failure re-authenticating cannot clear.
			wantHint := ""
			if wantKind == quota.Unauthorized {
				wantHint = testRenew
			}
			if fe.RenewHint != wantHint {
				t.Errorf("RenewHint = %q, want %q", fe.RenewHint, wantHint)
			}
		})
	}
}

func TestFetchRejectsAnOversizedResponseBody(t *testing.T) {
	c := newTestClient(t, testEndpoint(func([]byte, string, time.Time) (*quota.Snapshot, error) {
		t.Error("a body over the limit must never reach the vendor decoder")
		return nil, nil
	}), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
	}, stubCredentials{creds: validCredentials()})

	_, err := c.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Protocol {
		t.Fatalf("want Protocol, got %v", err)
	}
}

func TestFetchAcceptsABodyExactlyAtTheLimit(t *testing.T) {
	decoder := &recordingDecoder{}
	const prefix, suffix = `{"pad":"`, `"}`
	body := []byte(prefix + strings.Repeat("x", maxResponseBytes-len(prefix)-len(suffix)) + suffix)
	c := newTestClient(t, testEndpoint(decoder.decode), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}, stubCredentials{creds: validCredentials()})

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("a body exactly at the limit must be accepted: %v", err)
	}
	if len(decoder.raw) != maxResponseBytes {
		t.Errorf("decoder saw %d bytes, want the full %d", len(decoder.raw), maxResponseBytes)
	}
}

func TestFetchReportsADecoderFailureAsProtocol(t *testing.T) {
	decoder := &recordingDecoder{err: errors.New("schema changed")}
	c := newTestClient(t, testEndpoint(decoder.decode), okHandler(`{}`),
		stubCredentials{creds: validCredentials()})

	_, err := c.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Protocol {
		t.Fatalf("want Protocol, got %v", err)
	}
	if fe.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200: the transport succeeded and the document did not", fe.Status)
	}
}

// The decoder gets the bytes, the plan label off the credential file, and the
// injected clock — and nothing else. Handing it the credentials would put a
// bearer token inside a parser for an undocumented remote document.
func TestFetchHandsTheDecoderTheBytesPlanAndClock(t *testing.T) {
	decoder := &recordingDecoder{}
	c := newTestClient(t, testEndpoint(decoder.decode), okHandler(`{"a":1}`),
		stubCredentials{creds: validCredentials()})

	snap, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap != testSnapshot {
		t.Error("Fetch must return the decoder's snapshot unchanged")
	}
	if string(decoder.raw) != `{"a":1}` {
		t.Errorf("decoder raw = %q", decoder.raw)
	}
	if decoder.plan != "pro" {
		t.Errorf("plan = %q, want the credential file's subscription label", decoder.plan)
	}
	if !decoder.observedAt.Equal(observedAt) || decoder.observedAt.Location() != time.UTC {
		t.Errorf("observedAt = %v, want the injected clock in UTC", decoder.observedAt)
	}
}

// A cancelled context must abort the poll rather than be discovered after it.
func TestFetchHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newTestClient(t, testEndpoint(new(recordingDecoder).decode), func(http.ResponseWriter, *http.Request) {
		t.Error("a cancelled context must not reach the network")
	}, stubCredentials{creds: validCredentials()})

	_, err := c.Fetch(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
