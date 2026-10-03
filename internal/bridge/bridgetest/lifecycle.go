package bridgetest

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
)

// Packages scripts the editor's package state for the safe-shutdown and git_revert
// flows: which packages are dirty and which are loaded.
type Packages struct {
	mu     sync.Mutex
	Dirty  []string
	Loaded map[string]bool
}

// Set replaces the scripted state.
func (p *Packages) Set(dirty []string, loaded ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Dirty = dirty
	p.Loaded = map[string]bool{}
	for _, l := range loaded {
		p.Loaded[l] = true
	}
}

// Install registers packages_state.
func (p *Packages) Install(e *Emulator, w *World) {
	e.Handle("packages_state", func(args map[string]any) (any, *OpError) {
		p.mu.Lock()
		defer p.mu.Unlock()
		loaded := map[string]any{}
		names, _ := args["packages"].([]any)
		for _, n := range names {
			s, _ := n.(string)
			loaded[s] = p.Loaded[s]
		}
		dirty := append([]string{}, p.Dirty...)
		w.mu.Lock()
		pie := w.pie != nil
		w.mu.Unlock()
		return map[string]any{"dirty": dirty, "loaded": loaded, "pie": pie, "map": "/Game/Maps/L_Test", "editor_pid": 0}, nil
	})
}

// Recorder emulates the capture recorder: capture_stop writes Frames small PNGs
// (with per-frame observed state) into Dir.
type Recorder struct {
	Dir    string
	Frames int
	State  func(i int) map[string]any
	seq    int
}

// Install registers capture_start / capture_stop.
func (r *Recorder) Install(e *Emulator) {
	e.Handle("capture_start", func(map[string]any) (any, *OpError) {
		r.seq++
		return map[string]any{"session": fmt.Sprintf("s%d", r.seq), "running": true}, nil
	})
	e.Handle("capture_stop", func(args map[string]any) (any, *OpError) {
		sess, _ := args["session"].(string)
		dir := filepath.Join(r.Dir, sess)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, &OpError{Code: "EDITOR_ERROR", Message: err.Error()}
		}
		var frames []map[string]any
		for i := 0; i < r.Frames; i++ {
			name := fmt.Sprintf("f%05d.png", i)
			if err := writePNG(filepath.Join(dir, name), uint8(40*i)); err != nil {
				return nil, &OpError{Code: "EDITOR_ERROR", Message: err.Error()}
			}
			st := map[string]any{}
			if r.State != nil {
				st = r.State(i)
			}
			frames = append(frames, map[string]any{"index": i, "file": name, "t_world": float64(i) * 0.5, "state": st})
		}
		return map[string]any{"dir": dir, "frame_count": len(frames), "frames": frames, "stop_reason": "requested",
			"cell_width": 8, "cell_height": 8}, nil
	})
}

func writePNG(path string, shade uint8) error {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{shade, shade, shade, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
