package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/larahfelipe/meterai/internal/quota"
)

// usageResponse covers only the fields this client relies on. Every field is
// optional: the endpoint is undocumented, and an unknown field is ignored so
// that its appearance or removal cannot break decoding.
type usageResponse struct {
	PlanType            string     `json:"plan_type"`
	RateLimit           *rateLimit `json:"rate_limit"`
	CodeReviewRateLimit *rateLimit `json:"code_review_rate_limit"`
	Credits             *credits   `json:"credits"`
}

type rateLimit struct {
	PrimaryWindow   *window `json:"primary_window"`
	SecondaryWindow *window `json:"secondary_window"`
}

type window struct {
	UsedPercent       float64 `json:"used_percent"`
	ResetAt           *int64  `json:"reset_at"`
	ResetAfterSeconds *int64  `json:"reset_after_seconds"`
}

// credits deliberately does not declare approx_local_messages/
// approx_cloud_messages: the response carries them, but nothing in this app
// displays a message-count estimate, and an unread field left undeclared here
// is simply ignored by json.Unmarshal, the same as any other field this
// client does not rely on.
type credits struct {
	Balance    string `json:"balance"`
	HasCredits bool   `json:"has_credits"`
	Unlimited  bool   `json:"unlimited"`
}

const (
	sessionMeterKind           = "session"
	weeklyMeterKind            = "weekly"
	codeReviewSessionMeterKind = "code_review_session"
	codeReviewWeeklyMeterKind  = "code_review_weekly"
	creditsMeterKind           = "credits"
)

// decode is this vendor's usageapi.Decoder. The plan the credential file
// carried is ignored: this response names the subscription itself, in
// plan_type, and the endpoint's own answer outranks a label cached beside a
// token.
func decode(raw []byte, _ string, observedAt time.Time) (*quota.Snapshot, error) {
	var doc usageResponse
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode usage document: %w", err)
	}

	snapshot := &quota.Snapshot{Vendor: VendorKey, Product: productName, ObservedAt: observedAt, Plan: doc.PlanType}

	// Order is significant (§3.7): it sets menu row order and decides which
	// meters survive the tooltip budget, so the windows are listed positionally
	// rather than gathered from anything with its own iteration order.
	for _, w := range []struct {
		kind     string
		window   *window
		isActive bool
	}{
		{sessionMeterKind, primaryWindow(doc.RateLimit), true},
		{weeklyMeterKind, secondaryWindow(doc.RateLimit), false},
		{codeReviewSessionMeterKind, primaryWindow(doc.CodeReviewRateLimit), false},
		{codeReviewWeeklyMeterKind, secondaryWindow(doc.CodeReviewRateLimit), false},
	} {
		if m := windowMeter(w.kind, w.window, w.isActive, observedAt); m != nil {
			snapshot.Meters = append(snapshot.Meters, m)
		}
	}
	if balance := creditsBalance(doc.Credits); balance != nil {
		snapshot.Meters = append(snapshot.Meters, balance)
	}

	if len(snapshot.Meters) == 0 {
		return nil, errors.New("usage document carried no recognizable meter; the endpoint schema likely changed")
	}
	return snapshot, nil
}

func primaryWindow(r *rateLimit) *window {
	if r == nil {
		return nil
	}
	return r.PrimaryWindow
}

func secondaryWindow(r *rateLimit) *window {
	if r == nil {
		return nil
	}
	return r.SecondaryWindow
}

// windowMeter maps one rate-limit window. It returns nil for an absent window
// rather than a zeroed meter, so a window the endpoint did not report does not
// render as a window at 0%.
func windowMeter(kind string, w *window, isActive bool, now time.Time) *quota.Utilization {
	if w == nil {
		return nil
	}
	return &quota.Utilization{
		MeterID:  meterID(kind),
		Name:     kind,
		Percent:  w.UsedPercent,
		Reset:    windowReset(w, now),
		Level:    quota.SeverityNormal,
		IsActive: isActive,
	}
}

// maxRelativeReset bounds the relative reset the endpoint may state. The
// longest window it is known to report is weekly; a month covers any longer one
// a vendor might introduce, and everything past that is a corrupt number rather
// than a rate-limit window.
//
// The bound is not cosmetic. reset_after_seconds is an unvalidated JSON integer,
// and a value large enough to overflow the multiplication into time.Duration
// comes back negative — a reset instant in the *past*, which the UI renders as
// "resets in moments" on a window that has barely been touched. An absent
// countdown says nothing; a confidently wrong one misinforms.
const maxRelativeReset = 31 * 24 * time.Hour

// windowReset prefers the absolute reset_at the endpoint supplies; when only a
// relative reset_after_seconds is present, it is resolved against now rather
// than against a later read, so the reset instant does not drift with poll
// jitter.
//
// Only the relative form is bounded: an absolute epoch second is a complete
// instant, and time.Unix is total over the whole int64 range.
func windowReset(w *window, now time.Time) time.Time {
	switch {
	case w.ResetAt != nil:
		return time.Unix(*w.ResetAt, 0).UTC()
	case w.ResetAfterSeconds != nil:
		seconds := *w.ResetAfterSeconds
		if seconds < 0 || seconds > int64(maxRelativeReset/time.Second) {
			return time.Time{}
		}
		return now.Add(time.Duration(seconds) * time.Second)
	default:
		return time.Time{}
	}
}

// creditsBalance returns nil when the account has no credits, or when the
// balance is unlimited (there is no ceiling to show — the same shape as
// Anthropic's disabled spend allowance producing no meter), or when the
// balance string cannot be parsed without going through float64 (§3.11): an
// unrecognized format omits the meter rather than guessing at its value.
func creditsBalance(c *credits) *quota.Balance {
	if c == nil || !c.HasCredits || c.Unlimited {
		return nil
	}
	amount, ok := parseDollarString(c.Balance)
	if !ok {
		return nil
	}
	return &quota.Balance{
		MeterID: meterID(creditsMeterKind),
		Name:    creditsMeterKind,
		Used:    amount,
		Level:   quota.SeverityNormal,
	}
}

const (
	// dollarExponent is fixed at 2 (cents): every balance observed from this
	// endpoint is USD, and the string form carries at most two fractional digits.
	dollarExponent = 2
	dollarCurrency = "USD"
	dollarSymbol   = "$"
	negativeSign   = "-"
)

// parseDollarString parses a "$12.34" (or "-$1.00") balance into minor units
// without ever going through float64, per §3.11.
//
// Both digit groups are validated before parsing rather than handed to
// strconv, which accepts a leading sign and an underscore separator: "$+1.00"
// and "$1.-1" would otherwise be read as $1.00 and $0.99 by a function whose
// whole contract is that it reports anything it does not recognize. The groups
// are then concatenated so the minor-unit value is produced by a single parse —
// scaling the whole part by hand is where an unbounded numerator silently
// overflows int64, whereas strconv reports it.
//
// Anything not matching that shape — a different currency symbol, extra
// fractional digits beyond cents, non-digit characters, a value too large to
// represent exactly — is reported as unparseable rather than approximated.
func parseDollarString(s string) (quota.Money, bool) {
	s = strings.TrimSpace(s)
	negative := strings.HasPrefix(s, negativeSign)
	if negative {
		s = s[len(negativeSign):]
	}
	if !strings.HasPrefix(s, dollarSymbol) {
		return quota.Money{}, false
	}
	s = s[len(dollarSymbol):]

	// A decimal point with nothing after it is malformed; no point at all is the
	// whole-dollar form the endpoint also writes.
	whole, frac, hasPoint := strings.Cut(s, ".")
	if !isDigits(whole) || len(frac) > dollarExponent {
		return quota.Money{}, false
	}
	if hasPoint && !isDigits(frac) {
		return quota.Money{}, false
	}
	frac += strings.Repeat("0", dollarExponent-len(frac))

	amountMinor, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return quota.Money{}, false
	}
	if negative {
		amountMinor = -amountMinor
	}
	return quota.Money{AmountMinor: amountMinor, Currency: dollarCurrency, Exponent: dollarExponent}, true
}

// isDigits reports whether s is a non-empty run of ASCII digits. The range test
// is deliberate: unicode.IsDigit would accept the decimal digits of other
// scripts, which strconv then rejects, turning a validated string into a parse
// failure two lines later.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func meterID(kind string) quota.MeterID { return quota.MeterID(VendorKey + ":" + kind) }
