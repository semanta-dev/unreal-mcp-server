package visual

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writePNG creates a solid-color w x h PNG in dir and returns its path.
func writePNG(t *testing.T, dir, name string, w, h int, c color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create %s: %v", p, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode %s: %v", p, err)
	}
	return p
}

// solidCells writes n equal-sized solid-gray cells and returns their paths.
func solidCells(t *testing.T, dir string, n, w, h int) []string {
	t.Helper()
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		// distinct-ish grays so cells are individually valid PNGs
		g := uint8(40 + i*10)
		paths[i] = writePNG(t, dir, filepathName(i), w, h, color.RGBA{g, g, g, 0xff})
	}
	return paths
}

func filepathName(i int) string {
	return "cell_" + itoa(i) + ".png"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// decode decodes a MontageResult's PNG for pixel inspection.
func decode(t *testing.T, r MontageResult) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(r.PNG))
	if err != nil {
		t.Fatalf("decode result PNG: %v", err)
	}
	return img
}

func pix(img image.Image, x, y int) color.RGBA {
	return color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
}

func TestBuildDimensions(t *testing.T) {
	const (
		cellW = 10
		cellH = 8
		cols  = 3
	)
	for _, n := range []int{1, 2, 3, 4, 6} {
		for _, gutter := range []int{0, 6} {
			// effective columns shrink to n when n < cols
			effCols := cols
			if n < effCols {
				effCols = n
			}
			effRows := (n + effCols - 1) / effCols

			dir := t.TempDir()
			paths := solidCells(t, dir, n, cellW, cellH)
			res, err := Build(BuildOpts{CellPaths: paths, Cols: cols, Gutter: gutter})
			if err != nil {
				t.Fatalf("n=%d g=%d: Build: %v", n, gutter, err)
			}

			if res.Rows != effRows || res.Cols != effCols {
				t.Errorf("n=%d g=%d: got Rows=%d Cols=%d, want %d/%d", n, gutter, res.Rows, res.Cols, effRows, effCols)
			}
			if res.CellW != cellW || res.CellH != cellH {
				t.Errorf("n=%d g=%d: got CellW/H=%d/%d, want %d/%d", n, gutter, res.CellW, res.CellH, cellW, cellH)
			}

			wantW := effCols*cellW + (effCols+1)*gutter
			wantH := effRows*cellH + (effRows+1)*gutter
			img := decode(t, res)
			if img.Bounds().Dx() != wantW || img.Bounds().Dy() != wantH {
				t.Errorf("n=%d g=%d: canvas %dx%d, want %dx%d", n, gutter, img.Bounds().Dx(), img.Bounds().Dy(), wantW, wantH)
			}

			// Pixel-exact cell offsets.
			for i := 0; i < n; i++ {
				row, col := CellForFrame(i, effCols)
				x := gutter + col*(cellW+gutter)
				y := gutter + row*(cellH+gutter)
				want := image.Rect(x, y, x+cellW, y+cellH)
				if got := res.CellRect[i]; got != want {
					t.Errorf("n=%d g=%d cell=%d: rect %v, want %v", n, gutter, i, got, want)
				}
			}
			if len(res.CellRect) != n {
				t.Errorf("n=%d g=%d: CellRect has %d entries, want %d", n, gutter, len(res.CellRect), n)
			}
		}
	}
}

func TestCellForFrame(t *testing.T) {
	cases := []struct {
		i, cols      int
		wantR, wantC int
	}{
		{0, 3, 0, 0},
		{1, 3, 0, 1},
		{2, 3, 0, 2},
		{3, 3, 1, 0},
		{5, 3, 1, 2},
		{6, 3, 2, 0},
		{0, 1, 0, 0},
		{4, 1, 4, 0},
		{7, 0, 7, 0}, // cols<1 treated as 1
	}
	for _, c := range cases {
		r, col := CellForFrame(c.i, c.cols)
		if r != c.wantR || col != c.wantC {
			t.Errorf("CellForFrame(%d,%d)=(%d,%d), want (%d,%d)", c.i, c.cols, r, col, c.wantR, c.wantC)
		}
	}
}

func TestMarkCellsRedBorder(t *testing.T) {
	const (
		cellW  = 10
		cellH  = 8
		cols   = 2
		gutter = 4
	)
	dir := t.TempDir()
	paths := solidCells(t, dir, 4, cellW, cellH)
	res, err := Build(BuildOpts{CellPaths: paths, Cols: cols, Gutter: gutter, MarkCells: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, res)

	marked := res.CellRect[1]
	if got := pix(img, marked.Min.X, marked.Min.Y); got != colMark {
		t.Errorf("marked cell corner = %v, want red %v", got, colMark)
	}
	unmarked := res.CellRect[0]
	if got := pix(img, unmarked.Min.X, unmarked.Min.Y); got == colMark {
		t.Errorf("unmarked cell corner is red %v; should not be", got)
	}
	if got := pix(img, unmarked.Min.X, unmarked.Min.Y); got != colBorder {
		t.Errorf("unmarked cell corner = %v, want border %v", got, colBorder)
	}
}

func TestLabelClippedToCell(t *testing.T) {
	const (
		cellW  = 10
		cellH  = 8
		cols   = 2
		gutter = 6
	)
	dir := t.TempDir()
	paths := solidCells(t, dir, 2, cellW, cellH)
	// A long label that would overflow the cell if not clipped.
	res, err := Build(BuildOpts{
		CellPaths: paths, Cols: cols, Gutter: gutter,
		LabelForCell: func(i int) string {
			if i == 0 {
				return "88888888"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, res)

	cell0 := res.CellRect[0]
	// Label draws white inside the cell: glyph '8' row0 (".###.") at x+2 sets
	// the pixel one column in.
	if got := pix(img, cell0.Min.X+3, cell0.Min.Y+2); got != colLabel {
		t.Errorf("expected label pixel white at (%d,%d), got %v", cell0.Min.X+3, cell0.Min.Y+2, got)
	}
	// Just past the cell's right edge (in the gutter) must stay background:
	// without clipping the long label would paint here.
	ox, oy := cell0.Max.X+2, cell0.Min.Y+3
	if got := pix(img, ox, oy); got != colBackground {
		t.Errorf("expected background outside cell at (%d,%d), got %v", ox, oy, got)
	}
}

func TestNoLabelWhenNil(t *testing.T) {
	dir := t.TempDir()
	paths := solidCells(t, dir, 2, 10, 8)
	if _, err := Build(BuildOpts{CellPaths: paths, Cols: 2}); err != nil {
		t.Fatalf("Build with nil LabelForCell: %v", err)
	}
}

func TestSmallerCellLetterboxed(t *testing.T) {
	const (
		cellW = 10
		cellH = 8
	)
	dir := t.TempDir()
	green := color.RGBA{0x00, 0xff, 0x00, 0xff}
	p0 := writePNG(t, dir, "c0.png", cellW, cellH, color.RGBA{0x80, 0x80, 0x80, 0xff})
	p1 := writePNG(t, dir, "c1.png", 6, 4, green) // undersized

	res, err := Build(BuildOpts{CellPaths: []string{p0, p1}, Cols: 2, Gutter: 0})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.CellW != cellW || res.CellH != cellH {
		t.Fatalf("cell size from first frame = %dx%d, want %dx%d", res.CellW, res.CellH, cellW, cellH)
	}
	img := decode(t, res)
	r1 := res.CellRect[1]
	// Inside the drawn 6x4 region: green (top-left aligned, not a border pixel).
	if got := pix(img, r1.Min.X+1, r1.Min.Y+1); got != green {
		t.Errorf("undersized frame content = %v, want green %v", got, green)
	}
	// Letterbox area (beyond 6x4, still inside the cell) stays background.
	lx, ly := r1.Min.X+8, r1.Min.Y+6
	if got := pix(img, lx, ly); got != colBackground {
		t.Errorf("letterbox pixel = %v, want background %v", got, colBackground)
	}
}

func TestLargerCellClippedNoPanic(t *testing.T) {
	dir := t.TempDir()
	p0 := writePNG(t, dir, "c0.png", 10, 8, color.RGBA{0x80, 0x80, 0x80, 0xff})
	p1 := writePNG(t, dir, "c1.png", 14, 12, color.RGBA{0x00, 0x00, 0xff, 0xff}) // oversized
	res, err := Build(BuildOpts{CellPaths: []string{p0, p1}, Cols: 2, Gutter: 0})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	img := decode(t, res)
	// Canvas is sized from the first (10x8) cell, so the oversized frame did
	// not enlarge the grid.
	if img.Bounds().Dx() != 20 || img.Bounds().Dy() != 8 {
		t.Errorf("canvas %dx%d, want 20x8 (oversized frame must not scale grid)", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestBuildErrors(t *testing.T) {
	dir := t.TempDir()
	paths := solidCells(t, dir, 1, 4, 4)

	if _, err := Build(BuildOpts{CellPaths: paths, Cols: 0}); err == nil {
		t.Error("expected error for Cols < 1")
	}
	if _, err := Build(BuildOpts{CellPaths: nil, Cols: 2}); err == nil {
		t.Error("expected error for empty CellPaths")
	}
	if _, err := Build(BuildOpts{CellPaths: []string{filepath.Join(dir, "missing.png")}, Cols: 1}); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestMergeTimeline(t *testing.T) {
	res := MontageResult{Rows: 2, Cols: 3} // grid has 6 cells (0..5)
	frames := []Frame{
		{Index: 0, TWorld: 0.0, State: map[string]any{"k": 1}},
		{Index: 4, TWorld: 1.5, State: map[string]any{"k": 2}},
		{Index: 7, TWorld: 3.0, State: map[string]any{"k": 3}}, // beyond grid
	}
	out := MergeTimeline(frames, res)
	if len(out) != len(frames) {
		t.Fatalf("MergeTimeline returned %d entries, want %d", len(out), len(frames))
	}
	want := []struct{ row, col int }{{0, 0}, {1, 1}, {2, 1}}
	for i, e := range out {
		if e.Row != want[i].row || e.Col != want[i].col {
			t.Errorf("entry %d: (row,col)=(%d,%d), want (%d,%d)", i, e.Row, e.Col, want[i].row, want[i].col)
		}
		if e.Index != frames[i].Index || e.TWorld != frames[i].TWorld {
			t.Errorf("entry %d: index/tworld not carried: %+v", i, e)
		}
		if e.State["k"] != frames[i].State["k"] {
			t.Errorf("entry %d: state not carried: %+v", i, e.State)
		}
	}
}

func TestMergeTimelineEmpty(t *testing.T) {
	if out := MergeTimeline(nil, MontageResult{Cols: 3}); len(out) != 0 {
		t.Errorf("expected empty timeline, got %d entries", len(out))
	}
}
