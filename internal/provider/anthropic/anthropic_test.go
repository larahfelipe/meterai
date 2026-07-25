package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
	"github.com/larahfelipe/meterai/internal/quota"
)

// liveShapedResponse reproduces the exact field set returned by
// api.anthropic.com/api/oauth/usage on 2026-07-25, including the null-valued
// internal codename keys, so a parser regression against the real shape is
// caught without a network call.
const liveShapedResponse = `{
  "five_hour": {"utilization": 7.0, "resets_at": "2026-07-25T20:09:59.310897+00:00",
                "limit_dollars": null, "used_dollars": null, "remaining_dollars": null},
  "seven_day": {"utilization": 73.0, "resets_at": "2026-07-26T18:59:59.310918+00:00",
                "limit_dollars": null, "used_dollars": null, "remaining_dollars": null},
  "seven_day_oauth_apps": null, "seven_day_opus": null, "seven_day_sonnet": null,
  "seven_day_cowork": null, "seven_day_omelette": null, "tangelo": null,
  "iguana_necktie": null, "omelette_promotional": null, "nimbus_quill": null,
  "cinder_cove": null, "amber_ladder": null,
  "extra_usage": {"is_enabled": false, "monthly_limit": null, "used_credits": null,
                  "utilization": null, "currency": null, "decimal_places": null,
                  "disabled_reason": null, "user_disabled": false,
                  "spend_limit_reached": false, "credits_ever_enabled": false,
                  "daily": null, "weekly": null},
  "limits": [
    {"kind": "session", "group": "session", "percent": 7, "severity": "normal",
     "resets_at": "2026-07-25T20:09:59.310897+00:00", "scope": null, "is_active": false},
    {"kind": "weekly_all", "group": "weekly", "percent": 73, "severity": "normal",
     "resets_at": "2026-07-26T18:59:59.310918+00:00", "scope": null, "is_active": true}
  ],
  "spend": {"used": {"amount_minor": 0, "currency": "USD", "exponent": 2},
            "limit": null, "percent": 0, "severity": "normal", "enabled": false,
            "disabled_reason": null, "cap": null, "balance": null, "auto_reload": null,
            "disclaimer": "Usage credits cover you when you hit your plan limits."}
}`

var observedAt = time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)

func TestDecodeLiveShape(t *testing.T) {
	snap, err := decode([]byte(liveShapedResponse), "pro", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Vendor != VendorKey || snap.Plan != "pro" {
		t.Errorf("snapshot header = %+v", snap)
	}
	// spend.enabled is false, so no Balance meter: limits[] alone is used.
	if len(snap.Meters) != 2 {
		t.Fatalf("meters = %d, want 2", len(snap.Meters))
	}

	session, ok := snap.Meters[0].(*quota.Utilization)
	if !ok {
		t.Fatalf("meter 0 is %T, want *quota.Utilization", snap.Meters[0])
	}
	if session.MeterID != "anthropic:session" {
		t.Errorf("MeterID = %q", session.MeterID)
	}
	if session.Percent != 7 || session.IsActive {
		t.Errorf("session = %+v", session)
	}
	wantReset := time.Date(2026, 7, 25, 20, 9, 59, 310897000, time.UTC)
	if !session.Reset.Equal(wantReset) {
		t.Errorf("Reset = %v, want %v", session.Reset, wantReset)
	}

	// The active window must win the tray summary even though its percentage
	// is compared against an inactive one.
	primary := snap.Primary()
	if primary.ID() != "anthropic:weekly_all" {
		t.Errorf("Primary = %q, want anthropic:weekly_all", primary.ID())
	}
	if primary.Label() != "Semanal" {
		t.Errorf("Label = %q", primary.Label())
	}
}

func TestDecodeFallsBackToLegacyWindows(t *testing.T) {
	const noLimitsArray = `{"five_hour":{"utilization":42.5,"resets_at":"2026-07-25T20:09:59Z"},
	                        "seven_day":{"utilization":73,"resets_at":"2026-07-26T18:59:59Z"}}`
	snap, err := decode([]byte(noLimitsArray), "max", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snap.Meters) != 2 {
		t.Fatalf("meters = %d, want 2", len(snap.Meters))
	}
	byID := map[quota.MeterID]float64{}
	for _, m := range snap.Meters {
		byID[m.ID()] = m.(*quota.Utilization).Percent
	}
	if byID["anthropic:session"] != 42.5 || byID["anthropic:weekly_all"] != 73 {
		t.Errorf("legacy mapping = %v", byID)
	}
}

func TestDecodeEmitsBalanceWhenSpendEnabled(t *testing.T) {
	const withSpend = `{"limits":[{"kind":"session","percent":10,"severity":"normal","is_active":true}],
	  "spend":{"used":{"amount_minor":1234,"currency":"USD","exponent":2},
	           "limit":{"amount_minor":5000,"currency":"USD","exponent":2},
	           "percent":24.68,"severity":"warning","enabled":true}}`
	snap, err := decode([]byte(withSpend), "max", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	balance, ok := snap.Meters[len(snap.Meters)-1].(*quota.Balance)
	if !ok {
		t.Fatalf("last meter is %T, want *quota.Balance", snap.Meters[len(snap.Meters)-1])
	}
	if got := balance.Used.String(); got != "12.34 USD" {
		t.Errorf("Used = %q, want %q", got, "12.34 USD")
	}
	if balance.Limit == nil || balance.Limit.String() != "50.00 USD" {
		t.Errorf("Limit = %v", balance.Limit)
	}
	if balance.Severity() != quota.SeverityWarning {
		t.Errorf("Severity = %v", balance.Severity())
	}
}

func TestDecodeRejectsUnrecognizedDocument(t *testing.T) {
	for name, doc := range map[string]string{
		"empty object":       `{}`,
		"limits all null":    `{"limits":[{"kind":"session","percent":null}]}`,
		"spend but disabled": `{"spend":{"used":{"amount_minor":0,"currency":"USD","exponent":2},"enabled":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decode([]byte(doc), "pro", observedAt); err == nil {
				t.Fatal("want error signalling a schema change")
			}
		})
	}
}

func TestLabelFallsBackToTheRawKindForAnUnknownWindow(t *testing.T) {
	if got := label("weekly_future_thing"); got != "weekly_future_thing" {
		t.Errorf("label(unrecognized) = %q, want the raw kind returned unchanged", got)
	}
}

func TestParseSeverityMapsEveryVendorValue(t *testing.T) {
	cases := map[string]quota.Severity{
		"warning":                   quota.SeverityWarning,
		"critical":                  quota.SeverityCritical,
		"exceeded":                  quota.SeverityCritical,
		"normal":                    quota.SeverityNormal,
		"":                          quota.SeverityNormal,
		"unrecognized-future-value": quota.SeverityNormal,
	}
	for input, want := range cases {
		if got := parseSeverity(input); got != want {
			t.Errorf("parseSeverity(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestParseInstantToleratesGarbage(t *testing.T) {
	for _, s := range []string{"", "not-a-date", "2026-13-45T99:99:99Z"} {
		if got := parseInstant(s); !got.IsZero() {
			t.Errorf("parseInstant(%q) = %v, want zero", s, got)
		}
	}
}

// stubCredentials serves a fixed credential without touching the filesystem.
type stubCredentials struct {
	creds *credential.Credentials
	err   error
}

func (s stubCredentials) Token(context.Context) (*credential.Credentials, error) {
	return s.creds, s.err
}

func validCredentials() *credential.Credentials {
	c, err := credential.Parse([]byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-TEST",
		"expiresAt":1785022579691,"subscriptionType":"pro"}}`), "stub")
	if err != nil {
		panic(err)
	}
	return c
}

func newTestProvider(t *testing.T, handler http.HandlerFunc, creds CredentialSource) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p := New(creds, server.Client())
	p.now = func() time.Time { return observedAt }
	// Retarget the package endpoint at the test server without mutating the
	// exported constant: the transport rewrites the request host.
	p.client = &http.Client{Transport: rewriteHost{base: server.URL, next: server.Client().Transport}}
	return p
}

// rewriteHost redirects the fixed usageEndpoint to the test server.
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

func TestVendorReturnsTheStableKey(t *testing.T) {
	p := New(stubCredentials{}, nil)
	if p.Vendor() != VendorKey {
		t.Errorf("Vendor() = %q, want %q", p.Vendor(), VendorKey)
	}
}

func TestNewAppliesABoundedTimeoutWhenNoClientIsGiven(t *testing.T) {
	p := New(stubCredentials{}, nil)
	if p.client == nil {
		t.Fatal("New(nil) must build its own client rather than leave it nil")
	}
	if p.client.Timeout != requestTimeout {
		t.Errorf("client.Timeout = %v, want %v", p.client.Timeout, requestTimeout)
	}
}

func TestFetchSurfacesCredentialSourceFailureAsUnauthorized(t *testing.T) {
	sourceErr := errors.New("no candidate path holds Claude CLI credentials")
	p := newTestProvider(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a failed credential lookup must not reach the network")
	}, stubCredentials{err: sourceErr})

	_, err := p.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Unauthorized {
		t.Fatalf("want Unauthorized, got %v", err)
	}
	if !errors.Is(err, sourceErr) {
		t.Error("the underlying credential failure must remain reachable through errors.Is")
	}
}

// failingTransport always fails at the transport level, regardless of the
// request, to exercise Fetch's Transient classification of a network error.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestFetchClassifiesATransportFailureAsTransient(t *testing.T) {
	p := New(stubCredentials{creds: validCredentials()}, &http.Client{
		Transport: failingTransport{err: errors.New("connection refused")},
	})
	p.now = func() time.Time { return observedAt }

	_, err := p.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Transient {
		t.Fatalf("want Transient, got %v", err)
	}
}

func TestFetchRejectsAnOversizedResponseBody(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
	}, stubCredentials{creds: validCredentials()})

	_, err := p.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Protocol {
		t.Fatalf("want Protocol, got %v", err)
	}
}

func TestFetchSendsRequiredHeaders(t *testing.T) {
	var got http.Header
	p := newTestProvider(t, func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(liveShapedResponse))
	}, stubCredentials{creds: validCredentials()})

	snap, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Get("Authorization") != "Bearer sk-ant-oat01-TEST" {
		t.Errorf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get(oauthBetaHeader) != oauthBetaValue {
		t.Errorf("%s = %q", oauthBetaHeader, got.Get(oauthBetaHeader))
	}
	if !snap.ObservedAt.Equal(observedAt) {
		t.Errorf("ObservedAt = %v", snap.ObservedAt)
	}
}

func TestFetchClassifiesStatuses(t *testing.T) {
	cases := map[int]quota.FetchErrorKind{
		http.StatusUnauthorized:        quota.Unauthorized,
		http.StatusForbidden:           quota.Unauthorized,
		http.StatusTooManyRequests:     quota.RateLimited,
		http.StatusInternalServerError: quota.Transient,
		http.StatusBadGateway:          quota.Transient,
		http.StatusNotFound:            quota.Protocol,
	}
	for status, wantKind := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
			}, stubCredentials{creds: validCredentials()})

			_, err := p.Fetch(context.Background())
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
			if wantKind == quota.RateLimited && fe.RetryAfter != 2*time.Minute {
				t.Errorf("RetryAfter = %v, want 2m", fe.RetryAfter)
			}
		})
	}
}

func TestFetchRefusesExpiredToken(t *testing.T) {
	expired := validCredentials()
	p := newTestProvider(t, func(http.ResponseWriter, *http.Request) {
		t.Error("expired credentials must not reach the network")
	}, stubCredentials{creds: expired})
	// Advance the clock past the token's expiry.
	p.now = func() time.Time { return expired.ExpiresAt.Add(time.Second) }

	_, err := p.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Unauthorized {
		t.Fatalf("want Unauthorized, got %v", err)
	}
}

func TestFetchBodyGarbageIsProtocolError(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}, stubCredentials{creds: validCredentials()})

	_, err := p.Fetch(context.Background())
	var fe *quota.FetchError
	if !errors.As(err, &fe) || fe.Kind != quota.Protocol {
		t.Fatalf("want Protocol, got %v", err)
	}
}
