package montage

import (
	"image"
	"image/color"
	"testing"
)

var white = color.RGBA{0xff, 0xff, 0xff, 0xff}

func renderChar(r rune) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, glyphW, glyphH))
	drawString(img, 0, 0, string(r), white, img.Bounds())
	return img
}

func TestDrawGlyphPixels(t *testing.T) {
	img := renderChar('8')
	// '8' row0 is ".###." -> (1,0) on, (0,0) off.
	if got := img.RGBAAt(1, 0); got != white {
		t.Errorf("expected white at (1,0), got %v", got)
	}
	if got := img.RGBAAt(0, 0); got == white {
		t.Errorf("expected off at (0,0), got white")
	}
}

func TestDrawStringClips(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, glyphW, glyphH))
	clip := image.Rect(0, 0, 3, glyphH) // only columns 0..2 allowed
	drawString(img, 0, 0, "8", white, clip)
	// '8' row1 "#...#" would set column 4; it must be clipped away.
	if got := img.RGBAAt(4, 1); got == white {
		t.Errorf("pixel at (4,1) drawn outside clip rect")
	}
	// Column 0 of that row is inside the clip and should be set.
	if got := img.RGBAAt(0, 1); got != white {
		t.Errorf("pixel at (0,1) inside clip not drawn, got %v", got)
	}
}

func TestLowercaseFallsBackToUpper(t *testing.T) {
	a := renderChar('a')
	up := renderChar('A')
	for y := 0; y < glyphH; y++ {
		for x := 0; x < glyphW; x++ {
			if a.RGBAAt(x, y) != up.RGBAAt(x, y) {
				t.Fatalf("'a' and 'A' differ at (%d,%d)", x, y)
			}
		}
	}
}

func TestLowercaseSIsDistinct(t *testing.T) {
	// 's' has a dedicated lowercase glyph, so it must differ from 'S'.
	lo := renderChar('s')
	up := renderChar('S')
	same := true
	for y := 0; y < glyphH && same; y++ {
		for x := 0; x < glyphW; x++ {
			if lo.RGBAAt(x, y) != up.RGBAAt(x, y) {
				same = false
				break
			}
		}
	}
	if same {
		t.Error("lowercase 's' renders identically to 'S'; expected a distinct glyph")
	}
}

func TestUnknownGlyphBlank(t *testing.T) {
	img := renderChar('?') // not in the table -> blank
	for y := 0; y < glyphH; y++ {
		for x := 0; x < glyphW; x++ {
			if img.RGBAAt(x, y) == white {
				t.Fatalf("unknown glyph drew a pixel at (%d,%d)", x, y)
			}
		}
	}
}

func TestKnownGlyphsCovered(t *testing.T) {
	must := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ:.s- "
	for _, r := range must {
		if _, ok := lookupGlyph(r); !ok {
			t.Errorf("glyph %q missing from font table", r)
		}
	}
}
