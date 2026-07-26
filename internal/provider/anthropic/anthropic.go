// Package anthropic polls the undocumented consumer-subscription usage
// endpoint that the official Claude CLI uses. There is no public API for
// Claude Pro/Max quota; this endpoint's shape is confirmed only by
// observation and may change without notice, so every field is optional and
// an unrecognized document degrades to a Protocol error rather than a panic.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
	"github.com/larahfelipe/meterai/internal/quota"
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
	oauthBetaHeader     = "anthropic-beta"
	oauthBetaValue      = "oauth-2025-04-20"
	clientUserAgent     = "meterAI/0.1 (usage-monitor)"
	requestTimeout      = 20 * time.Second
	maxResponseBytes    = 1 << 20
	credentialSkewGuard = 5 * time.Minute
)

// CredentialSource supplies a currently-valid access token. It is an interface
// so that re-reading the credential file, which may live inside WSL, stays out
// of this package entirely.
type CredentialSource interface {
	Token(ctx context.Context) (*credential.Credentials, error)
}

// Provider implements quota.Provider for Anthropic consumer subscriptions.
type Provider struct {
	creds  CredentialSource
	client *http.Client
	// now is injected for deterministic tests.
	now func() time.Time
}

// New builds a Provider. A nil httpClient selects one with a bounded timeout;
// Go's default redirect policy already strips the Authorization header on a
// cross-host redirect, which is what prevents the token from reaching a third
// party if the endpoint is ever redirected.
func New(creds CredentialSource, httpClient *http.Client) *Provider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Provider{creds: creds, client: httpClient, now: time.Now}
}

func (p *Provider) Vendor() string { return VendorKey }

func (p *Provider) Fetch(ctx context.Context) (*quota.Snapshot, error) {
	creds, err := p.creds.Token(ctx)
	if err != nil {
		return nil, &quota.FetchError{Kind: quota.Unauthorized, Vendor: VendorKey, Err: err}
	}
	if !creds.IsUsableAt(p.now(), credentialSkewGuard) {
		return nil, &quota.FetchError{Kind: quota.Unauthorized, Vendor: VendorKey,
			Err: fmt.Errorf("access token expired at %s; run the claude CLI to renew it",
				creds.ExpiresAt.Format(time.RFC3339))}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageEndpoint, nil)
	if err != nil {
		return nil, &quota.FetchError{Kind: quota.Protocol, Vendor: VendorKey, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken.Reveal())
	req.Header.Set(oauthBetaHeader, oauthBetaValue)
	req.Header.Set("User-Agent", clientUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &quota.FetchError{Kind: quota.Transient, Vendor: VendorKey, Err: err}
	}
	defer func() {
		// Drain before close so the connection returns to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, &quota.FetchError{
			Kind:       classify(resp.StatusCode),
			Vendor:     VendorKey,
			Status:     resp.StatusCode,
			RetryAfter: quota.ParseRetryAfter(resp.Header.Get("Retry-After"), p.now()),
			Err:        errors.New(http.StatusText(resp.StatusCode)),
		}
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &quota.FetchError{Kind: quota.Transient, Vendor: VendorKey, Status: resp.StatusCode, Err: err}
	}
	if len(raw) > maxResponseBytes {
		return nil, &quota.FetchError{Kind: quota.Protocol, Vendor: VendorKey, Status: resp.StatusCode,
			Err: fmt.Errorf("response exceeds %d byte limit", maxResponseBytes)}
	}

	snapshot, err := decode(raw, creds.SubscriptionType, p.now().UTC())
	if err != nil {
		return nil, &quota.FetchError{Kind: quota.Protocol, Vendor: VendorKey, Status: resp.StatusCode, Err: err}
	}
	return snapshot, nil
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

// usageDocument covers only the fields this client relies on. The live response
// also carries internal codename keys with null values; every unknown field is
// ignored so that its appearance or removal cannot break parsing.
type usageDocument struct {
	// Limits is the normalized array and the preferred source.
	Limits []limitEntry `json:"limits"`
	// FiveHour and SevenDay are the older flat form, read only when Limits is
	// absent so that an endpoint rollback does not blind the app.
	FiveHour *legacyWindow `json:"five_hour"`
	SevenDay *legacyWindow `json:"seven_day"`
	Spend    *spendBlock   `json:"spend"`
}

type limitEntry struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	Severity string   `json:"severity"`
	ResetsAt string   `json:"resets_at"`
	IsActive bool     `json:"is_active"`
}

type legacyWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type spendBlock struct {
	Used struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Exponent    uint8  `json:"exponent"`
	} `json:"used"`
	Limit *struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Exponent    uint8  `json:"exponent"`
	} `json:"limit"`
	Percent  *float64 `json:"percent"`
	Severity string   `json:"severity"`
	Enabled  bool     `json:"enabled"`
}

const (
	legacyFiveHourKind = "session"
	legacySevenDayKind = "weekly_all"
	spendMeterKind     = "spend"
)

func decode(raw []byte, plan string, observedAt time.Time) (*quota.Snapshot, error) {
	var doc usageDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode usage document: %w", err)
	}

	snapshot := &quota.Snapshot{Vendor: VendorKey, Product: productName, ObservedAt: observedAt, Plan: plan}
	snapshot.Meters = normalizedWindows(doc.Limits)
	if len(snapshot.Meters) == 0 {
		snapshot.Meters = legacyWindows(doc.FiveHour, doc.SevenDay)
	}
	if balance := spendBalance(doc.Spend); balance != nil {
		snapshot.Meters = append(snapshot.Meters, balance)
	}

	if len(snapshot.Meters) == 0 {
		return nil, errors.New("usage document carried no recognizable meter; the endpoint schema likely changed")
	}
	return snapshot, nil
}

// normalizedWindows reads the limits array, skipping entries too incomplete to
// display rather than rendering them as zero.
func normalizedWindows(entries []limitEntry) []quota.Meter {
	var meters []quota.Meter
	for i := range entries {
		entry := &entries[i]
		if entry.Kind == "" || entry.Percent == nil {
			continue
		}
		meters = append(meters, &quota.Utilization{
			MeterID:  meterID(entry.Kind),
			Name:     entry.Kind,
			Percent:  *entry.Percent,
			Reset:    parseInstant(entry.ResetsAt),
			Level:    parseSeverity(entry.Severity),
			IsActive: entry.IsActive,
		})
	}
	return meters
}

// legacyWindows maps the pre-limits flat form. The windows are listed
// positionally rather than iterated from a map so that the meter order, and
// therefore the UI row order, cannot vary between polls.
func legacyWindows(fiveHour, sevenDay *legacyWindow) []quota.Meter {
	var meters []quota.Meter
	for _, w := range []struct {
		kind   string
		window *legacyWindow
	}{
		{legacyFiveHourKind, fiveHour},
		{legacySevenDayKind, sevenDay},
	} {
		if w.window == nil || w.window.Utilization == nil {
			continue
		}
		meters = append(meters, &quota.Utilization{
			MeterID: meterID(w.kind),
			Name:    w.kind,
			Percent: *w.window.Utilization,
			Reset:   parseInstant(w.window.ResetsAt),
			Level:   quota.SeverityNormal,
		})
	}
	return meters
}

// spendBalance returns nil when the account has no spend allowance enabled, so
// that a disabled feature produces no meter rather than a zeroed one.
func spendBalance(s *spendBlock) *quota.Balance {
	if s == nil || !s.Enabled {
		return nil
	}
	balance := &quota.Balance{
		MeterID: meterID(spendMeterKind),
		Name:    spendMeterKind,
		Used:    quota.Money{AmountMinor: s.Used.AmountMinor, Currency: s.Used.Currency, Exponent: s.Used.Exponent},
		Level:   parseSeverity(s.Severity),
	}
	if s.Percent != nil {
		balance.Percent = *s.Percent
	}
	if s.Limit != nil {
		balance.Limit = &quota.Money{AmountMinor: s.Limit.AmountMinor, Currency: s.Limit.Currency, Exponent: s.Limit.Exponent}
	}
	return balance
}

func meterID(kind string) quota.MeterID { return quota.MeterID(VendorKey + ":" + kind) }

func parseSeverity(s string) quota.Severity {
	switch s {
	case "warning":
		return quota.SeverityWarning
	case "critical", "exceeded":
		return quota.SeverityCritical
	default:
		return quota.SeverityNormal
	}
}

// parseInstant yields the zero time for absent or unparseable timestamps: a
// missing reset instant degrades the display but must never fail a poll.
func parseInstant(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
