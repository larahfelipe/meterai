package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"testing"

	"meterAI/internal/quota"
)

// decodeICO parses back the container this package writes, so the tests assert
// on pixels rather than on the encoder's own arithmetic.
func decodeICO(t *testing.T, raw []byte) *image.NRGBA {
	t.Helper()
	if len(raw) < icoDirSize+icoDirEntrySize+bitmapInfoHeaderSize {
		t.Fatalf("ICO is %d bytes, too short to contain a header", len(raw))
	}
	r := bytes.NewReader(raw)
	var reserved, kind, count uint16
	mustRead(t, r, &reserved, &kind, &count)
	if reserved != 0 || kind != icoTypeIcon || count != 1 {
		t.Fatalf("ICONDIR = {%d,%d,%d}, want {0,1,1}", reserved, kind, count)
	}

	var width, height, colors, pad uint8
	var planes, bits uint16
	var byteCount, offset uint32
	mustRead(t, r, &width, &height, &colors, &pad, &planes, &bits, &byteCount, &offset)
	if width != iconEdge || height != iconEdge {
		t.Fatalf("entry dimensions = %dx%d, want %dx%d", width, height, iconEdge, iconEdge)
	}
	if bits != bitsPerPixel {
		t.Fatalf("bit depth = %d, want %d", bits, bitsPerPixel)
	}
	if int(offset)+int(byteCount) != len(raw) {
		t.Fatalf("entry claims bytes [%d,%d) but the file is %d bytes", offset, uint32(len(raw))-byteCount, len(raw))
	}

	var headerSize uint32
	var bmWidth, bmHeight int32
	mustRead(t, r, &headerSize, &bmWidth, &bmHeight)
	if headerSize != bitmapInfoHeaderSize {
		t.Fatalf("BITMAPINFOHEADER size = %d", headerSize)
	}
	// The DIB height covers the XOR mask stacked on the AND mask.
	if bmHeight != iconEdge*2 {
		t.Fatalf("biHeight = %d, want %d", bmHeight, iconEdge*2)
	}

	pixels := raw[int(offset)+bitmapInfoHeaderSize:]
	img := image.NewNRGBA(image.Rect(0, 0, iconEdge, iconEdge))
	i := 0
	for y := iconEdge - 1; y >= 0; y-- {
		for x := 0; x < iconEdge; x++ {
			img.SetNRGBA(x, y, color.NRGBA{B: pixels[i], G: pixels[i+1], R: pixels[i+2], A: pixels[i+3]})
			i += 4
		}
	}
	return img
}

func mustRead(t *testing.T, r *bytes.Reader, values ...any) {
	t.Helper()
	for _, v := range values {
		if err := binary.Read(r, binary.LittleEndian, v); err != nil {
			t.Fatalf("truncated ICO: %v", err)
		}
	}
}

// fillRows counts the rows in the gauge interior painted with the fill colour.
func fillRows(t *testing.T, img *image.NRGBA) int {
	t.Helper()
	centreX := iconEdge / 2
	rows := 0
	for y := margin + borderWidth; y < iconEdge-margin-borderWidth; y++ {
		c := img.NRGBAAt(centreX, y)
		if c != colorTrack && c.A == 0xFF {
			rows++
		}
	}
	return rows
}

func TestRenderProducesAWellFormedIcon(t *testing.T) {
	img := decodeICO(t, Render(50, quota.SeverityNormal, false))

	// Corners stay transparent so the icon does not read as a filled square.
	if got := img.NRGBAAt(0, 0); got.A != 0 {
		t.Errorf("corner pixel = %+v, want transparent", got)
	}
	// The border must be opaque on all four sides.
	for _, p := range []image.Point{
		{X: iconEdge / 2, Y: margin},
		{X: iconEdge / 2, Y: iconEdge - margin - 1},
		{X: margin, Y: iconEdge / 2},
		{X: iconEdge - margin - 1, Y: iconEdge / 2},
	} {
		if got := img.NRGBAAt(p.X, p.Y); got != colorBorder {
			t.Errorf("border at %v = %+v, want %+v", p, got, colorBorder)
		}
	}
}

func TestRenderFillsFromTheBottom(t *testing.T) {
	img := decodeICO(t, Render(50, quota.SeverityNormal, false))
	interiorTop := margin + borderWidth
	interiorBottom := iconEdge - margin - borderWidth - 1
	centreX := iconEdge / 2

	if got := img.NRGBAAt(centreX, interiorBottom); got != colorNormal {
		t.Errorf("bottom of the gauge = %+v, want the fill colour", got)
	}
	if got := img.NRGBAAt(centreX, interiorTop); got != colorTrack {
		t.Errorf("top of a half-full gauge = %+v, want the empty track", got)
	}
}

func TestRenderFillHeightTracksPercent(t *testing.T) {
	interiorHeight := iconEdge - 2*(margin+borderWidth)
	cases := []struct {
		percent float64
		want    int
	}{
		{0, 0},
		{0.4, 1}, // any usage at all must be visible
		{25, 6},  // round(0.25*24)
		{50, 12},
		{100, interiorHeight},
		{140, interiorHeight}, // overage clamps rather than overflowing
	}
	for _, tc := range cases {
		img := decodeICO(t, Render(tc.percent, quota.SeverityNormal, false))
		if got := fillRows(t, img); got != tc.want {
			t.Errorf("Render(%v%%) filled %d rows, want %d", tc.percent, got, tc.want)
		}
	}
}

func TestRenderColoursBySeverity(t *testing.T) {
	cases := map[string]struct {
		level quota.Severity
		stale bool
		want  color.NRGBA
	}{
		"normal":            {quota.SeverityNormal, false, colorNormal},
		"warning":           {quota.SeverityWarning, false, colorWarning},
		"critical":          {quota.SeverityCritical, false, colorCritical},
		"stale overrides":   {quota.SeverityCritical, true, colorStale},
		"unknown is normal": {quota.Severity(0), false, colorNormal},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			img := decodeICO(t, Render(80, tc.level, tc.stale))
			got := img.NRGBAAt(iconEdge/2, iconEdge-margin-borderWidth-1)
			if got != tc.want {
				t.Errorf("fill = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	first := Render(73, quota.SeverityWarning, false)
	second := Render(73, quota.SeverityWarning, false)
	if !bytes.Equal(first, second) {
		t.Fatal("identical state produced different bytes; the tray would redraw on every poll")
	}
	if bytes.Equal(first, Render(80, quota.SeverityWarning, false)) {
		t.Error("a percentage change large enough to move a row must change the icon")
	}
}

// TestRenderQuantizesToRows pins the gauge's resolution. The interior is 24
// rows tall, so it cannot resolve better than ~4.2 percentage points, and 73%
// and 74% are deliberately the same picture. This is why the exact figures
// belong in the tooltip, and it is also what makes the "only redraw when the
// icon actually changed" optimization worthwhile.
func TestRenderQuantizesToRows(t *testing.T) {
	if !bytes.Equal(
		Render(73, quota.SeverityWarning, false),
		Render(74, quota.SeverityWarning, false),
	) {
		t.Error("expected 73% and 74% to render identically at 24 rows of resolution")
	}
}

func TestRenderHandlesDegenerateInput(t *testing.T) {
	for _, percent := range []float64{-10, 0, 1e9} {
		if len(Render(percent, quota.SeverityNormal, false)) == 0 {
			t.Errorf("Render(%v) produced no icon", percent)
		}
	}
	// NaN must not panic or produce a partial fill.
	nan := Render(math.NaN(), quota.SeverityNormal, false)
	if got := fillRows(t, decodeICO(t, nan)); got != 0 {
		t.Errorf("NaN filled %d rows, want 0", got)
	}
}
