package tools

import (
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
	var g modalGuard
	e := g.check(42)
	if e == nil || e.Code != envelope.Precondition || e.Details["modal"] != bpErrorsDialog {
		t.Fatalf("want PRECONDITION naming the dialog, got %+v", e)
	}
	if len(*closed) != 1 || (*closed)[0] != 7 {
		t.Fatalf("the dialog should be cancelled once: %v", *closed)
	}
}

func TestModalGuardReportsOtherDialogsOnlyWhenTheyStay(t *testing.T) {
	closed := stubDialogs(t, []desktop.Window{{HWND: 9, PID: 42, Title: "Message Log"}})
	var g modalGuard
	for i := 0; i < 2; i++ {
		if e := g.check(42); e != nil {
			t.Fatalf("check %d: a window seen once or twice may be progress UI: %+v", i, e)
		}
	}
	e := g.check(42)
	if e == nil || e.Code != envelope.Precondition {
		t.Fatalf("a dialog that stays should be reported: %+v", e)
	}
	if len(*closed) != 0 {
		t.Fatalf("an unknown dialog must never be touched: %v", *closed)
	}
	if e := (&modalGuard{}).check(0); e != nil {
		t.Fatalf("no pid, no guess: %+v", e)
	}
}
