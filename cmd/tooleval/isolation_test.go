package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// The isolation baseline: untracked files other than build output; what an agent adds
// afterwards is new, and new sources call for a rebuild.
func TestUntrackedBaselineAndNewFiles(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(p string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write("Game.uproject")
	run("add", ".")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")
	write("Content/UI/WBP_Old.uasset") // untracked before the eval: kept
	write("Saved/Logs/Game.log")       // build output: never counted
	base, err := untrackedFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 1 || !base["Content/UI/WBP_Old.uasset"] {
		t.Fatalf("baseline = %v", base)
	}
	write("Content/UI/WBP_New.uasset")
	write("Source/Game/NewWidget.cpp")
	write("Intermediate/Build/x.obj")
	extra, err := newUntracked(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(extra)
	if len(extra) != 2 || extra[0] != "Content/UI/WBP_New.uasset" || extra[1] != "Source/Game/NewWidget.cpp" {
		t.Fatalf("new = %v", extra)
	}
	if !touchesSource(extra) || touchesSource([]string{"Content/UI/WBP_New.uasset"}) {
		t.Fatal("touchesSource")
	}
	if !rebuildRequired(map[string]any{"result": map[string]any{"rebuild_required": true}}) || rebuildRequired(map[string]any{}) {
		t.Fatal("rebuildRequired")
	}
}
