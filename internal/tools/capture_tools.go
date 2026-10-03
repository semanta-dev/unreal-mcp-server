package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

// captureFrame mirrors one entry of the recorder manifest.
type captureFrame struct {
	Index  int            `json:"index"`
	File   string         `json:"file"`
	TWall  float64        `json:"t_wall"`
	TWorld float64        `json:"t_world"`
	State  map[string]any `json:"state"`
}

type captureStopResult struct {
	Dir        string         `json:"dir"`
	FrameCount int            `json:"frame_count"`
	CellWidth  int            `json:"cell_width"`
	CellHeight int            `json:"cell_height"`
	Frames     []captureFrame `json:"frames"`
	StopReason string         `json:"stop_reason"`
	Error      string         `json:"error"`
}

// errNoFrames: no frame was readable on disk.
var errNoFrames = envelope.New(envelope.OperationFailed, "no capture frames were readable on disk").
	WithHint("frames may still be flushing; increase interval_s or retry")

// montage tiles the frames into one contact sheet and returns its PNG plus a
// timeline sidecar (index -> t_world, cell, state). Frames missing on disk (e.g. a
// not-yet-flushed HighResShot) are skipped; survivors are re-indexed to their grid
// position so mark cells and cell coordinates stay aligned.
func montage(dir string, frames []captureFrame, cols int, drawLabels bool, markCells []int) ([]byte, map[string]any, error) {
	sort.Slice(frames, func(i, j int) bool { return frames[i].Index < frames[j].Index })
	survivors := make([]captureFrame, 0, len(frames))
	for _, f := range frames {
		if fi, err := os.Stat(filepath.Join(dir, f.File)); err == nil && fi.Size() > 0 {
			survivors = append(survivors, f)
		}
	}
	if len(survivors) == 0 {
		return nil, nil, errNoFrames
	}
	cellPaths := make([]string, len(survivors))
	posOf := make(map[int]int, len(survivors)) // original frame index -> grid position
	for pos, f := range survivors {
		cellPaths[pos] = filepath.Join(dir, f.File)
		posOf[f.Index] = pos
	}
	var marks []int
	for _, m := range markCells {
		if p, ok := posOf[m]; ok {
			marks = append(marks, p)
		}
	}
	var labelFn func(int) string
	if drawLabels {
		labelFn = func(i int) string {
			if i < len(survivors) {
				return fmt.Sprintf("%.1fs", survivors[i].TWorld)
			}
			return ""
		}
	}
	res, err := visual.Build(visual.BuildOpts{CellPaths: cellPaths, Cols: cols, Gutter: 6, MarkCells: marks, LabelForCell: labelFn})
	if err != nil {
		return nil, nil, err
	}
	tl := make([]map[string]any, len(survivors))
	for pos, f := range survivors {
		row, col := visual.CellForFrame(pos, res.Cols)
		tl[pos] = map[string]any{"i": pos, "frame": f.Index, "t": f.TWorld, "cell": []int{row, col}, "state": f.State}
	}
	return res.PNG, map[string]any{
		"dir":      dir,
		"frames":   len(survivors),
		"dropped":  len(frames) - len(survivors),
		"montage":  map[string]any{"rows": res.Rows, "cols": res.Cols, "cell_w": res.CellW, "cell_h": res.CellH},
		"timeline": tl,
	}, nil
}

func toVec3(s []float64) [3]float64 {
	var v [3]float64
	for i := 0; i < 3 && i < len(s); i++ {
		v[i] = s[i]
	}
	return v
}

func orDefault(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
