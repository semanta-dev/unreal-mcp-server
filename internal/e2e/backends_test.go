package e2e

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
)

// T1 scenarios that run over BOTH dispatch backends (plan §3.1): the uexec stdout
// marker and the native framed channel must give agents identical results — values,
// error codes and error details.
func TestBothBackends(t *testing.T) {
	for _, backend := range []struct {
		name   string
		native bool
	}{{"uexec", false}, {"native", true}} {
		t.Run(backend.name, func(t *testing.T) {
			t.Run("lifecycle", func(t *testing.T) {
				h := startHarness(t, harnessOpts{native: backend.native})
				h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "A"})
				if list := structured(t, h.call(t, "actor_query", map[string]any{"op": "list"})); list["count"] != 1.0 {
					t.Fatalf("list = %v", list)
				}
				if res := h.call(t, "actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": "A"}); res.IsError {
					t.Fatalf("delete: %s", text(res))
				}
				if backend.native && h.emu.NativeCalls() < 3 {
					t.Fatalf("ops did not go over the native backend (%d)", h.emu.NativeCalls())
				}
			})
			t.Run("conflict details survive", func(t *testing.T) {
				h := startHarness(t, harnessOpts{native: backend.native})
				h.emu.Handle("actor_query", func(map[string]any) (any, *bridgetest.OpError) {
					return nil, &bridgetest.OpError{Code: "CONFLICT", Message: "2 actors are labeled Twin",
						Details: map[string]any{"candidates": []any{"/L.L:P.Twin_1", "/L.L:P.Twin_2"}}}
				})
				e := errorOf(t, h.call(t, "actor_query", map[string]any{"op": "get", "actor": "Twin"}))
				d, _ := e["details"].(map[string]any)
				if e["code"] != "CONFLICT" || d == nil || len(d["candidates"].([]any)) != 2 {
					t.Fatalf("conflict = %v", e)
				}
			})
			t.Run("unknown op", func(t *testing.T) {
				h := startHarness(t, harnessOpts{native: backend.native})
				if e := errorOf(t, h.call(t, "viewport", map[string]any{"op": "get"})); e["code"] != "UNKNOWN_OP" {
					t.Fatalf("code = %v", e["code"])
				}
			})
			t.Run("retryable editor error", func(t *testing.T) {
				h := startHarness(t, harnessOpts{native: backend.native})
				h.emu.Handle("editor_ping", func(map[string]any) (any, *bridgetest.OpError) {
					return nil, &bridgetest.OpError{Code: "EDITOR_BUSY", Message: "a modal dialog is open", Retryable: true}
				})
				if e := errorOf(t, h.call(t, "editor", map[string]any{"op": "ping"})); e["code"] != "EDITOR_BUSY" || e["retryable"] != true {
					t.Fatalf("busy = %v", e)
				}
			})
		})
	}
}
