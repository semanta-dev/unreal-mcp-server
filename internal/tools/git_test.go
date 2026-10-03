package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/build"
)

func TestParseStatus(t *testing.T) {
	out := "## main...origin/main\n M Source/A.cpp\nA  Source/B.h\n?? new.txt"
	st := parseStatus(out)
	if st.Branch != "main" {
		t.Fatalf("branch = %q", st.Branch)
	}
	if len(st.Unstaged) != 1 || st.Unstaged[0] != "Source/A.cpp" {
		t.Fatalf("unstaged = %v", st.Unstaged)
	}
	if len(st.Staged) != 1 || st.Staged[0] != "Source/B.h" {
		t.Fatalf("staged = %v", st.Staged)
	}
	if len(st.Untracked) != 1 || st.Untracked[0] != "new.txt" {
		t.Fatalf("untracked = %v", st.Untracked)
	}
}

func TestParseLog(t *testing.T) {
	out := "abc123\x1ffix bug\x1f2026-07-02T10:00:00-07:00\ndef456\x1fadd feature\x1f2026-07-01T09:00:00-07:00"
	commits := parseLog(out)
	if len(commits) != 2 || commits[0].Hash != "abc123" || commits[0].Subject != "fix bug" {
		t.Fatalf("commits = %+v", commits)
	}
}

// callToolDeps registers all tools with the given deps and calls one.
func callToolDeps(t *testing.T, d Deps, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "test"}, nil)
	RegisterAll(srv, d)
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	return cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestGitToolsAgainstTempRepo drives the git tool end-to-end through the MCP client
// against a real throwaway repository.
func TestGitToolsAgainstTempRepo(t *testing.T) {
	if !build.Available() {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "t@t")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "commit.gpgsign", "false")
	git(t, repo, "config", "tag.gpgsign", "false")
	os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644)
	git(t, repo, "add", "seed.txt")
	git(t, repo, "commit", "-q", "-m", "seed")
	d := Deps{ProjectDir: repo}
	call := func(args map[string]any, out any) {
		t.Helper()
		res, err := callToolDeps(t, d, "git", args)
		if err != nil {
			t.Fatal(err)
		}
		decodeStructured(t, res, out)
	}

	os.WriteFile(filepath.Join(repo, "feature.cpp"), []byte("int x;\n"), 0o644)
	var st gitStatusOut
	call(map[string]any{"op": "status"}, &st)
	if len(st.Untracked) != 1 || st.Untracked[0] != "feature.cpp" {
		t.Fatalf("expected feature.cpp untracked, got %+v", st)
	}

	var cp struct {
		Commit    string `json:"commit"`
		Tag       string `json:"tag"`
		Committed bool   `json:"committed"`
	}
	call(map[string]any{"op": "checkpoint", "message": "feat: add feature"}, &cp)
	if len(cp.Commit) < 7 || cp.Tag != "umcp/cp/1" || !cp.Committed {
		t.Fatalf("checkpoint = %+v", cp)
	}
	// Nothing new: the checkpoint tags HEAD without a commit.
	call(map[string]any{"op": "checkpoint", "message": "again"}, &cp)
	if cp.Tag != "umcp/cp/2" || cp.Committed {
		t.Fatalf("empty checkpoint = %+v", cp)
	}

	var lg struct {
		Commits     []gitCommit `json:"commits"`
		Checkpoints []string    `json:"checkpoints"`
	}
	call(map[string]any{"op": "log", "limit": 10}, &lg)
	if len(lg.Commits) != 2 || lg.Commits[0].Subject != "feat: add feature" || len(lg.Checkpoints) != 2 || lg.Checkpoints[0] != "umcp/cp/2" {
		t.Fatalf("log = %+v", lg)
	}

	// A checkpoint never stages Saved/Intermediate.
	os.MkdirAll(filepath.Join(repo, "Saved"), 0o755)
	os.WriteFile(filepath.Join(repo, "Saved", "junk.log"), []byte("noise\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("keep\n"), 0o644)
	call(map[string]any{"op": "checkpoint", "message": "chore: keep"}, &cp)
	call(map[string]any{"op": "status"}, &st)
	foundSaved := false
	for _, u := range st.Untracked {
		foundSaved = foundSaved || strings.HasPrefix(u, "Saved")
	}
	if !foundSaved {
		t.Fatalf("Saved/ must stay out of checkpoints (untracked), got %+v", st)
	}
	for _, u := range append(append([]string{}, st.Untracked...), st.Staged...) {
		if u == "keep.txt" {
			t.Fatalf("keep.txt should have been committed: %+v", st)
		}
	}
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode structured: %v (%s)", err, b)
	}
}
