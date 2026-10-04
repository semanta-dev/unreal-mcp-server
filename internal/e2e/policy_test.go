package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/app"
)

// TestGatePolicyRequireFailsClosed: a project's .umcp.json applies at startup — its
// toolsets are enabled, and gate_policy "require" refuses destructive/exec ops (there
// is no approval surface yet) while read-only and mutating ops keep working.
func TestGatePolicyRequireFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".umcp.json"), []byte(`{"toolsets":["design"],"gate_policy":"require"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	toolsets, gate, err := app.StartupPolicy(dir, "ui")
	if err != nil || gate == nil || len(toolsets) != 2 {
		t.Fatalf("policy = %v %v %v", toolsets, gate, err)
	}
	h := startHarness(t, harnessOpts{project: dir, toolsets: toolsets, gate: gate})
	if res := h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "A"}); res.IsError {
		t.Fatalf("a mutating op must not be gated: %s", text(res))
	}
	e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": "A"}))
	d, _ := e["details"].(map[string]any)
	if e["code"] != "PRECONDITION" || !strings.Contains(fmt.Sprint(d["reason"]), "no approval surface") {
		t.Fatalf("a destructive op under require must be refused: %v", e)
	}
	if got := h.world.Labels(); len(got) != 1 {
		t.Fatalf("the refused delete ran: %v", got)
	}
	if e := errorOf(t, h.call(t, "python", map[string]any{"op": "run", "code": "1"})); e["code"] != "PRECONDITION" {
		t.Fatalf("an exec op under require must be refused: %v", e)
	}
	if res := h.call(t, "design_audit", map[string]any{"kind": "decision", "input": map[string]any{"points": []any{map[string]any{"t": 1, "available": []any{"a", "b"}, "chosen": "a"}}}}); res.IsError {
		t.Fatalf(".umcp.json toolsets were not applied: %s", text(res))
	}
	if res := h.call(t, "widget_query", map[string]any{"op": "describe"}); res.IsError && strings.Contains(text(res), "toolset") {
		t.Fatalf("-toolsets ui not applied: %s", text(res))
	}
	if _, _, err := app.StartupPolicy(writeUmcp(t, `{"gate_policy":"maybe"}`), ""); err == nil {
		t.Fatal("an invalid gate_policy must be rejected at startup")
	}
}

func writeUmcp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".umcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDaemonRefusesARequireProject: the daemon cannot ask for approval either, so a
// project with gate_policy "require" — or an invalid .umcp.json, which might have meant
// it — is refused before any editor is leased or spawned.
func TestDaemonRefusesARequireProject(t *testing.T) {
	e := startDaemon(t, time.Minute, nil)
	cs := e.connect(t, nil)
	defer cs.Close()
	proj := writeUmcp(t, `{"gate_policy":"require"}`)
	out := callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	errObj, _ := out["error"].(map[string]any)
	if out["_error"] == nil || errObj["code"] != "PRECONDITION" {
		t.Fatalf("attach of a require project = %v", out)
	}
	if leasedBy(e.dm, proj) != "" {
		t.Fatal("a refused attach must not keep the lease")
	}
	for _, body := range []string{`{"gate_policy":"Require"}`, `{not json`} {
		out := callTool(t, cs, "project", map[string]any{"op": "attach", "project": writeUmcp(t, body)})
		if errObj, _ := out["error"].(map[string]any); out["_error"] == nil || errObj["code"] != "PRECONDITION" {
			t.Fatalf("attach with .umcp.json %s = %v", body, out)
		}
	}
	if n := e.sp.spawned(); n != 0 {
		t.Fatalf("refused attaches spawned %d editor(s)", n)
	}
}
