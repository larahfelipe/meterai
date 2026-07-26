// Package quota defines the vendor-neutral usage model every provider maps
// onto, plus the shared fetch-error taxonomy. Vendors differ structurally:
// Anthropic reports rolling percentage windows, OpenRouter reports a money
// balance, others report daily counters. The Meter union below is the only
// shape the rest of the app needs to understand.
package quota

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MeterID is a "<vendor>:<kind>" key. It must stay stable for a given logical
// meter across releases, even if the vendor renames its own field.
type MeterID string

// Severity is the vendor's own assessment of how close to the limit the user
// is, normalized across vendors.
type Severity uint8

const (
	SeverityNormal Severity = iota + 1
	SeverityWarning
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityNormal:
		return "normal"
	case SeverityWarning:
		return "warning"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Meter is a sealed union: Utilization and Balance are its only possible
// implementations. A type switch over the two is exhaustive by construction,
// so no consumer can encounter an unhandled meter shape.
type Meter interface {
	ID() MeterID
	// Label is the vendor's own name for the meter, normally the raw kind string
	// it reports. Presentation layers translate by MeterID and fall back to this,
	// so a window a vendor introduces after a release still reaches the UI.
	Label() string
	Severity() Severity
	// ResetsAt is zero when the vendor exposes no reset instant, as with a
	// continuously drawn balance.
	ResetsAt() time.Time
	// sealedMeter is unexported so that no type outside this package can join
	// the union, which is what makes a type switch over it exhaustive.
	sealedMeter()
}

// Utilization is a percentage-of-allowance meter, such as the Claude 5-hour
// session window. Percent is not clamped: vendors that permit overage report
// values above 100 and the real figure is preserved.
type Utilization struct {
	MeterID MeterID
	Name    string
	Percent float64
	Reset   time.Time
	Level   Severity
	// IsActive marks the window the vendor considers currently binding when
	// several overlap. Primary tracks this one.
	IsActive bool
}

func (u *Utilization) ID() MeterID         { return u.MeterID }
func (u *Utilization) Label() string       { return u.Name }
func (u *Utilization) Severity() Severity  { return u.Level }
func (u *Utilization) ResetsAt() time.Time { return u.Reset }
func (u *Utilization) sealedMeter()        {} // union membership marker

// Balance is a monetary meter. Limit is nil when spend is uncapped.
type Balance struct {
	MeterID MeterID
	Name    string
	Used    Money
	Limit   *Money
	Percent float64
	Reset   time.Time
	Level   Severity
}

func (b *Balance) ID() MeterID         { return b.MeterID }
func (b *Balance) Label() string       { return b.Name }
func (b *Balance) Severity() Severity  { return b.Level }
func (b *Balance) ResetsAt() time.Time { return b.Reset }
func (b *Balance) sealedMeter()        {} // union membership marker

// Money is an exact minor-unit amount. Currency arithmetic never goes through
// float64: AmountMinor is authoritative and Exponent only controls display.
type Money struct {
	AmountMinor int64
	Currency    string
	Exponent    uint8
}

// maxMoneyExponent bounds the decimal places a vendor may request, so a corrupt
// response cannot drive an unbounded allocation.
const maxMoneyExponent = 6

func (m Money) String() string {
	exp := m.Exponent
	if exp > maxMoneyExponent {
		exp = maxMoneyExponent
	}
	// The magnitude is taken as uint64 because negating math.MinInt64 has no
	// representable result and would silently emit a corrupted amount.
	sign := ""
	magnitude := uint64(m.AmountMinor)
	if m.AmountMinor < 0 {
		sign = "-"
		magnitude = uint64(-(m.AmountMinor + 1)) + 1
	}
	divisor := uint64(1)
	for i := uint8(0); i < exp; i++ {
		divisor *= 10
	}
	whole := magnitude / divisor
	frac := magnitude % divisor
	currency := m.Currency
	if currency == "" {
		currency = "?"
	}
	if exp == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, currency)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, int(exp), frac, currency)
}

// Snapshot is one observation of one vendor's quota state. Meter order is
// significant: it drives display order and tooltip truncation.
type Snapshot struct {
	Vendor     string
	ObservedAt time.Time
	// Product is what the vendor calls the thing being metered ("Claude"), as
	// opposed to Vendor, which is the stable key ("anthropic"). It is display
	// only, and empty when a provider states none, in which case the UI falls
	// back to Vendor.
	Product string
	// Plan is the vendor's subscription label ("pro", "max"), for display only;
	// no behaviour keys off it.
	Plan   string
	Meters []Meter
}

// Primary returns the meter that best summarizes the snapshot, or nil when
// there are none. An active meter always outranks an inactive one regardless
// of value, because the vendor has declared which window is currently binding.
func (s *Snapshot) Primary() Meter {
	var best Meter
	var bestPercent float64
	bestActive := false
	for _, m := range s.Meters {
		percent, active := 0.0, false
		switch t := m.(type) {
		case *Utilization:
			percent, active = t.Percent, t.IsActive
		case *Balance:
			percent = t.Percent
		}
		if best == nil || (active && !bestActive) || (active == bestActive && percent > bestPercent) {
			best, bestPercent, bestActive = m, percent, active
		}
	}
	return best
}

// Provider is the extension point for new vendors. Implementations must be
// safe for concurrent use and must not block past the context deadline.
type Provider interface {
	// Vendor is the stable key namespacing this provider's MeterIDs.
	Vendor() string
	// Fetch performs one live poll. Every returned error is a *FetchError.
	Fetch(ctx context.Context) (*Snapshot, error)
}

// FetchErrorKind determines whether waiting can clear a failure, and so drives
// both the retry schedule and the message shown to the user.
type FetchErrorKind uint8

const (
	// Unauthorized: the token was rejected. Only re-authenticating with the
	// vendor's own CLI can clear it.
	Unauthorized FetchErrorKind = iota + 1
	// RateLimited: back off for at least RetryAfter.
	RateLimited
	// Transient: network failure, timeout, or 5xx.
	Transient
	// Protocol: the response could not be interpreted, meaning the
	// undocumented endpoint changed shape. Retrying cannot help.
	Protocol
)

func (k FetchErrorKind) String() string {
	switch k {
	case Unauthorized:
		return "unauthorized"
	case RateLimited:
		return "rate-limited"
	case Transient:
		return "transient"
	case Protocol:
		return "protocol"
	default:
		return "unknown"
	}
}

// FetchError is the single error type every Provider returns.
type FetchError struct {
	Kind   FetchErrorKind
	Vendor string
	// Status is the HTTP status, or 0 for transport-level failures.
	Status int
	// RetryAfter is non-zero only when the vendor supplied one.
	RetryAfter time.Duration
	Err        error
}

func (e *FetchError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %s (HTTP %d): %v", e.Vendor, e.Kind, e.Status, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Vendor, e.Kind, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

// ParseRetryAfter interprets the Retry-After header, which RFC 9110 allows to
// be either delay-seconds or an HTTP-date. now is a parameter because the
// HTTP-date form has to be resolved against a clock.
func ParseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := time.Parse(time.RFC1123, header); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
