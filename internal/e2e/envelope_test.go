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
	// location must be [x,y,z]; the schema rejects a string before the handler runs.
	e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "location": "far"}))
	if e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("code = %v", e["code"])
	}
	if len(h.emu.Calls()) != 0 {
		t.Fatalf("invalid call reached the editor: %v", h.emu.Calls())
	}
}

func TestEnvelopeUnknownOpFromEditor(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	// viewport op=get is real, but the emulator does not implement viewport_get.
	if e := errorOf(t, h.call(t, "viewport", map[string]any{"op": "get"})); e["code"] != "UNKNOWN_OP" {
		t.Fatalf("code = %v", e["code"])
	}
}

func TestEnvelopePythonException(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.Handle("actor_query", func(map[string]any) (any, *bridgetest.OpError) {
		return nil, &bridgetest.OpError{Code: "EDITOR_ERROR", Message: "boom"}
	})
	e := errorOf(t, h.call(t, "actor_query", map[string]any{"op": "get", "actor": "X"}))
	if e["code"] != "OPERATION_FAILED" {
		t.Fatalf("code = %v", e["code"])
	}
}

func TestEnvelopeTimeoutIsUnknownOutcomeForMutatingCall(t *testing.T) {
	h := startHarness(t, harnessOpts{cfg: func(c *uexec.Config) { c.CommandTimeout = 400 * time.Millisecond }})
	if res := h.call(t, "editor", map[string]any{"op": "status"}); res.IsError { // install the companion first
		t.Fatal(text(res))
	}
	h.emu.Handle("actor_spawn", func(map[string]any) (any, *bridgetest.OpError) {
		time.Sleep(1200 * time.Millisecond) // the editor is still working when the client gives up
		return map[string]any{"label": "late"}, nil
	})
	e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor"}))
	if e["code"] != "TIMEOUT" || e["outcome"] != "unknown" || e["retryable"] != false {
		t.Fatalf("want TIMEOUT/unknown/non-retryable, got %v", e)
	}
}

// TestV1TimeoutSStaysWithHandler: a v1 tool that implements timeout_s itself keeps
// its domain result — the spec layer must not race it with a ctx deadline
// (pie_wait_until reports met:false, not TIMEOUT).
func TestV1TimeoutSStaysWithHandler(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.Handle("pie_observe", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"gamestate": map[string]any{"wave_number": 1}}, nil
	})
	res := h.call(t, "pie_wait_until", map[string]any{"predicate": "gamestate.wave_number >= 2", "timeout_s": 1})
	if res.IsError {
		t.Fatalf("expected the domain negative met:false, got error: %s", text(res))
	}
	if got := structured(t, res)["met"]; got != false {
		t.Fatalf("met = %v, want false", got)
	}
}

// TestNullArgumentsAreAnEmptyObject: "arguments": null must not panic in default
// application.
func TestNullArgumentsAreAnEmptyObject(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	// "arguments": null is an empty object: editor without its required op is an
	// enveloped INVALID_ARGUMENT, never a recovered panic (INTERNAL).
	if e := errorOf(t, h.call(t, "editor", nil)); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("null arguments: %v", e)
	}
}
