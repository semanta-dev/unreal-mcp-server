package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/daemon"
)

// deadLiveness reports every PID dead (the editor process crashed).
type deadLiveness struct{}

func (deadLiveness) IsAlive(int) bool    { return false }
func (deadLiveness) Identity(int) string { return "" }

// TestDaemonEditorCrashThenReattach: a leased editor crashes; the reaper drops the
// lease; the holder's next call is a PRECONDITION telling it to re-attach; re-attaching
// spawns a fresh editor and work continues (the daemon's crash → relaunch path).
func TestDaemonEditorCrashThenReattach(t *testing.T) {
	e := startDaemon(t, time.Minute, nil)
	cs := e.connect(t, nil)
	defer cs.Close()
	proj := t.TempDir()
	callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	if st := callTool(t, cs, "editor", map[string]any{"op": "ping"}); st["_error"] != nil {
		t.Fatalf("ping before the crash: %v", st)
	}

	rt := daemon.NewRuntime(e.dm.Router, deadLiveness{}, time.Millisecond, 2*time.Millisecond, nil)
	time.Sleep(10 * time.Millisecond) // older than reapTTL
	if reaped := rt.Tick(); len(reaped) != 1 {
		t.Fatalf("the crashed editor was not reaped: %v", reaped)
	}
	out := callTool(t, cs, "editor", map[string]any{"op": "ping"})
	errObj, _ := out["error"].(map[string]any)
	if out["_error"] == nil || errObj["code"] != "PRECONDITION" || !strings.Contains(errObj["hint"].(string), "op=attach") {
		t.Fatalf("after the crash the holder must be told to re-attach: %v", out)
	}
	callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	if e.sp.spawned() != 2 {
		t.Fatalf("re-attach must spawn a fresh editor (spawned %d)", e.sp.spawned())
	}
	if st := callTool(t, cs, "editor", map[string]any{"op": "ping"}); st["_error"] != nil {
		t.Fatalf("ping after re-attach: %v", st)
	}
}

// TestToolsetsEnableDisableDescribe: toolsets change this session's tool list (with a
// list_changed notification), describe reports needs per op, and the cockpit URL never
// carries the human's token.
func TestToolsetsEnableDisableDescribe(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	if e := errorOf(t, h.call(t, "design_audit", map[string]any{"kind": "decision", "input": map[string]any{}})); e["code"] != "PRECONDITION" ||
		!strings.Contains(e["hint"].(string), "toolset") {
		t.Fatalf("a tool of a disabled toolset = %v", e)
	}
	list := structured(t, h.call(t, "toolsets", map[string]any{"op": "list"}))
	enabled := map[string]bool{}
	for _, x := range list["toolsets"].([]any) {
		m := x.(map[string]any)
		enabled[m["toolset"].(string)] = m["enabled"] == true
	}
	if !enabled["core"] || enabled["design"] {
		t.Fatalf("toolsets = %v", enabled)
	}
	if res := structured(t, h.call(t, "toolsets", map[string]any{"op": "enable", "toolset": "design"})); len(res["enabled"].([]any)) < 2 {
		t.Fatalf("enable = %v", res)
	}
	if res := h.call(t, "design_audit", map[string]any{"kind": "decision", "input": map[string]any{"points": []any{}}}); res.IsError {
		t.Fatalf("design_audit after enable: %s", text(res))
	}
	if e := errorOf(t, h.call(t, "toolsets", map[string]any{"op": "disable", "toolset": "core"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("disabling core = %v", e["code"])
	}
	d := structured(t, h.call(t, "toolsets", map[string]any{"op": "describe", "tool": "audio"}))
	ops := d["ops"].([]any)
	needs := ops[0].(map[string]any)["needs"]
	if !strings.Contains(strings.Join(anyStrings(needs), ","), "pie") || d["tier"] != "ephemeral" {
		t.Fatalf("describe audio = %v", d)
	}
	if e := errorOf(t, h.call(t, "toolsets", map[string]any{"op": "enable", "toolset": "nope"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("unknown toolset = %v", e["code"])
	}
}

// TestToolsetsListChangedReachesTheClient: enabling a toolset notifies the client.
func TestToolsetsListChangedReachesTheClient(t *testing.T) {
	changed := make(chan struct{}, 4)
	h := startHarness(t, harnessOpts{client: &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
		changed <- struct{}{}
	}}})
	h.call(t, "toolsets", map[string]any{"op": "enable", "toolset": "polyworld"})
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no notifications/tools/list_changed after enabling a toolset")
	}
}
