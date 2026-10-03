package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
)

// Found live (P7): a cold start longer than the attach call's deadline was cancelled
// and its editor killed. Now the call returns ErrEditorStarting and attaching again
// resumes the same start.
func TestAttachOutlivesTheCallDeadline(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	sp.gate = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := dm.Router.Attach(ctx, "s", "/A"); !errors.Is(err, session.ErrEditorStarting) {
		t.Fatalf("want ErrEditorStarting, got %v", err)
	}
	close(sp.gate) // the editor finishes starting
	id, err := dm.Router.Attach(context.Background(), "s", "/A")
	if err != nil || id == "" {
		t.Fatalf("re-attach should bind the same start: id=%q err=%v", id, err)
	}
	if sp.pid != 1 || len(sp.kills) != 0 {
		t.Fatalf("exactly one editor, never killed: spawned=%d kills=%v", sp.pid, sp.kills)
	}
}

func TestAbandonedColdStartStaysWarm(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	sp.gate = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _ = dm.Router.Attach(ctx, "gone", "/A")
	dm.Router.Release("gone") // the session ended while its editor was still loading
	close(sp.gate)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if l := dm.Pool.List(); len(l) == 1 && l[0].State == supervisor.Idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the abandoned editor should come up warm and unleased: %+v", dm.Pool.List())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id, err := dm.Router.Attach(context.Background(), "next", "/A"); err != nil || id == "" || sp.pid != 1 {
		t.Fatalf("the next session should lease the warm editor: id=%q err=%v spawned=%d", id, err, sp.pid)
	}
}
