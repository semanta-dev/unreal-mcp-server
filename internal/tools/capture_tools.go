package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/framing"
	"github.com/jdziat/unreal-mcp-server/internal/montage"
)

type captureStartIn struct {
	Session           string    `json:"session,omitempty" jsonschema:"a session id to reuse; omit to auto-generate"`
	World             string    `json:"world,omitempty" jsonschema:"auto|editor|game; default auto (game world if in PIE)"`
	Source            string    `json:"source,omitempty" jsonschema:"scene_capture (editor/simulate, backgrounded-safe; default) | pie_highres (possessed PIE, needs a foreground viewport) | game_scene (live PIE via the UnrealMCP C++ plugin — renders backgrounded)"`
	IntervalS         float64   `json:"interval_s,omitempty" jsonschema:"seconds between frames; default 0.25"`
	CellWidth         int       `json:"cell_width,omitempty" jsonschema:"per-frame width; default 480"`
	CellHeight        int       `json:"cell_height,omitempty" jsonschema:"per-frame height; default 270"`
	CameraMode        string    `json:"camera_mode,omitempty" jsonschema:"viewport (follow editor viewport; default) | fixed | actor"`
	CameraActor       string    `json:"camera_actor,omitempty" jsonschema:"actor label to ride when camera_mode=actor"`
	CameraFov         float64   `json:"camera_fov,omitempty" jsonschema:"field of view for game_scene fixed/player capture; default 90"`
	CameraLocation    []float64 `json:"camera_location,omitempty" jsonschema:"[x,y,z] for camera_mode=fixed"`
	CameraRotationPyr []float64 `json:"camera_rotation_pyr,omitempty" jsonschema:"[pitch,yaw,roll] for camera_mode=fixed"`
	TrackActors       []string  `json:"track_actors,omitempty" jsonschema:"actor labels to record detailed per-frame state for"`
	MaxFrames         int       `json:"max_frames,omitempty" jsonschema:"auto-stop after this many frames; default 240"`
	MaxSeconds        float64   `json:"max_seconds,omitempty" jsonschema:"auto-stop after this many seconds; default 60"`
	Include           []string  `json:"include,omitempty" jsonschema:"observe: property include globs"`
	Exclude           []string  `json:"exclude,omitempty" jsonschema:"observe: property exclude globs"`
	Properties        []string  `json:"properties,omitempty" jsonschema:"observe: exact property names"`
	IncludeUI         bool      `json:"include_ui,omitempty" jsonschema:"game_scene only: capture the composited on-screen viewport INCLUDING the Slate/UMG HUD (needs a visible/rendering window). Default false = 3D scene only (backgrounded-safe, no HUD)."`
}

type captureSessionIn struct {
	Session string `json:"session" jsonschema:"the capture session id"`
}

type captureStopIn struct {
	Session    string `json:"session" jsonschema:"the capture session id"`
	Cols       int    `json:"cols,omitempty" jsonschema:"montage columns; default 8"`
	DrawLabels bool   `json:"draw_labels,omitempty" jsonschema:"stamp each frame's world time into its cell"`
	MarkCells  []int  `json:"mark_cells,omitempty" jsonschema:"frame indices to flag with a red border"`
}

type sceneContactSheetIn struct {
	Target     []string `json:"target,omitempty" jsonschema:"actor labels/globs to frame; empty = whole level"`
	NumAngles  int      `json:"num_angles,omitempty" jsonschema:"orbit angles to capture; default 8"`
	Elevation  *float64 `json:"elevation,omitempty" jsonschema:"camera elevation degrees above horizon; default 25 (0 = horizontal orbit)"`
	Fov        float64  `json:"fov,omitempty" jsonschema:"vertical field of view degrees; default 60"`
	Fill       float64  `json:"fill,omitempty" jsonschema:"fraction of the frame the target fills; default 0.7"`
	Cols       int      `json:"cols,omitempty" jsonschema:"montage columns; default 4"`
	CellWidth  int      `json:"cell_width,omitempty" jsonschema:"per-angle width; default 480"`
	CellHeight int      `json:"cell_height,omitempty" jsonschema:"per-angle height; default 270"`
	World      string   `json:"world,omitempty" jsonschema:"auto|editor|game; default editor"`
}

// registerCaptureTools adds the multi-frame capture tools (goal A). The recorder
// runs in-editor and buffers frames+state to disk; Go collects them in one
// capture_stop and assembles a single contact-sheet montage + a synchronized
// timeline sidecar, so an agent reasons over a filmstrip in one image.
func registerCaptureTools(s *mcp.Server, d Deps) {
	b := d.Bridge
	add(s, "capture_start",
		"Start an in-editor recorder that captures a screenshot + observed game state every interval_s to disk (no per-frame round-trip). Use scene_capture for editor/simulate, pie_highres for possessed PIE. Stop with capture_stop.",
		structHandler[captureStartIn](b, "capture_start", func(in captureStartIn) map[string]any {
			m := map[string]any{}
			for k, v := range map[string]string{"session": in.Session, "world": in.World, "source": in.Source} {
				if v != "" {
					m[k] = v
				}
			}
			if in.IntervalS > 0 {
				m["interval_s"] = in.IntervalS
			}
			if in.CellWidth > 0 {
				m["cell_width"] = in.CellWidth
			}
			if in.CellHeight > 0 {
				m["cell_height"] = in.CellHeight
			}
			if in.MaxFrames > 0 {
				m["max_frames"] = in.MaxFrames
			}
			if in.MaxSeconds > 0 {
				m["max_seconds"] = in.MaxSeconds
			}
			if in.IncludeUI {
				m["include_ui"] = true
			}
			if len(in.TrackActors) > 0 {
				m["track_actors"] = in.TrackActors
			}
			if in.CameraMode != "" || in.CameraActor != "" || len(in.CameraLocation) > 0 {
				cam := map[string]any{}
				if in.CameraMode != "" {
					cam["mode"] = in.CameraMode
				}
				if in.CameraActor != "" {
					cam["actor_label"] = in.CameraActor
				}
				if len(in.CameraLocation) > 0 {
					cam["location"] = in.CameraLocation
				}
				if len(in.CameraRotationPyr) > 0 {
					cam["rotation_pyr"] = in.CameraRotationPyr
				}
				if in.CameraFov > 0 {
					cam["fov"] = in.CameraFov
				}
				m["camera"] = cam
			}
			obs := map[string]any{}
			if len(in.Include) > 0 {
				obs["include"] = in.Include
			}
			if len(in.Exclude) > 0 {
				obs["exclude"] = in.Exclude
			}
			if len(in.Properties) > 0 {
				obs["properties"] = in.Properties
			}
			if len(obs) > 0 {
				m["observe"] = obs
			}
			return m
		}))

	add(s, "capture_status",
		"One-shot status of a running capture (frames captured so far, last world time). Does NOT block or poll — the recorder owns cadence.",
		structHandler[captureSessionIn](b, "capture_poll", func(in captureSessionIn) map[string]any {
			return map[string]any{"session": in.Session}
		}))

	add(s, "capture_stop", "Stop a capture recorder and return a single contact-sheet montage image plus a synchronized timeline (per-frame world time + observed state, each mapped to a montage cell).",
		captureStop(b))

	add(s, "scene_contact_sheet", "Render a target (or the whole level) from N orbit angles into one contact-sheet montage — a quick multi-angle visual of a level/asset for design validation. Editor world (backgrounded-safe).",
		sceneContactSheet(b))

	add(s, "read_capture",
		"View an existing capture on disk as a contact-sheet montage — a session under Saved/MCP/capture, or an explicit dir of frames (e.g. your own capture output). Frames are decoded by content, so extensionless files work (no copy-to-.png needed).",
		readCapture(d))

	add(s, "capture_clear",
		"Delete MCP capture files from disk (a specific session, or all of them) so runs don't accumulate frames. Clears both Saved/MCP/capture and the mcp_* pie_highres screenshots.",
		captureClear(d))
}

type readCaptureIn struct {
	Session string `json:"session,omitempty" jsonschema:"a capture session dir under Saved/MCP/capture to view"`
	Dir     string `json:"dir,omitempty" jsonschema:"an explicit directory of frames to view (overrides session)"`
	Glob    string `json:"glob,omitempty" jsonschema:"file glob within the dir; default '*'"`
	Cols    int    `json:"cols,omitempty" jsonschema:"montage columns; default 6"`
}

func readCapture(d Deps) mcp.ToolHandlerFor[readCaptureIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in readCaptureIn) (*mcp.CallToolResult, any, error) {
		dir := in.Dir
		if dir == "" && in.Session != "" && resolveDeps(ctx, d).ProjectDir != "" {
			dir = filepath.Join(resolveDeps(ctx, d).ProjectDir, "Saved", "MCP", "capture", in.Session)
		}
		if dir == "" {
			return nil, nil, fmt.Errorf("provide session or dir")
		}
		pattern := in.Glob
		if pattern == "" {
			pattern = "*"
		}
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, nil, err
		}
		var files []string
		for _, m := range matches {
			if filepath.Ext(m) == ".json" {
				continue // skip manifest.json
			}
			if fi, err := os.Stat(m); err == nil && !fi.IsDir() && fi.Size() > 0 {
				files = append(files, m)
			}
		}
		sort.Strings(files)
		if len(files) == 0 {
			return textResult("no capture frames in " + dir), nil, nil
		}
		frames := make([]captureFrame, len(files))
		for i, f := range files {
			frames[i] = captureFrame{Index: i, File: filepath.Base(f)}
		}
		cols := in.Cols
		if cols <= 0 {
			cols = 6
		}
		return buildMontageResult(dir, frames, cols, false, nil, map[string]any{"read_from": dir, "count": len(files)})
	}
}

type captureClearIn struct {
	Session string `json:"session,omitempty" jsonschema:"a specific capture session to delete; empty = clear ALL MCP captures"`
}

func captureClear(d Deps) mcp.ToolHandlerFor[captureClearIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in captureClearIn) (*mcp.CallToolResult, map[string]any, error) {
		if resolveDeps(ctx, d).ProjectDir == "" {
			return nil, nil, fmt.Errorf("clearing captures needs -project")
		}
		root := filepath.Join(resolveDeps(ctx, d).ProjectDir, "Saved", "MCP", "capture")
		shots := filepath.Join(resolveDeps(ctx, d).ProjectDir, "Saved", "Screenshots")
		removed := 0
		rm := func(p string) {
			if os.RemoveAll(p) == nil {
				removed++
			}
		}
		if in.Session != "" {
			rm(filepath.Join(root, in.Session))
			if ms, _ := filepath.Glob(filepath.Join(shots, "mcp_"+in.Session+"_*")); ms != nil {
				for _, m := range ms {
					rm(m)
				}
			}
		} else {
			if entries, _ := os.ReadDir(root); entries != nil {
				for _, e := range entries {
					rm(filepath.Join(root, e.Name()))
				}
			}
			if ms, _ := filepath.Glob(filepath.Join(shots, "mcp_*")); ms != nil {
				for _, m := range ms {
					rm(m)
				}
			}
		}
		return nil, map[string]any{"removed": removed, "capture_dir": root}, nil
	}
}

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

func captureStop(b *bridge.Bridge) mcp.ToolHandlerFor[captureStopIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in captureStopIn) (*mcp.CallToolResult, any, error) {
		raw, err := bridgeFromCtx(ctx, b).Call(ctx, "capture_stop", map[string]any{"session": in.Session})
		if err != nil {
			return nil, nil, err
		}
		var r captureStopResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, nil, err
		}
		if r.Error != "" {
			return nil, nil, fmt.Errorf("capture_stop: %s", r.Error)
		}
		if len(r.Frames) == 0 {
			return textResult("capture produced 0 frames (recorder saw no ticks — was the editor/PIE actually ticking?)"), nil, nil
		}
		cols := in.Cols
		if cols <= 0 {
			cols = 8
		}
		return buildMontageResult(r.Dir, r.Frames, cols, in.DrawLabels, in.MarkCells,
			map[string]any{"stop_reason": r.StopReason})
	}
}

// buildMontageResult tiles the frames into one contact sheet and returns it as
// image content plus a JSON timeline sidecar (index -> t_world, cell, state).
func buildMontageResult(dir string, frames []captureFrame, cols int, drawLabels bool, markCells []int, extra map[string]any) (*mcp.CallToolResult, any, error) {
	sort.Slice(frames, func(i, j int) bool { return frames[i].Index < frames[j].Index })
	// Keep only frames whose PNG is actually on disk. HighResShot (pie_highres)
	// writes asynchronously, so a not-yet-flushed final frame is skipped rather
	// than failing the whole montage. Survivors are re-indexed to their grid
	// POSITION so mark-cells and cell coordinates stay aligned after any drop.
	survivors := make([]captureFrame, 0, len(frames))
	for _, f := range frames {
		if fi, err := os.Stat(filepath.Join(dir, f.File)); err == nil && fi.Size() > 0 {
			survivors = append(survivors, f)
		}
	}
	dropped := len(frames) - len(survivors)
	if len(survivors) == 0 {
		return textResult("no capture frames were readable on disk (frames may still be flushing; increase interval_s or retry)"), nil, nil
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
	res, err := montage.Build(montage.BuildOpts{CellPaths: cellPaths, Cols: cols, Gutter: 6, MarkCells: marks, LabelForCell: labelFn})
	if err != nil {
		return nil, nil, err
	}
	tl := make([]map[string]any, len(survivors))
	for pos, f := range survivors {
		row, col := montage.CellForFrame(pos, res.Cols)
		tl[pos] = map[string]any{"i": pos, "frame": f.Index, "t": f.TWorld, "cell": []int{row, col}, "state": f.State}
	}
	sidecar := map[string]any{
		"dir":      dir,
		"frames":   len(survivors),
		"dropped":  dropped,
		"montage":  map[string]any{"rows": res.Rows, "cols": res.Cols, "cell_w": res.CellW, "cell_h": res.CellH},
		"timeline": tl,
	}
	for k, v := range extra {
		sidecar[k] = v
	}
	sidecarJSON, _ := json.Marshal(sidecar)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: res.PNG, MIMEType: "image/png"},
		&mcp.TextContent{Text: string(sidecarJSON)},
	}}, nil, nil
}

func sceneContactSheet(b *bridge.Bridge) mcp.ToolHandlerFor[sceneContactSheetIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in sceneContactSheetIn) (*mcp.CallToolResult, any, error) {
		// 1. combined bounds of the target (or whole level).
		bargs := map[string]any{}
		if len(in.Target) > 0 {
			bargs["globs"] = in.Target
		}
		braw, err := bridgeFromCtx(ctx, b).Call(ctx, "scene_bounds", bargs)
		if err != nil {
			return nil, nil, err
		}
		var br struct {
			Combined struct {
				Origin []float64 `json:"origin"`
				Extent []float64 `json:"extent"`
			} `json:"combined"`
		}
		if err := json.Unmarshal(braw, &br); err != nil {
			return nil, nil, err
		}
		bounds := framing.Bounds{Origin: toVec3(br.Combined.Origin), Extent: toVec3(br.Combined.Extent)}

		// 2. Go computes the orbit poses (pure framing math).
		num := in.NumAngles
		if num <= 0 {
			num = 8
		}
		elevation := 25.0
		if in.Elevation != nil {
			elevation = *in.Elevation
		}
		fov, fill := orDefault(in.Fov, 60), orDefault(in.Fill, 0.7)
		poses := make([]map[string]any, num)
		for i := 0; i < num; i++ {
			az := 360.0 * float64(i) / float64(num)
			p := framing.FrameShot(bounds, az, elevation, fov, fill)
			poses[i] = map[string]any{
				"location":     []float64{p.Location[0], p.Location[1], p.Location[2]},
				"rotation_pyr": []float64{p.RotationPyr[0], p.RotationPyr[1], p.RotationPyr[2]},
			}
		}

		// 3. editor renders each pose synchronously to disk.
		cw, ch := orDefaultInt(in.CellWidth, 480), orDefaultInt(in.CellHeight, 270)
		world := in.World
		if world == "" {
			world = "editor"
		}
		praw, err := bridgeFromCtx(ctx, b).Call(ctx, "capture_poses", map[string]any{"poses": poses, "cell_width": cw, "cell_height": ch, "world": world})
		if err != nil {
			return nil, nil, err
		}
		var pr struct {
			Dir   string `json:"dir"`
			Cells []struct {
				Index       int       `json:"index"`
				File        string    `json:"file"`
				RotationPyr []float64 `json:"rotation_pyr"`
			} `json:"cells"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(praw, &pr); err != nil {
			return nil, nil, err
		}
		if pr.Error != "" {
			return nil, nil, fmt.Errorf("capture_poses: %s", pr.Error)
		}
		frames := make([]captureFrame, len(pr.Cells))
		for i, c := range pr.Cells {
			yaw := 0.0
			if len(c.RotationPyr) > 1 {
				yaw = c.RotationPyr[1]
			}
			frames[i] = captureFrame{Index: c.Index, File: c.File, State: map[string]any{"yaw": yaw}}
		}
		cols := in.Cols
		if cols <= 0 {
			cols = 4
		}
		return buildMontageResult(pr.Dir, frames, cols, false, nil,
			map[string]any{"center": br.Combined.Origin, "angles": num})
	}
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
