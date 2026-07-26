package tray

import (
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// meterBarCells is the fixed width of a textual gauge. Ten cells put one cell
	// at ten percentage points, the granularity a menu row is actually read at,
	// and keep the widest row — label, gauge, figures — inside the width a shell
	// menu shows without eliding.
	meterBarCells = 10

	// barFilledCell and barEmptyCell are Block Elements. Both are full-width in
	// the menu font, so a gauge occupies the same space whatever its value; a
	// pair of glyphs with different advance widths would misalign the figures
	// that follow it, since Windows menus use a proportional font.
	barFilledCell = '█'
	barEmptyCell  = '░'
)

// progressBar renders percent as a fixed-width gauge.
//
// It quantizes by the same rule as trayicon.fillHeight — any non-zero usage
// claims one cell, and the value is clamped at 100 — so the icon and the menu can
// never disagree about how full the same meter is. The clamp is presentation
// only: quota.Percent keeps a vendor's overage figure intact.
func progressBar(percent float64) string {
	filled := meterBarCells
	switch {
	case math.IsNaN(percent) || percent <= 0:
		filled = 0
	case percent < 100:
		filled = int(math.Round(percent / 100 * float64(meterBarCells)))
		if filled == 0 {
			// Barely started has to look different from not started at all.
			filled = 1
		}
	}

	var bar strings.Builder
	bar.Grow(meterBarCells * utf8.RuneLen(barFilledCell))
	for cell := 0; cell < meterBarCells; cell++ {
		if cell < filled {
			bar.WriteRune(barFilledCell)
			continue
		}
		bar.WriteRune(barEmptyCell)
	}
	return bar.String()
}
