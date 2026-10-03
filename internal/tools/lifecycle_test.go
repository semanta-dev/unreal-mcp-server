package tools

import (
	"os"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
)

// editorSim scripts the editor side of the safe-shutdown routine (§2.5).
type editorSim struct {
	mu        sync.Mutex
	dirty     [][]string // packages_state answers, in order (last one repeats)
	pie       bool
	exited    bool
	exitOnQit bool
	ops       []string
	killed    []int
	launched  [][]string
}

func (s *editorSim) dispatch(op string, args map[string]any) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, op)
	switch op {
	case "packages_state":
		d := s.dirty[0]
		if len(s.dirty) > 1 {
			s.dirty = s.dirty[1:]
		}
		return map[string]any{"dirty": d, "loaded": map[string]any{}, "pie": s.pie, "map": "/Game/Maps/L_Arena", "editor_pid": 4242}
	case "pie_stop":
		s.pie = false
		return map[string]any{"pie": "stopping"}
	case "editor_ping":
		return map[string]any{"ok": true, "pie": s.pie}
	case "quit_editor":
		s.exited = s.exitOnQit
		return map[string]any{"quitting": true}
	case "save_all":
		return map[string]any{"saved": true}
	}
	return map[string]any{}
}

func (s *editorSim) called(op string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.ops {
		if o == op {
			return true
		}
	}
	return false
}

// withProcessStubs swaps the process controls for the test.
func withProcessStubs(t *testing.T, sim *editorSim) {
	t.Helper()
	k, a, l := killPID, pidAlive, launchEditor
	t.Cleanup(func() { killPID, pidAlive, launchEditor = k, a, l })
	killPID = func(pid int) error {
		sim.mu.Lock()
		defer sim.mu.Unlock()
		sim.killed = append(sim.killed, pid)
		sim.exited = true
		return nil
	}
	pidAlive = func(int) bool { sim.mu.Lock(); defer sim.mu.Unlock(); return !sim.exited }
	launchEditor = func(_, uproject string, args ...string) (int, error) {
		sim.mu.Lock()
		defer sim.mu.Unlock()
		sim.launched = append(sim.launched, append([]string{filepath.Base(uproject)}, args...))
		return 5151, nil
	}
}

func lifecycleDepsFail(t *testing.T, sim *editorSim, fail func(string) (string, string, map[string]any)) Deps {
	d := lifecycleDeps(t, sim)
	d.Bridge = bridge.New(&scriptedRunner{dispatch: sim.dispatch, fail: fail}, bridge.Options{})
	return d
}

func lifecycleDeps(t *testing.T, sim *editorSim) Deps {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Game.uproject"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return Deps{Bridge: bridge.New(&scriptedRunner{dispatch: sim.dispatch}, bridge.Options{}), Jobs: jobs.NewRegistry(),
		ProjectDir: dir, EngineDir: t.TempDir()}
}

func restartResult(t *testing.T, d Deps, args map[string]any) map[string]any {
	t.Helper()
	res, err := callToolDeps(t, d, "editor_lifecycle", args)
	if err != nil {
		t.Fatal(err)
	}
	return structuredMap(t, res)
}

func TestRestartRefusesUnsavedWork(t *testing.T) {
	sim := &editorSim{dirty: [][]string{{"/Game/Maps/L_Arena", "/Game/BP/BP_New"}}}
	withProcessStubs(t, sim)
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "restart"})
	e, _ := out["error"].(map[string]any)
	if e == nil || e["code"] != "PRECONDITION" || len(e["details"].(map[string]any)["dirty"].([]any)) != 2 {
		t.Fatalf("restart with unsaved work = %v", out)
	}
	if sim.called("quit_editor") || len(sim.killed) > 0 {
		t.Fatal("a refused restart must not touch the editor")
	}
}

func TestRestartGracefulQuitStopsPIEAndRelaunchesOnTheMap(t *testing.T) {
	sim := &editorSim{dirty: [][]string{{}}, pie: true, exitOnQit: true}
	withProcessStubs(t, sim)
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "restart", "wait_s": 20})
	res, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || res == nil {
		t.Fatalf("restart job = %v", out)
	}
	sd := res["shutdown"].(map[string]any)
	if sd["graceful"] != true || sd["kill_fallback"] == true || len(sim.killed) != 0 {
		t.Fatalf("expected a graceful quit, got %v (killed %v)", sd, sim.killed)
	}
	if !sim.called("pie_stop") {
		t.Fatal("PIE must be stopped before quitting")
	}
	if len(sim.launched) != 1 || sim.launched[0][len(sim.launched[0])-1] != "/Game/Maps/L_Arena" {
		t.Fatalf("relaunch = %v, want the remembered map", sim.launched)
	}
}

func TestRestartKillFallbackIsReported(t *testing.T) {
	sim := &editorSim{dirty: [][]string{{}}, exitOnQit: false} // quit is ignored: a hung editor
	withProcessStubs(t, sim)
	w := gracefulQuitWait
	gracefulQuitWait = 300 * time.Millisecond
	t.Cleanup(func() { gracefulQuitWait = w })
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "restart", "wait_s": 20})
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("restart job = %v", out)
	}
	sd := res["shutdown"].(map[string]any)
	if sd["graceful"] != true || sd["kill_fallback"] != true || len(sim.killed) != 1 {
		t.Fatalf("a hung quit must be killed and reported: %v killed=%v", sd, sim.killed)
	}
}

func TestRestartDiscardKillsWithoutGracefulQuit(t *testing.T) {
	sim := &editorSim{dirty: [][]string{{"/Game/BP/BP_New"}}}
	withProcessStubs(t, sim)
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "restart", "discard_dirty": true, "wait_s": 20})
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("restart job = %v", out)
	}
	sd := res["shutdown"].(map[string]any)
	if sd["graceful"] == true || len(sim.killed) != 1 || sim.killed[0] != 4242 || sim.called("quit_editor") {
		t.Fatalf("discard must kill (a graceful quit would raise the save modal): %v killed=%v", sd, sim.killed)
	}
	if d, _ := sd["discarded_dirty"].([]any); len(d) != 1 {
		t.Fatalf("discarded packages not reported: %v", sd)
	}
}

func TestRestartRechecksDirtyRightBeforeQuitting(t *testing.T) {
	// Clean at the call, clean after PIE... then a package turns dirty (autosave tick).
	sim := &editorSim{dirty: [][]string{{}, {}, {"/Game/Maps/L_Arena"}}, pie: true, exitOnQit: true}
	withProcessStubs(t, sim)
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "restart", "wait_s": 20})
	if out["state"] != "failed" || sim.called("quit_editor") {
		t.Fatalf("a package dirtied mid-shutdown must abort the restart: %v", out)
	}
}

func TestReclaimNeedsAChannel(t *testing.T) {
	sim := &editorSim{dirty: [][]string{{}}}
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "reclaim"})
	if e, _ := out["error"].(map[string]any); e == nil || e["code"] != "UNSUPPORTED" {
		t.Fatalf("reclaim on a runner without a channel = %v", out)
	}
}

func TestRefusedGracefulQuitAbortsWithoutKilling(t *testing.T) {
	// Clean at both checks, but a package turns dirty before quit_editor runs: the
	// companion refuses, and the shutdown must stop there (never the kill fallback).
	sim := &editorSim{dirty: [][]string{{}}, exitOnQit: false}
	withProcessStubs(t, sim)
	d := lifecycleDepsFail(t, sim, func(op string) (string, string, map[string]any) {
		if op == "quit_editor" {
			return "PRECONDITION", "1 unsaved package(s)", map[string]any{"dirty": []any{"/Game/Maps/L_Arena"}}
		}
		return "", "", nil
	})
	out := restartResult(t, d, map[string]any{"op": "restart", "wait_s": 20})
	if out["state"] != "failed" || !strings.Contains(fmt.Sprint(out["error"]), "unsaved") {
		t.Fatalf("a refused quit must fail the restart: %v", out)
	}
	if len(sim.killed) != 0 || len(sim.launched) != 0 {
		t.Fatalf("a refused quit must not kill or relaunch: killed=%v launched=%v", sim.killed, sim.launched)
	}
}

func TestHungQuitWithUnsavedWorkIsNotKilled(t *testing.T) {
	// The quit hangs, and by then the editor reports unsaved work: leave it alone.
	sim := &editorSim{dirty: [][]string{{}, {}, {}, {"/Game/BP/BP_New"}}, exitOnQit: false}
	withProcessStubs(t, sim)
	w := gracefulQuitWait
	gracefulQuitWait = 200 * time.Millisecond
	t.Cleanup(func() { gracefulQuitWait = w })
	out := restartResult(t, lifecycleDeps(t, sim), map[string]any{"op": "restart", "wait_s": 20})
	if out["state"] != "failed" || len(sim.killed) != 0 {
		t.Fatalf("a hung editor with unsaved work must not be killed: %v killed=%v", out, sim.killed)
	}
}
