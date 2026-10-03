package tools

import (
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/desktop"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
)

func stubDialogs(t *testing.T, wins []desktop.Window) *[]uintptr {
	t.Helper()
	oldList, oldClose := editorDialogs, closeWindow
	t.Cleanup(func() { editorDialogs, closeWindow = oldList, oldClose })
	closed := &[]uintptr{}
	editorDialogs = func(pid int) []desktop.Window {
		if pid != 42 {
			return nil
		}
		return wins
	}
	closeWindow = func(h uintptr) error { *closed = append(*closed, h); return nil }
	return closed
}

// Found live (P7): PIE's "Blueprint Compilation Errors" dialog held the game thread,
// so pie op=start waited out its deadline with the editor frozen until a human came.
func TestModalGuardCancelsTheBlueprintErrorsDialog(t *testing.T) {
	closed := stubDialogs(t, []desktop.Window{{HWND: 7, PID: 42, Title: bpErrorsDialog}})
	e := cancelPIEBlueprintDialog(42, false)
	if e == nil || e.Code != envelope.Precondition || e.Details["modal"] != bpErrorsDialog {
		t.Fatalf("want PRECONDITION naming the dialog, got %+v", e)
	}
	if len(*closed) != 1 || (*closed)[0] != 7 {
		t.Fatalf("the dialog should be cancelled once: %v", *closed)
	}
}

// Other windows of the editor (progress, a docked-out tab, an unknown dialog) are
// never touched and never turn a slow start into an early failure.
func TestModalGuardLeavesOtherWindowsAlone(t *testing.T) {
	closed := stubDialogs(t, []desktop.Window{{HWND: 9, PID: 42, Title: "Message Log"}})
	for i := 0; i < 5; i++ {
		if e := cancelPIEBlueprintDialog(42, false); e != nil {
			t.Fatalf("an unknown window must not fail PIE start: %+v", e)
		}
	}
	if len(*closed) != 0 {
		t.Fatalf("an unknown dialog must never be touched: %v", *closed)
	}
	if e := cancelPIEBlueprintDialog(0, false); e != nil {
		t.Fatalf("no pid, no guess: %+v", e)
	}
}

func TestModalGuardHintWhenIgnoreWasAskedWithoutThePlugin(t *testing.T) {
	stubDialogs(t, []desktop.Window{{HWND: 7, PID: 42, Title: bpErrorsDialog}})
	if e := cancelPIEBlueprintDialog(42, true); e == nil || !strings.Contains(e.Hint, "needs a current UnrealMCP plugin") {
		t.Fatalf("the hint must not suggest the flag the caller already passed: %+v", e)
	}
}
