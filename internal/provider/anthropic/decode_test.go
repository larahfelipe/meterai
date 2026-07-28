package anthropic

import (
	"testing"
	"time"

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

	if snap.Product != "Claude" {
		t.Errorf("Product = %q, want the product name the subscription is sold as", snap.Product)
	}

	// The active window must win the tray summary even though its percentage
	// is compared against an inactive one.
	primary := snap.Primary()
	if primary.ID() != "anthropic:weekly_all" {
		t.Errorf("Primary = %q, want anthropic:weekly_all", primary.ID())
	}
	// The provider reports the vendor's own kind; translation happens above it.
	if primary.Label() != "weekly_all" {
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

func TestDecodeSurfacesAnUnknownWindowUnderItsRawKind(t *testing.T) {
	const unknownKind = `{"limits":[{"kind":"weekly_future_thing","percent":11,"severity":"normal"}]}`
	snap, err := decode([]byte(unknownKind), "pro", observedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A window introduced after this release must reach the UI, which resolves
	// its display name from the MeterID and falls back to this one.
	if got := snap.Meters[0].Label(); got != "weekly_future_thing" {
		t.Errorf("Label = %q, want the raw kind returned unchanged", got)
	}
	if got := snap.Meters[0].ID(); got != "anthropic:weekly_future_thing" {
		t.Errorf("MeterID = %q", got)
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
