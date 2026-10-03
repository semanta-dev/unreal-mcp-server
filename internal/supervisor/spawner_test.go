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
