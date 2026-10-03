package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
)

// Found live (P7): attaching a project already open in a hand-run editor launched a
// second editor, and the lease's channel then bound the first by project path.
func TestSpawnRefusesNextToAForeignEditor(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "Game.uproject"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := foreignEditors
	t.Cleanup(func() { foreignEditors = old })
	foreignEditors = func(string) []int { return []int{4242} }
	rs := NewRecordStore(t.TempDir(), slog.Default())
	s := &ProcessSpawner{Records: rs, EngineDir: "unused", Logger: slog.Default()}
	_, pid, _, err := s.Spawn(context.Background(), proj, "tok")
	if !errors.Is(err, lifecycle.ErrForeignEditor) || pid != 0 {
		t.Fatalf("want ErrForeignEditor and no launch, got pid=%d err=%v", pid, err)
	}
	if recs := rs.Read(); len(recs) != 0 {
		t.Fatalf("the write-ahead intent must be removed: %+v", recs)
	}
}

// Gate findings (P7): a spawn binds only the editor it launched; a reply without a pid
// is never accepted unverified.
func TestIdentifyNode(t *testing.T) {
	for _, c := range []struct {
		reported, launched int
		want               nodeIdentity
	}{{4242, 4242, nodeIsOurs}, {999, 4242, nodeIsForeign}, {0, 4242, nodeUnverified}} {
		if got := identifyNode(c.reported, c.launched); got != c.want {
			t.Errorf("identifyNode(%d, %d) = %v, want %v", c.reported, c.launched, got, c.want)
		}
	}
	if statusPID([]byte(`{"editor_pid": 77}`)) != 77 || statusPID([]byte(`{}`)) != 0 {
		t.Fatal("statusPID")
	}
}

func TestSpawnNeverLaunchesOnACancelledContext(t *testing.T) {
	old := foreignEditors
	t.Cleanup(func() { foreignEditors = old })
	foreignEditors = func(string) []int { t.Fatal("must not get as far as checking for editors"); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &ProcessSpawner{Records: NewRecordStore(t.TempDir(), slog.Default()), EngineDir: "unused"}
	if _, pid, _, err := s.Spawn(ctx, t.TempDir(), "tok"); err == nil || pid != 0 {
		t.Fatalf("want an error and no launch, got pid=%d err=%v", pid, err)
	}
}
