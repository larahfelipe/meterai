package quota

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestMoneyFormatsExactly(t *testing.T) {
	cases := map[string]struct {
		money Money
		want  string
	}{
		"two decimals":        {Money{AmountMinor: 1234, Currency: "USD", Exponent: 2}, "12.34 USD"},
		"zero":                {Money{AmountMinor: 0, Currency: "USD", Exponent: 2}, "0.00 USD"},
		"sub-unit":            {Money{AmountMinor: 7, Currency: "USD", Exponent: 2}, "0.07 USD"},
		"whole units":         {Money{AmountMinor: 500, Currency: "JPY", Exponent: 0}, "500 JPY"},
		"three decimals":      {Money{AmountMinor: 1500, Currency: "BHD", Exponent: 3}, "1.500 BHD"},
		"negative":            {Money{AmountMinor: -250, Currency: "USD", Exponent: 2}, "-2.50 USD"},
		"missing currency":    {Money{AmountMinor: 100, Exponent: 2}, "1.00 ?"},
		"exponent past bound": {Money{AmountMinor: 1, Currency: "USD", Exponent: 30}, "0.000001 USD"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.money.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMoneyKeepsFullInt64Range(t *testing.T) {
	// Formatting must not overflow or lose digits at the extremes, since the
	// minor-unit value is the authoritative figure. math.MinInt64 in particular
	// has no representable negation, so a naive sign flip corrupts the output.
	cases := map[int64]string{
		math.MaxInt64: "92233720368547758.07 USD",
		math.MinInt64: "-92233720368547758.08 USD",
	}
	for amount, want := range cases {
		if got := (Money{AmountMinor: amount, Currency: "USD", Exponent: 2}).String(); got != want {
			t.Errorf("Money(%d) = %q, want %q", amount, got, want)
		}
	}
}

func TestUtilizationImplementsMeter(t *testing.T) {
	reset := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	var m Meter = &Utilization{MeterID: "v:session", Name: "Session", Percent: 42, Reset: reset, Level: SeverityWarning}

	if m.ID() != "v:session" {
		t.Errorf("ID() = %q, want %q", m.ID(), "v:session")
	}
	if m.Label() != "Session" {
		t.Errorf("Label() = %q, want %q", m.Label(), "Session")
	}
	if m.Severity() != SeverityWarning {
		t.Errorf("Severity() = %v, want %v", m.Severity(), SeverityWarning)
	}
	if !m.ResetsAt().Equal(reset) {
		t.Errorf("ResetsAt() = %v, want %v", m.ResetsAt(), reset)
	}
}

func TestBalanceImplementsMeter(t *testing.T) {
	reset := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	var m Meter = &Balance{MeterID: "v:credits", Name: "Credits", Level: SeverityCritical, Reset: reset}

	if m.ID() != "v:credits" {
		t.Errorf("ID() = %q, want %q", m.ID(), "v:credits")
	}
	if m.Label() != "Credits" {
		t.Errorf("Label() = %q, want %q", m.Label(), "Credits")
	}
	if m.Severity() != SeverityCritical {
		t.Errorf("Severity() = %v, want %v", m.Severity(), SeverityCritical)
	}
	if !m.ResetsAt().Equal(reset) {
		t.Errorf("ResetsAt() = %v, want %v", m.ResetsAt(), reset)
	}
}

func utilization(id MeterID, percent float64, active bool) *Utilization {
	return &Utilization{MeterID: id, Percent: percent, IsActive: active, Level: SeverityNormal}
}

func TestPrimaryPrefersTheActiveWindow(t *testing.T) {
	// The vendor declares which window currently binds; a higher percentage on
	// an inactive window must not displace it.
	snapshot := &Snapshot{Meters: []Meter{
		utilization("v:session", 95, false),
		utilization("v:weekly", 40, true),
	}}
	if got := snapshot.Primary().ID(); got != "v:weekly" {
		t.Errorf("Primary = %q, want the active window", got)
	}
}

func TestPrimaryFallsBackToTheHighest(t *testing.T) {
	snapshot := &Snapshot{Meters: []Meter{
		utilization("v:a", 12, false),
		utilization("v:b", 73, false),
		utilization("v:c", 55, false),
	}}
	if got := snapshot.Primary().ID(); got != "v:b" {
		t.Errorf("Primary = %q, want the highest utilization", got)
	}
}

func TestPrimaryAmongSeveralActive(t *testing.T) {
	snapshot := &Snapshot{Meters: []Meter{
		utilization("v:a", 20, true),
		utilization("v:b", 80, true),
	}}
	if got := snapshot.Primary().ID(); got != "v:b" {
		t.Errorf("Primary = %q, want the highest of the active windows", got)
	}
}

func TestPrimaryConsidersBalances(t *testing.T) {
	snapshot := &Snapshot{Meters: []Meter{
		utilization("v:session", 10, false),
		&Balance{MeterID: "v:credits", Percent: 90, Level: SeverityWarning},
	}}
	if got := snapshot.Primary().ID(); got != "v:credits" {
		t.Errorf("Primary = %q, want the balance meter", got)
	}
}

func TestPrimaryOnEmptySnapshot(t *testing.T) {
	if got := (&Snapshot{}).Primary(); got != nil {
		t.Errorf("Primary = %v, want nil", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 25, 18, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"0":                             0,
		"-5":                            0,
		"90":                            90 * time.Second,
		" 90 ":                          90 * time.Second,
		"Sat, 25 Jul 2026 18:02:00 UTC": 2 * time.Minute,
		// A date already in the past means "retry now", not "wait forever".
		"Sat, 25 Jul 2026 17:00:00 UTC": 0,
		"nonsense":                      0,
		// A delay large enough to overflow time.Duration must not come back
		// negative, which the poller would read as "the vendor asked for nothing".
		"9223372036854775807":           maxRetryAfter,
		"999999999999":                  maxRetryAfter,
		"Mon, 25 Jul 2050 18:00:00 UTC": maxRetryAfter,
	}
	for header, want := range cases {
		if got := ParseRetryAfter(header, now); got != want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", header, got, want)
		}
		if got := ParseRetryAfter(header, now); got < 0 {
			t.Errorf("ParseRetryAfter(%q) = %v, which is negative", header, got)
		}
	}
}

func TestFetchErrorMessage(t *testing.T) {
	cause := errors.New("connection reset")

	transport := &FetchError{Kind: Transient, Vendor: "anthropic", Err: cause}
	if got := transport.Error(); got != "anthropic: transient: connection reset" {
		t.Errorf("transport error = %q", got)
	}

	http := &FetchError{Kind: RateLimited, Vendor: "anthropic", Status: 429, Err: cause}
	if got := http.Error(); got != "anthropic: rate-limited (HTTP 429): connection reset" {
		t.Errorf("http error = %q", got)
	}

	// Unwrapping must reach the cause so callers can match on it.
	if !errors.Is(http, cause) {
		t.Error("FetchError does not unwrap to its cause")
	}
	var target *FetchError
	if !errors.As(fmtWrap(http), &target) || target.Kind != RateLimited {
		t.Error("FetchError is not recoverable through a wrapping chain")
	}
}

// fmtWrap buries the error one level deep, as a caller adding context would.
func fmtWrap(err error) error { return errors.Join(errors.New("context"), err) }

func TestFetchErrorKindStringKnownValues(t *testing.T) {
	cases := map[FetchErrorKind]string{
		Unauthorized: "unauthorized",
		RateLimited:  "rate-limited",
		Transient:    "transient",
		Protocol:     "protocol",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("FetchErrorKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestSeverityStringKnownValues(t *testing.T) {
	cases := map[Severity]string{
		SeverityNormal:   "normal",
		SeverityWarning:  "warning",
		SeverityCritical: "critical",
	}
	for sev, want := range cases {
		if got := sev.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", sev, got, want)
		}
	}
}

func TestUnknownEnumsRenderSafely(t *testing.T) {
	if got := Severity(0).String(); got != "unknown" {
		t.Errorf("Severity(0) = %q", got)
	}
	if got := FetchErrorKind(0).String(); got != "unknown" {
		t.Errorf("FetchErrorKind(0) = %q", got)
	}
}
