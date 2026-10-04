package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/app"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R1.1–R1.5: the game's own API through game / game_command, object references in
// actor_call, object-path predicates, and long waits as jobs.

const gameAPIJSON = `{"version": 1, "object": "@subsystem:Game.GameAgentSubsystem", "capabilities": "GetCapabilitiesJson",
	"snapshot": "PeekSnapshotJson", "command": "ExecuteCommandJson", "events": "GetEventsSince"}`

// gameProject writes a project whose .umcp.json declares gameAPI (raw JSON).
func gameProject(t *testing.T, gameAPI string, extra string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Game.uproject", `{"Modules":[{"Name":"Game"}]}`)
	write(".umcp.json", fmt.Sprintf(`{"game_api": %s%s}`, gameAPI, extra))
	return dir
}

func TestGameAPIReadsAndIdempotentCommands(t *testing.T) {
	dir := gameProject(t, gameAPIJSON, "")
	h := startHarness(t, harnessOpts{project: dir, toolsets: []spec.Toolset{spec.Game}})
	h.world.StartPIE()
	defer h.world.StopPIE()

	caps := structured(t, h.call(t, "game", map[string]any{"op": "capabilities"}))
	if caps["world_epoch"] != "epoch-1" {
		t.Fatalf("capabilities = %v", caps)
	}
	snap := structured(t, h.call(t, "game", map[string]any{"op": "snapshot"}))
	cursor := snap["events_cursor"]
	first := structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "r1"}))
	again := structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "r1"}))
	if first["accepted"] != true || fmt.Sprint(first) != fmt.Sprint(again) || h.world.Game.Runs != 1 {
		t.Fatalf("a repeated request_id must return the recorded result and run once: %v / %v (runs %d)", first, again, h.world.Game.Runs)
	}
	ev := structured(t, h.call(t, "game", map[string]any{"op": "events", "since": cursor}))
	if evs, _ := ev["events"].([]any); len(evs) != 1 || ev["gap"] != false {
		t.Fatalf("events since the snapshot = %v", ev)
	}

	// The game world restarts (a new PIE session): the server still holds the old epoch.
	h.world.Game.Restart("epoch-2")
	e := errorOf(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "r2"}))
	if d, _ := e["details"].(map[string]any); e["code"] != "PRECONDITION" || d["game_error"] != "dedup_expired" ||
		!strings.Contains(fmt.Sprint(e["hint"]), "NEW request_id") || h.world.Game.Runs != 1 {
		t.Fatalf("a command for the old world = %v (runs %d)", e, h.world.Game.Runs)
	}
	old := structured(t, h.call(t, "game", map[string]any{"op": "events", "since": cursor}))
	if old["gap"] != true || old["reason"] != "world_changed" {
		t.Fatalf("the old world's cursor = %v", old)
	}
	// The refused id must not be reused: it would run in the new world.
	if e := errorOf(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "r2"})); e["code"] != "INVALID_ARGUMENT" || h.world.Game.Runs != 1 {
		t.Fatalf("re-sending a refused request_id = %v (runs %d)", e, h.world.Game.Runs)
	}
	// The server forgot the stale epoch: the next command reads the new world first.
	if res := structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "r3"})); res["accepted"] != true || res["world_epoch"] != "epoch-2" {
		t.Fatalf("a command after re-reading = %v", res)
	}
	if e := errorOf(t, h.call(t, "game_command", map[string]any{"name": "start_wave"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("request_id is required: %v", e)
	}
}

func TestGameAPIConfigErrorsAreLocal(t *testing.T) {
	// The object is not one of the project's modules: only the game toolset is off;
	// gate_policy in the same file still applies.
	dir := gameProject(t, strings.Replace(gameAPIJSON, "Game.GameAgentSubsystem", "Engine.GameInstanceSubsystem", 1), `, "gate_policy": "require"`)
	toolsets, gate, err := app.StartupPolicy(dir, "")
	if err != nil || gate == nil {
		t.Fatalf("a bad game_api must not fail the session or drop the gate: %v %v", err, gate)
	}
	for _, ts := range toolsets {
		if ts == spec.Game {
			t.Fatal("an invalid game_api enabled the game toolset")
		}
	}
	h := startHarness(t, harnessOpts{project: dir, toolsets: append(toolsets, spec.Game), gate: gate})
	h.world.StartPIE()
	defer h.world.StopPIE()
	e := errorOf(t, h.call(t, "game", map[string]any{"op": "snapshot"}))
	if e["code"] != "PRECONDITION" || !strings.Contains(fmt.Sprint(e["message"]), "not one of the project's own modules") {
		t.Fatalf("game with an invalid game_api = %v", e)
	}
	list := structured(t, h.call(t, "toolsets", map[string]any{"op": "list"}))
	found := false
	for _, row := range list["toolsets"].([]any) {
		if r := row.(map[string]any); r["toolset"] == "game" && strings.Contains(fmt.Sprint(r["unavailable"]), "own modules") {
			found = true
		}
	}
	if !found {
		t.Fatalf("toolsets op=list should say why game is unavailable: %v", list)
	}
	// With a valid declaration the startup policy turns the toolset on.
	ok := gameProject(t, gameAPIJSON, "")
	if ts, _, err := app.StartupPolicy(ok, ""); err != nil || len(ts) != 1 || ts[0] != spec.Game {
		t.Fatalf("a valid game_api: %v %v", ts, err)
	}
	for _, bad := range []string{`{"version": 2}`, strings.Replace(gameAPIJSON, `"events"`, `"extra": "x", "events"`, 1)} {
		d := gameProject(t, bad, "")
		if ts, _, err := app.StartupPolicy(d, ""); err != nil || len(ts) != 0 {
			t.Fatalf("game_api %s: %v %v", bad, ts, err)
		}
	}
}

func TestGameCommandIsDeniedUnderRequire(t *testing.T) {
	dir := gameProject(t, gameAPIJSON, `, "gate_policy": "require"`)
	toolsets, gate, _ := app.StartupPolicy(dir, "")
	h := startHarness(t, harnessOpts{project: dir, toolsets: toolsets, gate: gate})
	h.world.StartPIE()
	defer h.world.StopPIE()
	if res := h.call(t, "game", map[string]any{"op": "snapshot"}); res.IsError {
		t.Fatalf("a read is not gated: %s", text(res))
	}
	if res := h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "x"}); !res.IsError || h.world.Game.Runs != 0 {
		t.Fatalf("game_command (exec) under gate_policy require must be refused: %s", text(res))
	}
}

func TestObjectRefsAndObjectPathPredicates(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.StartPIE()
	defer h.world.StopPIE()
	h.world.SetObject("@subsystem:AgentSubsystem", map[string]any{"PeekSnapshotJson": `{"wave_number": 3}`, "Mode": "defend"})
	res := structured(t, h.call(t, "actor_call", map[string]any{"actor": "@subsystem:AgentSubsystem", "function": "PeekSnapshotJson", "parse": "json"}))
	if r, _ := res["result"].(map[string]any); r["wave_number"] != 3.0 {
		t.Fatalf("actor_call parse=json on a subsystem = %v", res)
	}
	if e := errorOf(t, h.call(t, "actor_call", map[string]any{"actor": "@subsystem:AgentSubsystem", "function": "Mode", "parse": "json"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("parse=json on a non-JSON result = %v", e)
	}
	w := structured(t, h.call(t, "pie_wait", map[string]any{
		"predicate": "@subsystem:AgentSubsystem.PeekSnapshotJson().wave_number >= 3 and not @subsystem:AgentSubsystem.PeekSnapshotJson().wave_number > 5", "timeout_s": 2}))
	if w["met"] != true {
		t.Fatalf("object-path predicate = %v", w)
	}
}

func TestLongPieWaitIsAJob(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.StartPIE()
	defer h.world.StopPIE()
	out := structured(t, h.call(t, "pie_wait", map[string]any{"predicate": "counts.Nothing >= 1", "timeout_s": 60}))
	if out["job_id"] == nil {
		t.Fatalf("a 60 s wait should return a job: %v", out)
	}
	if e := errorOf(t, h.call(t, "pie_wait", map[string]any{"predicate": "a >= 1", "timeout_s": 601})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("timeout_s beyond 600 = %v", e)
	}
	h.call(t, "job", map[string]any{"op": "cancel", "job_id": out["job_id"]})
}

// Review R1 #1: a command re-sent after a restart goes to the world it was first sent
// to — the new world refuses it — so it never runs twice, even when the agent read the
// new world in between.
func TestGameCommandResendAfterRestartNeverRunsTwice(t *testing.T) {
	h := startHarness(t, harnessOpts{project: gameProject(t, gameAPIJSON, ""), toolsets: []spec.Toolset{spec.Game}})
	h.world.StartPIE()
	defer h.world.StopPIE()
	if res := structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "u1"})); res["accepted"] != true {
		t.Fatalf("first send = %v", res)
	}
	h.world.Game.Restart("epoch-2")
	structured(t, h.call(t, "game", map[string]any{"op": "snapshot"})) // the server learns epoch-2
	e := errorOf(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "u1"}))
	if d, _ := e["details"].(map[string]any); d["game_error"] != "dedup_expired" || h.world.Game.Runs != 1 {
		t.Fatalf("re-send after a restart = %v (runs %d)", e, h.world.Game.Runs)
	}
	// The current world is still known: a new id runs there at once.
	if res := structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "u2"})); res["world_epoch"] != "epoch-2" {
		t.Fatalf("a new id after the refusal = %v", res)
	}
}

// Review R1 #2: no world_epoch, no command.
func TestGameCommandNeedsAnEpoch(t *testing.T) {
	h := startHarness(t, harnessOpts{project: gameProject(t, gameAPIJSON, ""), toolsets: []spec.Toolset{spec.Game}})
	h.world.StartPIE()
	defer h.world.StopPIE()
	h.world.Game.NoEpoch = true
	e := errorOf(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "n1"}))
	if e["code"] != "OPERATION_FAILED" || !strings.Contains(fmt.Sprint(e["message"]), "world_epoch") || h.world.Game.Runs != 0 {
		t.Fatalf("a game without an epoch = %v (runs %d)", e, h.world.Game.Runs)
	}
}

// Review R1 #9: pie start/stop forget the epoch, so the first command in a world the
// server itself restarted reads it instead of being refused.
func TestPieRestartForgetsTheGameWorld(t *testing.T) {
	h := startHarness(t, harnessOpts{project: gameProject(t, gameAPIJSON, ""), toolsets: []spec.Toolset{spec.Game}})
	h.world.StartPIE()
	structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "p1"}))
	h.call(t, "pie", map[string]any{"op": "stop"})
	h.world.Game.Restart("epoch-2")
	h.call(t, "pie", map[string]any{"op": "start"})
	defer h.world.StopPIE()
	if res := structured(t, h.call(t, "game_command", map[string]any{"name": "start_wave", "request_id": "p2"})); res["accepted"] != true || res["world_epoch"] != "epoch-2" {
		t.Fatalf("the first command after pie start = %v", res)
	}
}

// Review R1 #3/#5: actor_call until reads the call's result, not object paths; the
// /Script/ form of an object path parses.
func TestUntilRefusesObjectPaths(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.StartPIE()
	defer h.world.StopPIE()
	e := errorOf(t, h.call(t, "actor_call", map[string]any{"actor": "@pawn", "function": "GetHealth",
		"until": "@subsystem:/Script/Game.AgentSubsystem.wave >= 1", "timeout_s": 1}))
	if e["code"] != "INVALID_ARGUMENT" || !strings.Contains(fmt.Sprint(e["hint"]), "pie_wait") {
		t.Fatalf("until with an object path = %v", e)
	}
}
