// Package montage assembles many equal-sized PNG frames into a single
// contact-sheet (grid) image — a "filmstrip" — so an LLM can reason over a
// whole sequence of captures in one image instead of many. Cells are assumed
// to be pre-rendered at the target resolution and are tiled 1:1 (never scaled),
// which keeps the package dependency-free: standard library only, no
// golang.org/x/image. Cells can be marked (red border) as visual evidence of a
// failed check, and labeled with a small embedded bitmap font (see font.go).
//
// The package also maps a logical frame timeline onto grid coordinates
// (MergeTimeline / CellForFrame) so a sidecar can say "check failed at frame i"
// and a reader can point at the exact cell (row, col) in the montage.
package montage

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

// BuildOpts configures a montage build.
type BuildOpts struct {
	// CellPaths are the PNG frames to tile, in row-major order. The first
	// decoded frame's dimensions define the cell size for the whole grid.
	CellPaths []string
	// Cols is the number of grid columns. Must be >= 1.
	Cols int
	// Gutter is the pixel spacing between cells and around the canvas edge.
	Gutter int
	// MarkCells are 0-based cell indices to flag with a red 2px border.
	MarkCells []int
	// LabelForCell, if non-nil, returns the label to stamp into the top-left
	// of cell i. Returning "" stamps nothing for that cell.
	LabelForCell func(i int) string
}

// Result is a built montage.
type Result struct {
	PNG      []byte                  // the encoded contact-sheet PNG
	Rows     int                     // number of grid rows
	Cols     int                     // number of grid columns actually used
	CellW    int                     // cell width in pixels (first frame)
	CellH    int                     // cell height in pixels (first frame)
	CellRect map[int]image.Rectangle // absolute rect of each cell index
}

// Frame is one logical entry in a capture timeline, keyed by its index in the
// sequence (which maps row-major onto the montage grid).
type Frame struct {
	Index  int
	TWorld float64
	State  map[string]any
}

// TimelineEntry is a Frame enriched with its grid coordinates in a Result.
type TimelineEntry struct {
	Index  int
	TWorld float64
	State  map[string]any
	Row    int
	Col    int
}

// Palette for the contact sheet. All opaque.
var (
	colBackground = color.RGBA{0x20, 0x20, 0x20, 0xff} // dark gray canvas
	colBorder     = color.RGBA{0x60, 0x60, 0x60, 0xff} // subtle cell border
	colMark       = color.RGBA{0xff, 0x00, 0x00, 0xff} // marked-cell border
	colLabel      = color.RGBA{0xff, 0xff, 0xff, 0xff} // label text
)

// Build tiles the PNGs in opts.CellPaths into one contact-sheet image.
//
// Cell size is taken from the first decoded frame. Cells are drawn 1:1,
// top-left aligned; a cell whose decoded size differs is letterboxed against
// the dark background (never scaled) rather than causing an error. Every cell
// gets a 1px border; cells in opts.MarkCells get a red 2px border instead.
// Labels (if opts.LabelForCell is set) are clipped to their own cell.
func Build(opts BuildOpts) (Result, error) {
	if opts.Cols < 1 {
		return Result{}, fmt.Errorf("montage: Cols must be >= 1, got %d", opts.Cols)
	}
	if len(opts.CellPaths) == 0 {
		return Result{}, fmt.Errorf("montage: no cell paths provided")
	}

	cells := make([]image.Image, len(opts.CellPaths))
	for i, p := range opts.CellPaths {
		img, err := decodePNG(p)
		if err != nil {
			return Result{}, err
		}
		cells[i] = img
	}

	n := len(cells)
	cellW := cells[0].Bounds().Dx()
	cellH := cells[0].Bounds().Dy()

	// Actual columns used: opts.Cols, or n when there are fewer cells than
	// columns (a tighter single row).
	cols := opts.Cols
	if n < cols {
		cols = n
	}
	rows := (n + cols - 1) / cols // ceil(n/cols)

	g := opts.Gutter
	if g < 0 {
		g = 0
	}

	canvasW := cols*cellW + (cols+1)*g
	canvasH := rows*cellH + (rows+1)*g
	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(colBackground), image.Point{}, draw.Src)

	marked := make(map[int]bool, len(opts.MarkCells))
	for _, m := range opts.MarkCells {
		marked[m] = true
	}

	rects := make(map[int]image.Rectangle, n)
	for i := 0; i < n; i++ {
		row, col := CellForFrame(i, cols)
		x := g + col*(cellW+g)
		y := g + row*(cellH+g)
		rect := image.Rect(x, y, x+cellW, y+cellH)
		rects[i] = rect

		// Draw the cell 1:1, top-left aligned, clamped to the cell box. A
		// smaller frame leaves the dark background showing (letterbox); a
		// larger frame is cropped. Never scaled.
		src := cells[i]
		sb := src.Bounds()
		dw := sb.Dx()
		if dw > cellW {
			dw = cellW
		}
		dh := sb.Dy()
		if dh > cellH {
			dh = cellH
		}
		draw.Draw(canvas, image.Rect(x, y, x+dw, y+dh), src, sb.Min, draw.Src)

		// Border on top of the frame edge.
		if marked[i] {
			drawRectBorder(canvas, rect, colMark, 2)
		} else {
			drawRectBorder(canvas, rect, colBorder, 1)
		}

		// Label, clipped to this cell only.
		if opts.LabelForCell != nil {
			if s := opts.LabelForCell(i); s != "" {
				drawString(canvas, x+2, y+2, s, colLabel, rect)
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return Result{}, fmt.Errorf("montage: encode: %w", err)
	}

	return Result{
		PNG:      buf.Bytes(),
		Rows:     rows,
		Cols:     cols,
		CellW:    cellW,
		CellH:    cellH,
		CellRect: rects,
	}, nil
}

// CellForFrame maps a 0-based frame index to its row-major grid coordinates for
// a grid of the given column count. cols < 1 is treated as 1 so the function is
// total and never divides by zero.
func CellForFrame(i, cols int) (row, col int) {
	if cols < 1 {
		cols = 1
	}
	return i / cols, i % cols
}

// MergeTimeline enriches each frame with the grid (row, col) it lands on in res,
// carrying TWorld and State through. It is total: frames whose index falls
// beyond the last grid cell (Index >= res.Rows*res.Cols) are still returned with
// their computed (row, col); they simply have no cell drawn in res.
func MergeTimeline(frames []Frame, res Result) []TimelineEntry {
	out := make([]TimelineEntry, 0, len(frames))
	for _, f := range frames {
		row, col := CellForFrame(f.Index, res.Cols)
		out = append(out, TimelineEntry{
			Index:  f.Index,
			TWorld: f.TWorld,
			State:  f.State,
			Row:    row,
			Col:    col,
		})
	}
	return out
}

// decodePNG opens and decodes a single PNG file.
func decodePNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("montage: decode %s: %w", path, err)
	}
	return img, nil
}

// drawRectBorder paints a border of thick pixels, inset inward from r's edges.
func drawRectBorder(dst draw.Image, r image.Rectangle, col color.Color, thick int) {
	for t := 0; t < thick; t++ {
		rr := image.Rect(r.Min.X+t, r.Min.Y+t, r.Max.X-t, r.Max.Y-t)
		if rr.Empty() {
			return
		}
		for x := rr.Min.X; x < rr.Max.X; x++ {
			dst.Set(x, rr.Min.Y, col)
			dst.Set(x, rr.Max.Y-1, col)
		}
		for y := rr.Min.Y; y < rr.Max.Y; y++ {
			dst.Set(rr.Min.X, y, col)
			dst.Set(rr.Max.X-1, y, col)
		}
	}
}
