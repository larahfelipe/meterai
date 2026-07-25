// Package trayicon renders the Windows notification-area icon. Windows accepts
// only the ICO container there, and the icon is redrawn on every state change,
// so the encoder is written directly rather than pulling in an image codec.
//
// The design constraint that drives every choice here is that Windows displays
// this at 16x16 device pixels at 100% DPI. Anything thinner than two pixels
// disappears, and glyphs are unreadable, so the meter is a solid bottom-up fill
// with a high-contrast border: the exact figures live in the tooltip.
package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/larahfelipe/meterai/internal/quota"
)

const (
	// iconEdge is the rendered size in pixels. 32 is the largest size Windows
	// requests for the notification area at high DPI; it downsamples cleanly
	// to 16 because every element is an even number of pixels.
	iconEdge = 32
	// margin keeps the gauge clear of the icon bounds so adjacent tray icons
	// stay visually separated.
	margin = 2
	// borderWidth is the dark outline that keeps the gauge legible on a light
	// taskbar. Two pixels survives the 32->16 downsample; one does not.
	borderWidth = 2
)

// Palette entries follow the severity the vendor reported rather than local
// thresholds, so the icon never contradicts the vendor's own warning state.
var (
	colorNormal      = color.NRGBA{R: 0x3F, G: 0xB9, B: 0x50, A: 0xFF}
	colorWarning     = color.NRGBA{R: 0xD2, G: 0x99, B: 0x22, A: 0xFF}
	colorCritical    = color.NRGBA{R: 0xF8, G: 0x51, B: 0x49, A: 0xFF}
	colorStale       = color.NRGBA{R: 0x8B, G: 0x94, B: 0x9E, A: 0xFF}
	colorTrack       = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x40}
	colorBorder      = color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x99}
	colorTransparent = color.NRGBA{}
)

// Render encodes a Windows ICO showing percent of an allowance consumed.
// level selects the fill colour; stale overrides it with a neutral grey to
// signal that the figure is no longer being confirmed. percent is clamped to
// [0,100] for display: a vendor reporting overage still renders as full.
//
// The output is a pure function of its inputs, so identical state produces
// byte-identical icons and the tray is only updated when something changed.
func Render(percent float64, level quota.Severity, stale bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconEdge, iconEdge))
	draw.Draw(img, img.Bounds(), &image.Uniform{colorTransparent}, image.Point{}, draw.Src)

	gauge := image.Rect(margin, margin, iconEdge-margin, iconEdge-margin)
	draw.Draw(img, gauge, &image.Uniform{colorBorder}, image.Point{}, draw.Src)

	interior := image.Rect(
		gauge.Min.X+borderWidth, gauge.Min.Y+borderWidth,
		gauge.Max.X-borderWidth, gauge.Max.Y-borderWidth,
	)
	draw.Draw(img, interior, &image.Uniform{colorTrack}, image.Point{}, draw.Src)

	filled := fillHeight(percent, interior.Dy())
	if filled > 0 {
		// Fill upward from the bottom edge, the direction users read as
		// "how full is it".
		fill := image.Rect(interior.Min.X, interior.Max.Y-filled, interior.Max.X, interior.Max.Y)
		draw.Draw(img, fill, &image.Uniform{fillColor(level, stale)}, image.Point{}, draw.Src)
	}
	return encodeICO(img)
}

// fillHeight converts a percentage into pixel rows. Any non-zero usage claims
// at least one row, so "barely started" is visually distinct from "not started".
func fillHeight(percent float64, available int) int {
	if math.IsNaN(percent) || percent <= 0 {
		return 0
	}
	if percent >= 100 {
		return available
	}
	rows := int(math.Round(percent / 100 * float64(available)))
	if rows == 0 {
		return 1
	}
	return rows
}

func fillColor(level quota.Severity, stale bool) color.NRGBA {
	if stale {
		return colorStale
	}
	switch level {
	case quota.SeverityCritical:
		return colorCritical
	case quota.SeverityWarning:
		return colorWarning
	default:
		return colorNormal
	}
}

// ICO container constants, per the Windows ICONDIR/ICONDIRENTRY/BITMAPINFOHEADER
// layout. The image is stored as a bottom-up 32-bit BGRA DIB.
const (
	icoTypeIcon          = 1
	icoDirSize           = 6
	icoDirEntrySize      = 16
	bitmapInfoHeaderSize = 40
	bitsPerPixel         = 32
	bytesPerPixel        = bitsPerPixel / 8
	// andMaskRowBytes is the 1-bit-per-pixel transparency mask row, padded to a
	// 4-byte boundary as the format requires. It is written all-zero because
	// the 32-bit DIB carries its own alpha channel, but Windows still expects
	// the mask to be present and correctly sized.
	andMaskRowBytes = ((iconEdge + 31) / 32) * 4
)

func encodeICO(img *image.NRGBA) []byte {
	xorSize := iconEdge * iconEdge * bytesPerPixel
	andSize := andMaskRowBytes * iconEdge
	imageSize := bitmapInfoHeaderSize + xorSize + andSize

	buf := bytes.NewBuffer(make([]byte, 0, icoDirSize+icoDirEntrySize+imageSize))
	write := func(values ...any) {
		for _, v := range values {
			// Errors from a bytes.Buffer are impossible: it grows or panics on
			// allocation failure, which is an unrecoverable condition.
			_ = binary.Write(buf, binary.LittleEndian, v)
		}
	}

	// ICONDIR
	write(uint16(0), uint16(icoTypeIcon), uint16(1))
	// ICONDIRENTRY
	write(uint8(iconEdge), uint8(iconEdge), uint8(0), uint8(0),
		uint16(1), uint16(bitsPerPixel),
		uint32(imageSize), uint32(icoDirSize+icoDirEntrySize))
	// BITMAPINFOHEADER: biHeight is doubled to cover the XOR and AND masks.
	write(uint32(bitmapInfoHeaderSize), int32(iconEdge), int32(iconEdge*2),
		uint16(1), uint16(bitsPerPixel), uint32(0), uint32(xorSize),
		int32(0), int32(0), uint32(0), uint32(0))

	// XOR mask: bottom-up rows of BGRA.
	for y := iconEdge - 1; y >= 0; y-- {
		for x := 0; x < iconEdge; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	buf.Write(make([]byte, andSize))
	return buf.Bytes()
}
