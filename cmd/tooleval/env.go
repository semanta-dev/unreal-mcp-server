package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/app"
	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// v2Env is one task run's world: the real v2 server (core toolset, as an agent first
// sees it) over the T1 op emulator in stateful-world mode, in a throwaway project.
type v2Env struct {
	world *bridgetest.World
	emu   *bridgetest.Emulator
	cs    *mcp.ClientSession
	dir   string

	mu      sync.Mutex
	changed bool // tools/list_changed seen since the last listing
	closers []func()
}

func uexecConfig() uexec.Config {
	c := uexec.DefaultConfig()
	c.CommandAddr = "127.0.0.1:0"
	c.PingInterval = 50 * time.Millisecond
	c.NodeTimeout = 3 * time.Second
	c.DiscoveryTimeout = 3 * time.Second
	c.CommandTimeout = 10 * time.Second
	c.AcceptAttempts = 4
	c.AcceptTimeout = 400 * time.Millisecond
	return c
}

func newV2Env(ctx context.Context, t *task) (*v2Env, error) {
	e := &v2Env{world: bridgetest.NewWorld(), emu: bridgetest.New()}
	dir, err := os.MkdirTemp("", "tooleval-")
	if err != nil {
		return nil, err
	}
	e.dir = dir
	e.closers = append(e.closers, func() { _ = os.RemoveAll(dir) })
	if err := seedProject(dir); err != nil {
		e.Close()
		return nil, err
	}
	e.world.Install(e.emu)
	installPermissive(e.emu, e.world, dir)
	for _, a := range t.Setup.Actors {
		e.world.AddActor(a.Label, a.Class, a.Location, a.Properties)
	}
	for _, p := range t.Setup.Assets {
		e.world.AddAsset(p, "Blueprint")
	}
	if t.Setup.PIE {
		e.world.StartPIE()
	}

	ed, err := uexectest.Start(e.emu.Options())
	if err != nil {
		e.Close()
		return nil, err
	}
	e.closers = append(e.closers, ed.Close)
	cfg := uexecConfig()
	disc, err := uexec.OpenUnicastDiscovery(ctx, cfg, ed.Addr(), nil)
	if err != nil {
		e.Close()
		return nil, err
	}
	e.closers = append(e.closers, func() { disc.Close() })
	sess := uexec.NewOnDiscovery(cfg, disc, nil)
	e.closers = append(e.closers, func() { sess.Close() })
	b := bridge.New(sess, bridge.Options{})
	deps := tools.Deps{Bridge: b, Jobs: jobs.NewRegistry(), ProjectDir: dir}
	srv := app.NewServer(app.Options{Deps: deps, Catalog: evalCatalog}, nil).MCP

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		e.Close()
		return nil, err
	}
	e.closers = append(e.closers, func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "tooleval", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			e.mu.Lock()
			e.changed = true
			e.mu.Unlock()
		}}).Connect(ctx, ct, nil)
	if err != nil {
		e.Close()
		return nil, err
	}
	e.closers = append(e.closers, func() { cs.Close() })
	e.cs = cs
	return e, nil
}

// toolsChanged reports (and clears) whether the server announced a new tool list.
func (e *v2Env) toolsChanged() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.changed
	e.changed = false
	return c
}

func (e *v2Env) Close() {
	for i := len(e.closers) - 1; i >= 0; i-- {
		e.closers[i]()
	}
}

// seedProject writes a minimal UE project: a .uproject, a log, config, and a git repo
// with one checkpoint, so git/logs/project tools have something real to read.
func seedProject(dir string) error {
	files := map[string]string{
		"Game.uproject":           `{"Modules":[{"Name":"Game"}]}`,
		"Saved/Logs/Game.log":     "LogTemp: Display: editor ready\nLogBlueprint: Warning: BP_Spawner has an unused variable\n",
		"Config/DefaultGame.ini":  "[/Script/EngineSettings.GeneralProjectSettings]\nProjectName=Game\n",
		"Source/Game/Game.cpp":    "// game module\n",
		"Saved/Profiling/run.csv": "FrameTime,GameThreadTime\n16.6,9.1\n33.4,20.2\n16.7,9.0\n",
		".mcp/scenarios/smoke.json": `{"schema":"scenario/v1","name":"smoke","mode":"pie","duration_s":2,"interval_s":0.5,` +
			`"rubric":[{"id":"no_errors","kind":"log_zero","path":"errors","severity":"warn"}]}`,
	}
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	for _, name := range []string{"Saved/frame.png", "Saved/shot_a.png", "Saved/shot_b.png"} {
		if err := bridgetest.WritePNG(filepath.Join(dir, filepath.FromSlash(name)), 64); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil // git tools then report their own error, as on a machine without git
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "e@e"}, {"config", "user.name", "eval"},
		{"config", "commit.gpgsign", "false"}, {"config", "tag.gpgsign", "false"}, {"add", "-A"},
		{"commit", "-q", "-m", "baseline"}, {"tag", "-a", "umcp/cp/1", "-m", "baseline"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v: %s", args, err, out)
		}
	}
	return nil
}

// installPermissive answers the companion ops the stateful world does not model with
// plausible results (images are real PNGs), so an agent is never derailed by the
// emulator rather than by the tool surface.
func installPermissive(emu *bridgetest.Emulator, w *bridgetest.World, dir string) {
	png := filepath.Join(dir, "frame.png")
	_ = bridgetest.WritePNG(png, 64)
	pngBytes, _ := os.ReadFile(png)
	var n int
	var nmu sync.Mutex
	shot := func(prefix string) string {
		nmu.Lock()
		n++
		k := n
		nmu.Unlock()
		p := filepath.Join(dir, fmt.Sprintf("%s_%d.png", prefix, k))
		_ = os.WriteFile(p, pngBytes, 0o644)
		return p
	}
	(&bridgetest.Recorder{Dir: filepath.Join(dir, "Saved", "MCP", "capture"), Frames: 3}).Install(emu)
	(&bridgetest.Packages{}).Install(emu, w)
	ok := func(v map[string]any) bridgetest.OpFunc {
		return func(map[string]any) (any, *bridgetest.OpError) { return v, nil }
	}
	for _, op := range []string{"python_recipe", "open_level", "save_all", "set_world_gamemode", "console", "viewport_get",
		"viewport_set", "focus_actors", "asset_info", "asset_query", "asset_deps", "asset_tags", "asset_edit", "import_assets",
		"asset_reimport", "datatable_import", "map_gameplay", "widget_tree", "widget_describe", "widget_compose",
		"widget_compile", "world_query", "instances_count", "pie_input", "audio_capture_start", "audio_capture_stop",
		"play_test_sound", "scene_apply", "scene_clear", "scene_prune", "scene_actors", "design_probe", "capture_poll",
		"company_status", "company_build", "company_select", "company_road", "company_demolish", "apply_level_recipe",
		"quit_editor"} {
		emu.Handle(op, ok(map[string]any{"ok": true, "count": 0.0, "total": 0.0, "actors": []any{}, "capital": 100.0,
			"imported": []any{"/Game/Imported/X"}, "rows": 0.0, "digest": "d", "saved": true, "loaded": true}))
	}
	emu.Handle("instances_list", ok(map[string]any{"instances": []any{}, "count": 0.0}))
	emu.Handle("scene_bounds", ok(map[string]any{"combined": map[string]any{"origin": []any{0.0, 0.0, 0.0},
		"extent": []any{100.0, 100.0, 100.0}}, "count": 1.0}))
	emu.Handle("asset_thumbnail", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"thumbnail_path": shot("thumb"), "rendered": true, "num_lods": 1.0}, nil
	})
	emu.Handle("widget_render", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"ok": true, "path": shot("render")}, nil
	})
	emu.Handle("take_screenshot", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"file": shot("shot")}, nil
	})
	emu.Handle("pie_screenshot", func(map[string]any) (any, *bridgetest.OpError) {
		if !w.Snapshot().PIERunning {
			return nil, &bridgetest.OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
		}
		return map[string]any{"file": shot("pie")}, nil
	})
	emu.Handle("capture_poses", func(args map[string]any) (any, *bridgetest.OpError) {
		poses, _ := args["poses"].([]any)
		var cells []any
		for i := range poses {
			p := shot("pose")
			cells = append(cells, map[string]any{"index": float64(i), "file": filepath.Base(p), "rotation_pyr": []any{0.0, float64(i), 0.0}})
		}
		return map[string]any{"dir": dir, "cells": cells}, nil
	})
}

// evalCatalog is the real v2 catalog with the desktop tools' handlers replaced: they
// would capture and drive this machine's real screen (and send it to the API).
func evalCatalog(d session.Deps) []*spec.Spec {
	specs := tools.Specs(d)
	for _, sp := range specs {
		switch sp.Name {
		case "desktop_capture":
			sp.Handler = func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
				if c.Op.Name == "list_windows" {
					return &spec.Result{Data: map[string]any{"count": 1, "windows": []any{map[string]any{
						"title": "Game - Unreal Editor", "pid": 4242, "hwnd": 1001, "foreground": true}}}}, nil
				}
				return &spec.Result{Data: map[string]any{"title": "Game - Unreal Editor", "width": 64, "height": 64},
					Content: []mcp.Content{&mcp.ImageContent{Data: stubPNG, MIMEType: "image/png"}}}, nil
			}
		case "desktop_input":
			sp.Handler = func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
				return &spec.Result{Data: map[string]any{"ok": true}}, nil
			}
		}
	}
	return specs
}

var stubPNG = func() []byte {
	dir, err := os.MkdirTemp("", "tooleval-png")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "s.png")
	_ = bridgetest.WritePNG(p, 64)
	b, _ := os.ReadFile(p)
	return b
}()
