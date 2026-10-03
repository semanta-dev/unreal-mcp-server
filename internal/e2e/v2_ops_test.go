package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/build"
)

// T1 scenarios for the P5d tools: the editor-aware git_revert (§2.5), jobs, playtest
// (the contact sheet reaches the agent through the job) and offline analysis.

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// revertRepo is a project repo with a checkpoint (umcp/cp/1) holding a level and a
// header, then changes since: a modified level, a new asset, a modified header.
func revertRepo(t *testing.T) string {
	t.Helper()
	if !build.Available() {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	gitRun(t, dir, "config", "tag.gpgsign", "false")
	write := func(p, body string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Content/Maps/L_Arena.umap", "v1")
	write("Source/Game/Hero.h", "v1")
	write("Game.uproject", "{}")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "cp")
	gitRun(t, dir, "tag", "-a", "umcp/cp/1", "-m", "cp")
	write("Content/Maps/L_Arena.umap", "v2")
	write("Content/BP/BP_New.uasset", "new")
	gitRun(t, dir, "add", "Content/BP/BP_New.uasset") // staged since the checkpoint
	write("Source/Game/Hero.h", "v2")
	return dir
}

func read(t *testing.T, dir, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestGitRevertWithEditorUpButAssetsNotLoaded(t *testing.T) {
	dir := revertRepo(t)
	h := startHarness(t, harnessOpts{project: dir})
	pk := &bridgetest.Packages{}
	pk.Install(h.emu, h.world)
	plan := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "dry_run": true}))
	if plan["editor_restart"] != false || plan["changes"] != 3.0 || plan["rebuild_required"] != true {
		t.Fatalf("dry run = %v", plan)
	}
	if read(t, dir, "Content/Maps/L_Arena.umap") != "v2" {
		t.Fatal("dry_run changed files")
	}
	out := structured(t, h.call(t, "git_revert", map[string]any{"to": "umcp/cp/1", "wait_s": 20}))
	res, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || res == nil {
		t.Fatalf("revert job = %v", out)
	}
	if read(t, dir, "Content/Maps/L_Arena.umap") != "v1" || read(t, dir, "Source/Game/Hero.h") != "v1" || read(t, dir, "Content/BP/BP_New.uasset") != "<missing>" {
		t.Fatal("files not reverted to the checkpoint")
	}
	if res["editor_restarted"] != false || !strings.Contains(strings.Join(anyStrings(res["possibly_stale"]), ","), "/Game/Maps/L_Arena") {
		t.Fatalf("revert result = %v", res)
	}
	if bk := res["backup_dir"].(string); read(t, bk, "Content/Maps/L_Arena.umap") != "v2" {
		t.Fatalf("the pre-revert version was not backed up in %s", bk)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestGitRevertRefusesToDiscardUnsavedWork(t *testing.T) {
	dir := revertRepo(t)
	h := startHarness(t, harnessOpts{project: dir})
	pk := &bridgetest.Packages{}
	pk.Install(h.emu, h.world)
	// The level is open in the editor and an unrelated new asset is unsaved.
	pk.Set([]string{"/Game/BP/BP_Unsaved"}, "/Game/Maps/L_Arena")
	e := errorOf(t, h.call(t, "git_revert", map[string]any{"to": "1"}))
	d, _ := e["details"].(map[string]any)
	if e["code"] != "PRECONDITION" || !strings.Contains(strings.Join(anyStrings(d["dirty"]), ","), "BP_Unsaved") {
		t.Fatalf("revert with unsaved work = %v", e)
	}
	if read(t, dir, "Content/Maps/L_Arena.umap") != "v2" {
		t.Fatal("a refused revert changed files")
	}
}

func TestGitRevertOnlyToCheckpoints(t *testing.T) {
	dir := revertRepo(t)
	h := startHarness(t, harnessOpts{noEditor: true, project: dir})
	for _, to := range []string{"HEAD~1", "main", "umcp/cp/x"} {
		if e := errorOf(t, h.call(t, "git_revert", map[string]any{"to": to})); e["code"] != "PRECONDITION" {
			t.Fatalf("to=%s: %v", to, e["code"])
		}
	}
	if e := errorOf(t, h.call(t, "git_revert", map[string]any{"to": "9"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("missing checkpoint = %v", e["code"])
	}
	// No editor running: the revert proceeds offline.
	out := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "wait_s": 20}))
	if out["state"] != "succeeded" || read(t, dir, "Source/Game/Hero.h") != "v1" {
		t.Fatalf("offline revert = %v", out)
	}
}

func TestJobTool(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	if e := errorOf(t, h.call(t, "job", map[string]any{"op": "status", "job_id": "j999"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("unknown job = %v", e["code"])
	}
	if e := errorOf(t, h.call(t, "job", map[string]any{"op": "status", "job_id": "j1", "wait_s": 1})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("status with wait_s = %v", e["code"])
	}
	if res := structured(t, h.call(t, "job", map[string]any{"op": "list"})); res["jobs"] != nil {
		t.Fatalf("fresh project has jobs: %v", res)
	}
}

const scenarioJSON = `{"schema":"scenario/v1","name":"smoke","mode":"pie","duration_s":0.2,"interval_s":0.1,
 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`

func TestPlaytestRunReturnsVerdictAndContactSheet(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	res := h.call(t, "playtest", map[string]any{"op": "run", "json": scenarioJSON, "wait_s": 20})
	out := structured(t, res)
	r, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || r == nil || r["verdict"] != "PASS" {
		t.Fatalf("playtest = %v", out)
	}
	img := false
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok && ic.MIMEType == "image/png" && len(ic.Data) > 0 {
			img = true
		}
	}
	if !img {
		t.Fatal("the contact sheet did not reach the agent as image content")
	}
	if e := errorOf(t, h.call(t, "playtest", map[string]any{"op": "run", "json": `{"schema":"scenario/v1"}`})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("invalid scenario = %v", e["code"])
	}
}

func TestAnalyzeOffline(t *testing.T) {
	h := startHarness(t, harnessOpts{noEditor: true, project: t.TempDir()})
	timeline := []any{map[string]any{"index": 0, "t_world": 0, "state": map[string]any{"gamestate": map[string]any{"wave": 1}}},
		map[string]any{"index": 1, "t_world": 1, "state": map[string]any{"gamestate": map[string]any{"wave": 2}}}}
	rubric := []any{map[string]any{"id": "w", "kind": "reached", "path": "gamestate.wave", "params": map[string]any{"value": 2}}}
	if res := structured(t, h.call(t, "analyze", map[string]any{"op": "rubric", "timeline": timeline, "rubric": rubric})); res["verdict"] != "PASS" {
		t.Fatalf("rubric = %v", res)
	}
	if res := structured(t, h.call(t, "analyze", map[string]any{"op": "scenarios"})); len(res["scenarios"].([]any)) != 0 {
		t.Fatalf("scenarios = %v", res)
	}
}

// TestGitRevertProjectInASubdirectory: the project is MyGame/ inside the repository —
// paths must be resolved relative to the project, not the repository root.
func TestGitRevertProjectInASubdirectory(t *testing.T) {
	if !build.Available() {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	gitRun(t, repo, "config", "tag.gpgsign", "false")
	proj := filepath.Join(repo, "My Game")
	write := func(p, body string) {
		full := filepath.Join(proj, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Content/Maps/L_Arena.umap", "v1")
	write("Game.uproject", "{}")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "cp")
	gitRun(t, repo, "tag", "-a", "umcp/cp/1", "-m", "cp")
	write("Content/Maps/L_Arena.umap", "v2")
	write("Content/BP/BP_Ünïcode New.uasset", "new")

	h := startHarness(t, harnessOpts{project: proj})
	pk := &bridgetest.Packages{}
	pk.Install(h.emu, h.world)
	plan := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "dry_run": true}))
	if plan["changes"] != 1.0 { // the untracked new asset is kept, not reverted
		t.Fatalf("dry run = %v", plan)
	}
	out := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "wait_s": 20}))
	if out["state"] != "succeeded" || read(t, proj, "Content/Maps/L_Arena.umap") != "v1" {
		t.Fatalf("nested revert = %v", out)
	}
	if res := out["result"].(map[string]any); !strings.Contains(strings.Join(anyStrings(res["possibly_stale"]), ","), "/Game/Maps/L_Arena") {
		t.Fatalf("package mapping lost for a nested project: %v", res)
	}
	if read(t, proj, "Content/BP/BP_Ünïcode New.uasset") != "new" {
		t.Fatal("an untracked file must be kept")
	}
}
