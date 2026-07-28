package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/larahfelipe/meterai/internal/quota"
)

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

// money is the amount shape the spend block repeats for its used and limit
// figures. Minor units throughout: no currency figure passes through float64.
type money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Exponent    uint8  `json:"exponent"`
}

type spendBlock struct {
	Used     money    `json:"used"`
	Limit    *money   `json:"limit"`
	Percent  *float64 `json:"percent"`
	Severity string   `json:"severity"`
	Enabled  bool     `json:"enabled"`
}

const (
	legacyFiveHourKind = "session"
	legacySevenDayKind = "weekly_all"
	spendMeterKind     = "spend"
)

// decode is this vendor's usageapi.Decoder. The plan comes from the credential
// file rather than from the response: this endpoint reports windows and spend,
// and names the subscription nowhere.
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
		Used:    amount(s.Used),
		Level:   parseSeverity(s.Severity),
	}
	if s.Percent != nil {
		balance.Percent = *s.Percent
	}
	if s.Limit != nil {
		limit := amount(*s.Limit)
		balance.Limit = &limit
	}
	return balance
}

func amount(m money) quota.Money {
	return quota.Money{AmountMinor: m.AmountMinor, Currency: m.Currency, Exponent: m.Exponent}
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
