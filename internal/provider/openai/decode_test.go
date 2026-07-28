package openai

import (
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/quota"
)

var observedAt = time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)

// liveShapedResponse reproduces the field set confirmed against
// github.com/akitaonrails/ai-usagebar and github.com/mryll/codexbar.
const liveShapedResponse = `{
  "plan_type": "plus",
  "rate_limit": {
    "primary_window": {"used_percent": 18.0, "limit_window_seconds": 18000, "reset_at": 1785022579},
    "secondary_window": {"used_percent": 62.5, "limit_window_seconds": 604800, "reset_at": 1785222579}
  },
  "code_review_rate_limit": {
    "primary_window": {"used_percent": 4.0, "limit_window_seconds": 18000, "reset_at": 1785022579},
    "secondary_window": {"used_percent": 9.0, "limit_window_seconds": 604800, "reset_at": 1785222579}
  },
  "credits": {"balance": "$12.34", "has_credits": true, "unlimited": false,
              "approx_local_messages": [10, 20], "approx_cloud_messages": [5, 15]}
}`

func TestDecodeLiveShape(t *testing.T) {
	snap, err := decode([]byte(liveShapedResponse), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Vendor != VendorKey || snap.Plan != "plus" {
		t.Errorf("snapshot header = %+v", snap)
	}
	if snap.Product != "ChatGPT" {
		t.Errorf("Product = %q, want the product the subscription is sold as", snap.Product)
	}
	if len(snap.Meters) != 5 {
		t.Fatalf("meters = %d, want 5 (session, weekly, code review session, code review weekly, credits)", len(snap.Meters))
	}

	session, ok := snap.Meters[0].(*quota.Utilization)
	if !ok {
		t.Fatalf("meter 0 is %T, want *quota.Utilization", snap.Meters[0])
	}
	if session.MeterID != "openai:session" || !session.IsActive {
		t.Errorf("session = %+v, want the active 5h window", session)
	}
	if session.Percent != 18.0 {
		t.Errorf("Percent = %v", session.Percent)
	}
	wantReset := time.Unix(1785022579, 0).UTC()
	if !session.Reset.Equal(wantReset) {
		t.Errorf("Reset = %v, want %v", session.Reset, wantReset)
	}

	weekly := snap.Meters[1].(*quota.Utilization)
	if weekly.MeterID != "openai:weekly" || weekly.IsActive {
		t.Errorf("weekly = %+v, want an inactive weekly window", weekly)
	}

	codeReviewSession := snap.Meters[2].(*quota.Utilization)
	if codeReviewSession.MeterID != "openai:code_review_session" {
		t.Errorf("MeterID = %q", codeReviewSession.MeterID)
	}
	codeReviewWeekly := snap.Meters[3].(*quota.Utilization)
	if codeReviewWeekly.MeterID != "openai:code_review_weekly" {
		t.Errorf("MeterID = %q", codeReviewWeekly.MeterID)
	}

	balance, ok := snap.Meters[4].(*quota.Balance)
	if !ok {
		t.Fatalf("meter 4 is %T, want *quota.Balance", snap.Meters[4])
	}
	if balance.MeterID != "openai:credits" {
		t.Errorf("MeterID = %q", balance.MeterID)
	}
	if got := balance.Used.String(); got != "12.34 USD" {
		t.Errorf("Used = %q, want %q", got, "12.34 USD")
	}

	// The active session window must win the tray summary even though its
	// percentage is lower than the inactive weekly window.
	primary := snap.Primary()
	if primary.ID() != "openai:session" {
		t.Errorf("Primary = %q, want openai:session", primary.ID())
	}
}

func TestDecodeHandlesAWeeklyOnlyResponse(t *testing.T) {
	const weeklyOnly = `{"plan_type":"plus","rate_limit":{"secondary_window":{"used_percent":40,"reset_at":1785222579}}}`
	snap, err := decode([]byte(weeklyOnly), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snap.Meters) != 1 {
		t.Fatalf("meters = %d, want 1", len(snap.Meters))
	}
	if snap.Meters[0].ID() != "openai:weekly" {
		t.Errorf("MeterID = %q", snap.Meters[0].ID())
	}
}

func TestDecodeOmitsCodeReviewWhenAbsent(t *testing.T) {
	const noCodeReview = `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}}}`
	snap, err := decode([]byte(noCodeReview), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snap.Meters) != 1 {
		t.Fatalf("meters = %d, want 1", len(snap.Meters))
	}
}

func TestDecodeOmitsCreditsWhenUnlimited(t *testing.T) {
	const unlimited = `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}},
	                     "credits":{"balance":"$0.00","has_credits":true,"unlimited":true}}`
	snap, err := decode([]byte(unlimited), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, m := range snap.Meters {
		if m.ID() == "openai:credits" {
			t.Errorf("an unlimited balance must produce no meter, got %+v", m)
		}
	}
}

func TestDecodeOmitsCreditsWhenHasCreditsIsFalse(t *testing.T) {
	const noCredits = `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}},
	                     "credits":{"balance":"$5.00","has_credits":false,"unlimited":false}}`
	snap, err := decode([]byte(noCredits), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, m := range snap.Meters {
		if m.ID() == "openai:credits" {
			t.Errorf("has_credits=false must produce no meter, got %+v", m)
		}
	}
}

func TestDecodeOmitsAnUnparseableBalanceRatherThanFail(t *testing.T) {
	for name, doc := range map[string]string{
		"non-dollar currency": `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}},
		                          "credits":{"balance":"€5.00","has_credits":true,"unlimited":false}}`,
		"too many fractional digits": `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}},
		                          "credits":{"balance":"$5.001","has_credits":true,"unlimited":false}}`,
		"not a number": `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}},
		                          "credits":{"balance":"$abc","has_credits":true,"unlimited":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			snap, err := decode([]byte(doc), "", observedAt)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			for _, m := range snap.Meters {
				if m.ID() == "openai:credits" {
					t.Errorf("an unparseable balance must produce no meter, got %+v", m)
				}
			}
		})
	}
}

func TestDecodeUsesResetAfterSecondsWhenResetAtIsAbsent(t *testing.T) {
	const relative = `{"rate_limit":{"primary_window":{"used_percent":5,"reset_after_seconds":3600}}}`
	snap, err := decode([]byte(relative), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := observedAt.Add(time.Hour)
	if got := snap.Meters[0].ResetsAt(); !got.Equal(want) {
		t.Errorf("ResetsAt() = %v, want %v", got, want)
	}
}

// reset_after_seconds is an unvalidated JSON integer. Multiplied into a
// time.Duration it overflows into a negative value, and a negative offset lands
// the reset instant in the past — which the menu renders as "resets in moments"
// on a window that has barely been touched. No countdown says nothing; a
// confidently wrong one misinforms.
func TestDecodeRejectsAnOutOfRangeRelativeReset(t *testing.T) {
	for name, seconds := range map[string]string{
		"overflows time.Duration": "9223372036854775807",
		"past the horizon":        "31536000",
		"negative":                "-3600",
	} {
		t.Run(name, func(t *testing.T) {
			doc := `{"rate_limit":{"primary_window":{"used_percent":5,"reset_after_seconds":` + seconds + `}}}`
			snap, err := decode([]byte(doc), "", observedAt)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := snap.Meters[0].ResetsAt(); !got.IsZero() {
				t.Errorf("ResetsAt() = %v, want the zero time (no countdown at all)", got)
			}
		})
	}
}

func TestDecodeAcceptsARelativeResetAtTheHorizon(t *testing.T) {
	doc := `{"rate_limit":{"primary_window":{"used_percent":5,"reset_after_seconds":2678400}}}`
	snap, err := decode([]byte(doc), "", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := observedAt.Add(maxRelativeReset)
	if got := snap.Meters[0].ResetsAt(); !got.Equal(want) {
		t.Errorf("ResetsAt() = %v, want %v", got, want)
	}
}

func TestDecodeRejectsUnrecognizedDocument(t *testing.T) {
	for name, doc := range map[string]string{
		"empty object":               `{}`,
		"rate limit with no windows": `{"rate_limit":{}}`,
		"credits but no other data":  `{"credits":{"balance":"$1.00","has_credits":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decode([]byte(doc), "", observedAt); err == nil {
				t.Fatal("want error signalling a schema change")
			}
		})
	}
}

func TestDecodeIgnoresUnknownFields(t *testing.T) {
	const doc = `{"rate_limit":{"primary_window":{"used_percent":5,"reset_at":1785022579}},
	              "future_field": {"x": 1}, "another_future_thing": null}`
	if _, err := decode([]byte(doc), "", observedAt); err != nil {
		t.Fatalf("unknown fields must not break decoding: %v", err)
	}
}

func TestParseDollarString(t *testing.T) {
	cases := map[string]struct {
		want quota.Money
		ok   bool
	}{
		"$0.00":  {quota.Money{AmountMinor: 0, Currency: "USD", Exponent: 2}, true},
		"$12.34": {quota.Money{AmountMinor: 1234, Currency: "USD", Exponent: 2}, true},
		"$5":     {quota.Money{AmountMinor: 500, Currency: "USD", Exponent: 2}, true},
		"$5.1":   {quota.Money{AmountMinor: 510, Currency: "USD", Exponent: 2}, true},
		"-$3.50": {quota.Money{AmountMinor: -350, Currency: "USD", Exponent: 2}, true},
		"€5.00":  {quota.Money{}, false},
		"$5.001": {quota.Money{}, false},
		"$abc":   {quota.Money{}, false},
		"":       {quota.Money{}, false},
		"$":      {quota.Money{}, false},
		"5.00":   {quota.Money{}, false},

		// strconv accepts a sign and an underscore separator on its own. Left to
		// it, this function would silently read "$+1.00" as a dollar and "$1.-1"
		// as ninety-nine cents — values the endpoint never wrote, reported as if
		// it had. Its whole contract is that it recognizes the one shape it
		// documents and reports everything else.
		"$+1.00": {quota.Money{}, false},
		"$1.-1":  {quota.Money{}, false},
		"$-1.00": {quota.Money{}, false},
		"$1_0":   {quota.Money{}, false},
		"$ 1.00": {quota.Money{}, false},
		"$1.":    {quota.Money{}, false},
		"$.50":   {quota.Money{}, false},
		"$1.2e3": {quota.Money{}, false},
		// Non-ASCII decimal digits parse as digits under unicode.IsDigit and not
		// under strconv, which is why the validation is an explicit ASCII range.
		"$١٢.٣٤": {quota.Money{}, false},

		// The balance is an unvalidated string from an undocumented endpoint. A
		// value too large for exact minor units is reported, never wrapped into a
		// plausible-looking negative balance.
		"$92233720368547758.08":   {quota.Money{}, false},
		"$999999999999999999.99":  {quota.Money{}, false},
		"-$999999999999999999.99": {quota.Money{}, false},
		// The largest amount that is still exact.
		"$92233720368547758.07": {quota.Money{AmountMinor: 9223372036854775807, Currency: "USD", Exponent: 2}, true},
	}
	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			got, ok := parseDollarString(input)
			if ok != tc.ok {
				t.Fatalf("parseDollarString(%q) ok = %v, want %v", input, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("parseDollarString(%q) = %+v, want %+v", input, got, tc.want)
			}
		})
	}
}
