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

	"github.com/jdziat/unreal-mcp-server/internal/gitutil"
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

// TestGitToolsAgainstTempRepo drives the git tools end-to-end through the MCP
// client against a real throwaway repository.
func TestGitToolsAgainstTempRepo(t *testing.T) {
	if !gitutil.Available() {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "t@t")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "commit.gpgsign", "false")
	// seed an initial commit so HEAD exists
	os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644)
	git(t, repo, "add", "seed.txt")
	git(t, repo, "commit", "-q", "-m", "seed")

	d := Deps{ProjectDir: repo}

	// New untracked file shows in status.
	os.WriteFile(filepath.Join(repo, "feature.cpp"), []byte("int x;\n"), 0o644)
	res, err := callToolDeps(t, d, "git_status", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var st gitStatusOut
	decodeStructured(t, res, &st)
	if len(st.Untracked) != 1 || st.Untracked[0] != "feature.cpp" {
		t.Fatalf("expected feature.cpp untracked, got %+v", st)
	}

	// Checkpoint commits it.
	res, err = callToolDeps(t, d, "git_checkpoint", map[string]any{"message": "feat: add feature"})
	if err != nil {
		t.Fatal(err)
	}
	var cp gitCheckpointOut
	decodeStructured(t, res, &cp)
	if len(cp.Commit) < 7 {
		t.Fatalf("bad commit hash: %q", cp.Commit)
	}

	// Status is now clean of the file.
	res, _ = callToolDeps(t, d, "git_status", map[string]any{})
	decodeStructured(t, res, &st)
	if len(st.Untracked) != 0 || len(st.Staged) != 0 {
		t.Fatalf("expected clean status, got %+v", st)
	}

	// Log shows 2 commits.
	res, _ = callToolDeps(t, d, "git_log", map[string]any{"limit": 10})
	var lg gitLogOut
	decodeStructured(t, res, &lg)
	if len(lg.Commits) != 2 || lg.Commits[0].Subject != "feat: add feature" {
		t.Fatalf("log = %+v", lg.Commits)
	}

	// Checkpoint must not stage Saved/Intermediate.
	os.MkdirAll(filepath.Join(repo, "Saved"), 0o755)
	os.WriteFile(filepath.Join(repo, "Saved", "junk.log"), []byte("noise\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("keep\n"), 0o644)
	res, err = callToolDeps(t, d, "git_checkpoint", map[string]any{"message": "chore: keep"})
	if err != nil {
		t.Fatal(err)
	}
	// Saved/ must remain untracked (git reports untracked dirs as "Saved/").
	res, _ = callToolDeps(t, d, "git_status", map[string]any{})
	decodeStructured(t, res, &st)
	foundSaved := false
	for _, u := range st.Untracked {
		if strings.HasPrefix(u, "Saved") {
			foundSaved = true
		}
	}
	if !foundSaved {
		t.Fatalf("Saved/ should be excluded from the checkpoint (still untracked), got %+v", st)
	}
	// ...and keep.txt WAS committed (not in untracked/staged).
	for _, u := range append(append([]string{}, st.Untracked...), st.Staged...) {
		if u == "keep.txt" {
			t.Fatalf("keep.txt should have been committed, but appears in status: %+v", st)
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
