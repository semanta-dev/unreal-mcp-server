package montage

// This file holds a tiny, dependency-free 5x7 monospaced bitmap font used to
// stamp per-cell labels (timestamps, frame indices) onto the montage. Glyphs
// are authored as human-readable pixel art ('#' = on) and compiled to packed
// bit rows at init, so the source stays legible while the runtime form is a
// compact array of bit rows. No external font files or imports are needed.

import (
	"image"
	"image/color"
	"image/draw"
)

const (
	glyphW   = 5          // glyph cell width in pixels
	glyphH   = 7          // glyph cell height in pixels
	glyphAdv = glyphW + 1 // horizontal advance (1px inter-glyph spacing)
)

// glyphSource is the readable pixel art. Each glyph is up to glyphH rows of up
// to glyphW characters; '#' means the pixel is on, anything else is off. Rows
// shorter than glyphW (or fewer than glyphH) are padded with off pixels, so a
// blank string yields a blank glyph (used for space).
var glyphSource = map[rune][]string{
	'0': {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2': {".###.", "#...#", "....#", "..##.", ".#...", "#....", "#####"},
	'3': {"#####", "....#", "...#.", "..##.", "....#", "#...#", ".###."},
	'4': {"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	'5': {"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	'6': {"..##.", ".#...", "#....", "####.", "#...#", "#...#", ".###."},
	'7': {"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	'8': {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9': {".###.", "#...#", "#...#", ".####", "....#", "...#.", ".##.."},

	'A': {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'B': {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'C': {".###.", "#...#", "#....", "#....", "#....", "#...#", ".###."},
	'D': {"###..", "#..#.", "#...#", "#...#", "#...#", "#..#.", "###.."},
	'E': {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'F': {"#####", "#....", "#....", "####.", "#....", "#....", "#...."},
	'G': {".###.", "#...#", "#....", "#.###", "#...#", "#...#", ".###."},
	'H': {"#...#", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'I': {".###.", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'J': {"..###", "...#.", "...#.", "...#.", "...#.", "#..#.", ".##.."},
	'K': {"#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"},
	'L': {"#....", "#....", "#....", "#....", "#....", "#....", "#####"},
	'M': {"#...#", "##.##", "#.#.#", "#.#.#", "#...#", "#...#", "#...#"},
	'N': {"#...#", "##..#", "#.#.#", "#..##", "#...#", "#...#", "#...#"},
	'O': {".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'P': {"####.", "#...#", "#...#", "####.", "#....", "#....", "#...."},
	'Q': {".###.", "#...#", "#...#", "#...#", "#.#.#", "#..#.", ".##.#"},
	'R': {"####.", "#...#", "#...#", "####.", "#.#..", "#..#.", "#...#"},
	'S': {".###.", "#...#", "#....", ".###.", "....#", "#...#", ".###."},
	'T': {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."},
	'U': {"#...#", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'V': {"#...#", "#...#", "#...#", "#...#", "#...#", ".#.#.", "..#.."},
	'W': {"#...#", "#...#", "#...#", "#.#.#", "#.#.#", "##.##", "#...#"},
	'X': {"#...#", "#...#", ".#.#.", "..#..", ".#.#.", "#...#", "#...#"},
	'Y': {"#...#", "#...#", ".#.#.", "..#..", "..#..", "..#..", "..#.."},
	'Z': {"#####", "....#", "...#.", "..#..", ".#...", "#....", "#####"},

	// Lowercase 's' is kept distinct (for durations like "1.23s"). Other
	// lowercase letters fall back to their uppercase glyph in lookupGlyph.
	's': {".....", ".....", ".####", "#....", ".###.", "....#", "####."},

	':': {".....", "..#..", "..#..", ".....", "..#..", "..#..", "....."},
	'.': {".....", ".....", ".....", ".....", ".....", "..#..", "..#.."},
	'-': {".....", ".....", ".....", ".###.", ".....", ".....", "....."},
	' ': {"", "", "", "", "", "", ""},
}

// glyphBits is the compiled, runtime form: for each glyph, glyphH rows where
// bit (glyphW-1) is the leftmost column and bit 0 the rightmost.
var glyphBits = compileGlyphs(glyphSource)

func compileGlyphs(src map[rune][]string) map[rune][glyphH]uint8 {
	out := make(map[rune][glyphH]uint8, len(src))
	for r, rows := range src {
		var g [glyphH]uint8
		for y := 0; y < glyphH && y < len(rows); y++ {
			row := rows[y]
			var bits uint8
			for x := 0; x < glyphW && x < len(row); x++ {
				if row[x] == '#' {
					bits |= 1 << uint(glyphW-1-x)
				}
			}
			g[y] = bits
		}
		out[r] = g
	}
	return out
}

// lookupGlyph returns the packed rows for a rune. Lowercase letters that lack a
// dedicated glyph fall back to their uppercase form. Unknown runes return
// (_, false) so callers render nothing (a blank cell).
func lookupGlyph(r rune) ([glyphH]uint8, bool) {
	if g, ok := glyphBits[r]; ok {
		return g, true
	}
	if r >= 'a' && r <= 'z' {
		if g, ok := glyphBits[r-'a'+'A']; ok {
			return g, true
		}
	}
	return [glyphH]uint8{}, false
}

// drawGlyph stamps a single glyph with its top-left at (x,y), painting only
// pixels that fall inside clip.
func drawGlyph(dst draw.Image, x, y int, r rune, col color.Color, clip image.Rectangle) {
	g, ok := lookupGlyph(r)
	if !ok {
		return
	}
	for gy := 0; gy < glyphH; gy++ {
		bits := g[gy]
		if bits == 0 {
			continue
		}
		for gx := 0; gx < glyphW; gx++ {
			if bits&(1<<uint(glyphW-1-gx)) == 0 {
				continue
			}
			p := image.Point{X: x + gx, Y: y + gy}
			if !p.In(clip) {
				continue
			}
			dst.Set(p.X, p.Y, col)
		}
	}
}

// drawString stamps s left-to-right starting at (x,y), clipping every pixel to
// clip so a label can never bleed outside its cell. Unknown runes advance the
// cursor but draw nothing.
func drawString(dst draw.Image, x, y int, s string, col color.Color, clip image.Rectangle) {
	cx := x
	for _, r := range s {
		drawGlyph(dst, cx, y, r, col, clip)
		cx += glyphAdv
	}
}
