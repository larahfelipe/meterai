package quota

import (
	"math"
	"strings"
	"testing"
)

func TestSeverityForCombinesVendorAndLocalThresholds(t *testing.T) {
	thresholds := DefaultThresholds() // warn 75, critical 90
	cases := []struct {
		percent float64
		vendor  Severity
		want    Severity
	}{
		{10, SeverityNormal, SeverityNormal},
		{74, SeverityNormal, SeverityNormal},
		// The case that motivates local thresholds: the vendor called 75%
		// normal, the user wants a warning.
		{75, SeverityNormal, SeverityWarning},
		{89.9, SeverityNormal, SeverityWarning},
		{90, SeverityNormal, SeverityCritical},
		// Percent is not clamped in the model, so a vendor's overage figure has
		// to escalate rather than wrap back to normal.
		{140, SeverityNormal, SeverityCritical},
		// The vendor is never overruled downward.
		{5, SeverityCritical, SeverityCritical},
		{80, SeverityCritical, SeverityCritical},
		{5, SeverityWarning, SeverityWarning},
		// Nor upward past its own reading when the local policy is stricter.
		{95, SeverityWarning, SeverityCritical},
	}
	for _, tc := range cases {
		if got := thresholds.SeverityFor(tc.percent, tc.vendor); got != tc.want {
			t.Errorf("SeverityFor(%v, %v) = %v, want %v", tc.percent, tc.vendor, got, tc.want)
		}
	}
}

// A configured pair has to move the boundaries, not just the defaults: this is
// the whole point of making them settable.
func TestSeverityForHonoursAConfiguredPair(t *testing.T) {
	thresholds := Thresholds{WarnAtPercent: 50, CriticalAtPercent: 60}
	for _, tc := range []struct {
		percent float64
		want    Severity
	}{
		{49.9, SeverityNormal},
		{50, SeverityWarning},
		{59.9, SeverityWarning},
		{60, SeverityCritical},
		// Well past what the defaults would have called normal, and still normal
		// only because the user moved the boundary; the inverse case proves the
		// figure is read against the setting rather than against 75/90.
		{74, SeverityCritical},
	} {
		if got := thresholds.SeverityFor(tc.percent, SeverityNormal); got != tc.want {
			t.Errorf("SeverityFor(%v) with %+v = %v, want %v", tc.percent, thresholds, got, tc.want)
		}
	}
}

// A meter that reported nothing at all must not read as an alert, and neither
// must one whose percentage could not be computed.
func TestSeverityForTreatsUnusableReadingsAsNormal(t *testing.T) {
	thresholds := DefaultThresholds()
	for _, percent := range []float64{0, -1, math.NaN()} {
		if got := thresholds.SeverityFor(percent, SeverityNormal); got != SeverityNormal {
			t.Errorf("SeverityFor(%v) = %v, want normal", percent, got)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		thresholds Thresholds
		wantErr    string
	}{
		"warn at zero":               {Thresholds{0, 90}, "warnAtPercent"},
		"warn below zero":            {Thresholds{-5, 90}, "warnAtPercent"},
		"warn above the ceiling":     {Thresholds{101, 90}, "warnAtPercent"},
		"critical at zero":           {Thresholds{75, 0}, "criticalAtPercent"},
		"critical above the ceiling": {Thresholds{75, 150}, "criticalAtPercent"},
		"critical below warn":        {Thresholds{90, 60}, "unreachable"},
		// Equal thresholds are the boundary case: SeverityFor tests critical
		// first, so the warning level would exist in the settings and never once
		// be displayed.
		"critical equal to warn": {Thresholds{80, 80}, "unreachable"},
		"the zero value":         {Thresholds{}, "warnAtPercent"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.thresholds.Validate()
			if err == nil {
				t.Fatalf("%+v must not validate", tc.thresholds)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAcceptsTheBoundsItDocuments(t *testing.T) {
	for _, thresholds := range []Thresholds{
		DefaultThresholds(),
		// One point apart at the bottom of the range, and the ceiling itself:
		// both ends of what the document may legitimately carry.
		{WarnAtPercent: 1, CriticalAtPercent: 2},
		{WarnAtPercent: 99, CriticalAtPercent: percentCeiling},
		{WarnAtPercent: 0.5, CriticalAtPercent: 99.5},
	} {
		if err := thresholds.Validate(); err != nil {
			t.Errorf("%+v must validate: %v", thresholds, err)
		}
	}
}

// The default pair is what every fresh install runs on, so it has to satisfy
// the rule the app enforces on every other pair.
func TestDefaultThresholdsValidate(t *testing.T) {
	if err := DefaultThresholds().Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if got := DefaultThresholds(); got.WarnAtPercent != 75 || got.CriticalAtPercent != 90 {
		t.Errorf("DefaultThresholds() = %+v, want 75/90", got)
	}
}
