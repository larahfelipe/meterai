package quota

import "fmt"

const (
	// defaultWarnPercent and defaultCriticalPercent are the thresholds applied
	// when the settings document names none. They exist because vendors report
	// "normal" well past the point a user wants warning: the observed Anthropic
	// response called 74% of the weekly allowance normal.
	defaultWarnPercent     = 75.0
	defaultCriticalPercent = 90.0

	// percentCeiling bounds threshold configuration. A threshold above 100 could
	// never be reached by a meter that stops at its allowance, which is a silent
	// misconfiguration rather than a stricter setting.
	percentCeiling = 100.0
)

// Thresholds is the local escalation policy: the utilization at which a meter
// starts reading as a warning, and the one at which it reads as critical.
//
// It lives beside Severity rather than in the settings package because it is
// what Severity means locally — anything holding a percentage can classify it
// without learning where the numbers were persisted. The JSON tags are the
// persisted shape; they are stable across releases.
type Thresholds struct {
	WarnAtPercent     float64 `json:"warnAtPercent"`
	CriticalAtPercent float64 `json:"criticalAtPercent"`
}

// DefaultThresholds is the policy used when no document supplies one.
func DefaultThresholds() Thresholds {
	return Thresholds{WarnAtPercent: defaultWarnPercent, CriticalAtPercent: defaultCriticalPercent}
}

// SeverityFor combines the vendor's own assessment with the local thresholds,
// taking whichever is more severe. The vendor is never overruled downward: if
// Anthropic says critical, so does the icon, regardless of local settings.
//
// The comparisons are inclusive, so a threshold names the first figure that
// carries its severity rather than the last one below it.
func (t Thresholds) SeverityFor(percent float64, vendor Severity) Severity {
	local := SeverityNormal
	switch {
	case percent >= t.CriticalAtPercent:
		local = SeverityCritical
	case percent >= t.WarnAtPercent:
		local = SeverityWarning
	}
	if vendor > local {
		return vendor
	}
	return local
}

// Validate reports why a policy cannot be used. Every failure names the field
// as the settings document spells it, because a user who edits that document by
// hand is the only one who can produce one of these.
func (t Thresholds) Validate() error {
	if t.WarnAtPercent <= 0 || t.WarnAtPercent > percentCeiling {
		return fmt.Errorf("warnAtPercent is %v; it must be in (0,%v]", t.WarnAtPercent, percentCeiling)
	}
	if t.CriticalAtPercent <= 0 || t.CriticalAtPercent > percentCeiling {
		return fmt.Errorf("criticalAtPercent is %v; it must be in (0,%v]", t.CriticalAtPercent, percentCeiling)
	}
	// Equal thresholds are rejected along with inverted ones: SeverityFor tests
	// critical first, so a warning that starts where critical does is a level
	// the app can never display and a setting that silently does nothing.
	if t.CriticalAtPercent <= t.WarnAtPercent {
		return fmt.Errorf("criticalAtPercent (%v) is not above warnAtPercent (%v); the warning level would be unreachable",
			t.CriticalAtPercent, t.WarnAtPercent)
	}
	return nil
}
