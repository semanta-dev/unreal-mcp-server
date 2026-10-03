package e2e

import (
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// Every error path must yield the structured envelope (plan §2.2 R5).

func TestEnvelopeInvalidArgument(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	// x must be a number; the schema rejects a string before the handler runs.
	e := errorOf(t, h.call(t, "spawn_actor", map[string]any{"class_path": "/Script/Engine.Actor", "x": "far"}))
	if e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("code = %v", e["code"])
	}
	if len(h.emu.Calls()) != 0 {
		t.Fatalf("invalid call reached the editor: %v", h.emu.Calls())
	}
}

func TestEnvelopeUnknownOpFromEditor(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	// get_selection is a real v1 tool whose op the emulator does not implement.
	if e := errorOf(t, h.call(t, "get_selection", nil)); e["code"] != "UNKNOWN_OP" {
		t.Fatalf("code = %v", e["code"])
	}
}

func TestEnvelopePythonException(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.Handle("get_actor", func(map[string]any) (any, *bridgetest.OpError) {
		return nil, &bridgetest.OpError{Code: "EDITOR_ERROR", Message: "boom"}
	})
	e := errorOf(t, h.call(t, "get_actor", map[string]any{"actor_label": "X"}))
	if e["code"] != "OPERATION_FAILED" {
		t.Fatalf("code = %v", e["code"])
	}
}

func TestEnvelopeTimeoutIsUnknownOutcomeForMutatingCall(t *testing.T) {
	h := startHarness(t, harnessOpts{cfg: func(c *uexec.Config) { c.CommandTimeout = 400 * time.Millisecond }})
	if res := h.call(t, "editor_status", nil); res.IsError { // install the companion first
		t.Fatal(text(res))
	}
	h.emu.Handle("spawn_actor", func(map[string]any) (any, *bridgetest.OpError) {
		time.Sleep(1200 * time.Millisecond) // the editor is still working when the client gives up
		return map[string]any{"label": "late"}, nil
	})
	e := errorOf(t, h.call(t, "spawn_actor", map[string]any{"class_path": "/Script/Engine.Actor"}))
	if e["code"] != "TIMEOUT" || e["outcome"] != "unknown" || e["retryable"] != false {
		t.Fatalf("want TIMEOUT/unknown/non-retryable, got %v", e)
	}
}
