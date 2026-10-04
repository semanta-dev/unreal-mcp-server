package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/build"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// TestEveryOpIsWired is the plan's §2.4 table test: every (tool, op) runs end to end
// against the emulator with generated arguments. Each call must come back enveloped
// (never INTERNAL — a recovered panic), must not dispatch an op the companion lacks
// (UNKNOWN_OP), and — via the harness — must only dispatch the Python ops its OpSpec
// declares in Reaches.
// expectedErrors are the only cells allowed to fail, with the code they must fail with
// (they need an engine install, or a job/UFUNCTION that does not exist): anything
// else that errors is a regression.
var expectedErrors = map[string]string{
	"build/": "PRECONDITION", "headless/commandlet": "PRECONDITION", "headless/exec": "PRECONDITION",
	"headless/tests": "PRECONDITION", "job/status": "NOT_FOUND", "job/wait": "NOT_FOUND", "job/cancel": "NOT_FOUND",
	"actor_call/": "NOT_FOUND",
}

// expectedJobState is the final state of an async cell's job (default succeeded).
var expectedJobState = map[string]string{
	"editor_lifecycle/restart": "failed", // no engine directory to relaunch with; nothing is closed (PRECONDITION)
}

func TestEveryOpIsWired(t *testing.T) {
	for _, backend := range []struct {
		name   string
		native bool
	}{{"uexec", false}, {"native", true}} {
		t.Run(backend.name, func(t *testing.T) { everyOp(t, backend.native) })
	}
}

func everyOp(t *testing.T, native bool) {
	dir := t.TempDir()
	png := filepath.Join(dir, "frame.png")
	if err := bridgetest.WritePNG(png, 128); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{
		"Game.uproject":          `{"Modules":[{"Name":"Game"}]}`,
		"Saved/Logs/Game.log":    "LogTemp: Warning: hello\n",
		"Config/DefaultGame.ini": "",
	} {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !build.Available() {
		t.Skip("git not on PATH")
	}
	// A real repo with checkpoint umcp/cp/1 for git / git_revert.
	gitRun(t, dir, "init", "-q")
	for _, kv := range [][2]string{{"commit.gpgsign", "false"}, {"tag.gpgsign", "false"}, {"user.email", "t@t"},
		{"user.name", "t"}, {"core.autocrlf", "false"}} {
		gitRun(t, dir, "config", kv[0], kv[1])
	}
	gitRun(t, dir, "add", "Game.uproject")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	gitRun(t, dir, "tag", "-a", "umcp/cp/1", "-m", "seed")

	h := startHarness(t, harnessOpts{project: dir, native: native, toolsets: []spec.Toolset{spec.Design, spec.UI, spec.PolyWorld, spec.Headless, spec.World}})
	installPermissiveOps(h, dir, png)
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Cube"})

	var names []string
	for n := range h.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	cells := 0
	for _, name := range names {
		sp := h.specs[name]
		if strings.HasPrefix(name, "desktop_") || name == "project" {
			continue // desktop drives the real screen/mouse; project needs the daemon (daemon_test)
		}
		for _, op := range sp.Ops {
			op := op
			cell := name + "/" + op.Name
			t.Run(cell, func(t *testing.T) {
				if needsPIE(name, op) {
					h.world.StartPIE()
					defer h.world.StopPIE()
				}
				args := opArgs(sp, op, dir, png)
				res := h.call(t, name, args)
				cells++
				if want, ok := expectedErrors[cell]; ok {
					if e := errorOf(t, res); e["code"] != want {
						t.Fatalf("%s: want %s, got %v", cell, want, e)
					}
					return
				}
				if res.IsError {
					t.Fatalf("%s args=%v → %v", cell, args, structured(t, res)["error"])
				}
				if _, async := args["wait_s"]; async {
					out := structured(t, res)
					want := expectedJobState[cell]
					if want == "" {
						want = "succeeded"
					}
					if out["state"] != want {
						t.Fatalf("%s: job %v, want %s: %v", cell, out["state"], want, out)
					}
				}
			})
		}
	}
	if cells < 120 {
		t.Fatalf("only %d (tool, op) cells ran", cells)
	}
}

func needsPIE(tool string, op spec.OpSpec) bool {
	for _, n := range op.Needs {
		if n == "pie" {
			return true
		}
	}
	return tool == "pie_wait" || (tool == "pie" && op.Name == "stop")
}

// opArgs builds a valid-looking argument set from the op's required params.
func opArgs(sp *spec.Spec, op spec.OpSpec, dir, png string) map[string]any {
	tool := sp.Name
	args := map[string]any{}
	if op.Name != "" {
		args["op"] = op.Name
	}
	sample := map[string]any{
		"actor": "Cube", "actors": []any{"Cube"}, "class": "/Script/Engine.Actor", "label": "X",
		"properties": map[string]any{"health": 1.0}, "function": "GetWave", "command": "stat fps", "code": "1+1",
		"level": "/Game/Maps/L_Test", "asset": "/Game/X", "dest": "/Game/New/" + tool + "_" + op.Name,
		"kind": "blueprint", "row_struct": "/Script/Engine.TableRowBase", "parent": "/Game/M", "tree": map[string]any{"name": "Root"},
		"key": "W", "predicate": "counts.Actor >= 0", "name": "snap1", "message": "cp", "to": "1", "job_id": "j1",
		"toolset": "design", "tag": "A.B", "enum": "EFoo", "files": []any{png}, "scene_id": "arena", "sound": "/Game/S",
		"preset": "studio", "layout": map[string]any{"type": "grid", "count": 2.0, "spacing": 100.0},
		"timeline": []any{}, "rubric": []any{}, "path": png, "baseline": png, "property": "ProjectileClass",
		"session": "s1", "location": []any{0.0, 0.0, 0.0}, "option": 0.0, "building": 0.0,
		"start": []any{0.0, 0.0}, "end": []any{1.0, 1.0}, "center": []any{0.0, 0.0, 0.0}, "point": []any{0.0, 0.0, 0.0},
		"text": "hi", "filter": "Project.Smoke", "commandlet": "ResavePackages", "exec_cmds": []any{"stat fps"},
		"scaffold": map[string]any{"waves": []any{map[string]any{"count": 3.0, "hp": 10.0, "spike": false}}, "base_damage": 5.0, "focus": 1.0, "spread": 1.0, "splash_min": 0.0, "expand_growth": 0.0},
		"seed": map[string]any{"policy": map[string]any{"s": 0.5, "splash": 0.2, "e": 0.3},
			"scaffold": map[string]any{"waves": []any{map[string]any{"count": 3.0, "hp": 10.0, "spike": false}}, "base_damage": 5.0, "focus": 1.0, "spread": 1.0, "splash_min": 0.0, "expand_growth": 0.0}},
		"input": map[string]any{"points": []any{map[string]any{"t": 1, "available": []any{"a", "b"}, "chosen": "a"}}},
	}
	required := append([]string{}, op.Required...)
	if sp.Schema != nil {
		required = append(required, sp.Schema.Required...)
	}
	for _, k := range required {
		if v, ok := sample[k]; ok {
			args[k] = v
		}
	}
	switch tool {
	case "actor_edit":
		args["world"] = "editor"
		if op.Name == "spawn" {
			args["label"] = "Spawned"
		}
		if op.Name == "delete" {
			args["actor"] = "Spawned" // keep Cube for the cells after this one
		}
	case "asset_create":
		args["class"] = "/Script/Engine.Actor"
	case "snapshot", "snapshot_restore":
		args["name"] = "snap1"
	case "logs":
		if op.Name == "since" {
			args["marker"] = "0"
		}
	case "scene":
		if op.Name == "apply" || op.Name == "check" {
			args["json"] = `{"schema":"unreal.scene/v1","scene_id":"arena","actors":[{"label":"hero","kind":"class","class_path":"/Script/Engine.Actor"}]}`
		}
	case "scene_clear":
		if op.Name == "prune" {
			args["json"] = `{"schema":"unreal.scene/v1","scene_id":"arena","actors":[{"label":"hero","kind":"class","class_path":"/Script/Engine.Actor"}]}`
		}
	case "playtest":
		args["json"] = `{"schema":"scenario/v1","name":"smoke","mode":"pie","duration_s":0.1,"interval_s":0.05}`
		args["wait_s"] = 20.0
	case "asset_import":
		if op.Name == "datatable" {
			args["json"] = "[]"
		}
	case "capture":
		if op.Name == "read" || op.Name == "status" || op.Name == "stop" {
			args["session"] = "s1"
		}
		if op.Name == "clear" {
			args["all"] = true
		}
	case "design_audit":
		args["kind"] = "decision"
	case "analyze":
		if op.Name == "perf" {
			csv := filepath.Join(dir, "perf.csv")
			_ = os.WriteFile(csv, []byte("FrameTime\n16.6\n33.4\n"), 0o644)
			args["path"] = csv
		}
	case "build", "git_revert", "headless":
		args["wait_s"] = 5.0
	case "editor_lifecycle":
		if op.Name != "reclaim" {
			args["wait_s"] = 5.0
		}
	case "widget_query":
		if op.Name == "render" {
			args["class"] = "/Script/UMG.UserWidget"
		}
	}
	return args
}

// installPermissiveOps answers every companion op the tools reach with a plausible
// result (the world emulator answers the stateful ones).
func installPermissiveOps(h *harness, dir, png string) {
	rec := &bridgetest.Recorder{Dir: filepath.Join(dir, "Saved", "MCP", "capture"), Frames: 2}
	rec.Install(h.emu)
	(&bridgetest.Packages{}).Install(h.emu, h.world)
	ok := func(v map[string]any) bridgetest.OpFunc {
		return func(map[string]any) (any, *bridgetest.OpError) { return v, nil }
	}
	pngBytes, _ := os.ReadFile(png) // in memory: the git_revert cell may delete the fixture file
	cp := func(name string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, pngBytes, 0o644)
		return p
	}
	for _, py := range []string{"python_recipe", "open_level", "save_all", "set_world_gamemode", "console", "viewport_get", "viewport_set",
		"focus_actors", "asset_info", "asset_query", "asset_deps", "asset_tags", "asset_edit", "import_assets", "asset_reimport",
		"datatable_import", "map_gameplay", "widget_tree", "widget_describe", "widget_compose", "widget_compile", "world_query",
		"instances_count", "pie_input", "audio_capture_start", "audio_capture_stop", "play_test_sound", "scene_apply",
		"scene_clear", "scene_prune", "scene_actors", "design_probe", "capture_poll", "company_status", "company_build",
		"company_select", "company_road", "company_demolish", "apply_level_recipe", "quit_editor"} {
		h.emu.Handle(py, ok(map[string]any{"ok": true, "count": 0.0, "total": 0.0, "actors": []any{}, "capital": 100.0,
			"imported": []any{"/Game/Imported/X"}, "rows": 0.0, "digest": "d", "saved": true, "loaded": true}))
	}
	h.emu.Handle("instances_list", ok(map[string]any{"instances": []any{}, "count": 0.0}))
	h.emu.Handle("scene_bounds", ok(map[string]any{"combined": map[string]any{"origin": []any{0.0, 0.0, 0.0}, "extent": []any{100.0, 100.0, 100.0}}, "count": 1.0}))
	h.emu.Handle("asset_thumbnail", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"thumbnail_path": cp("thumb.png"), "rendered": true, "num_lods": 1.0}, nil
	})
	h.emu.Handle("widget_render", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"ok": true, "path": cp("render.png")}, nil
	})
	h.emu.Handle("take_screenshot", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"file": cp(fmt.Sprintf("shot_%d.png", len(h.emu.Calls())))}, nil
	})
	h.emu.Handle("pie_screenshot", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"file": cp(fmt.Sprintf("pie_%d.png", len(h.emu.Calls())))}, nil
	})
	h.emu.Handle("capture_poses", func(args map[string]any) (any, *bridgetest.OpError) {
		poses, _ := args["poses"].([]any)
		var cells []any
		for i := range poses {
			cp(fmt.Sprintf("p%05d.png", i))
			cells = append(cells, map[string]any{"index": float64(i), "file": fmt.Sprintf("p%05d.png", i), "rotation_pyr": []any{0.0, float64(i), 0.0}})
		}
		return map[string]any{"dir": dir, "cells": cells}, nil
	})
}
