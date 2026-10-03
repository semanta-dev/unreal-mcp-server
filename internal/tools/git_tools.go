package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/build"
)

var errNoProject = errors.New("no project dir configured (set -project / UMCP_PROJECT_DIR)")

// Never stage these generated/machine-local trees in a checkpoint.
var gitExcludes = []string{":(exclude)Saved", ":(exclude)Intermediate", ":(exclude)DerivedDataCache"}

type gitStatusOut struct {
	Branch    string   `json:"branch"`
	Staged    []string `json:"staged"`
	Unstaged  []string `json:"unstaged"`
	Untracked []string `json:"untracked"`
}

type gitDiffIn struct {
	Paths []string `json:"paths,omitempty" jsonschema:"limit the diff to these paths"`
}
type gitDiffOut struct {
	Diff string `json:"diff"`
}

type gitCheckpointIn struct {
	Message string   `json:"message" jsonschema:"commit message"`
	Paths   []string `json:"paths,omitempty" jsonschema:"paths to stage; default = all tracked+new except Saved/Intermediate/DerivedDataCache"`
}
type gitCheckpointOut struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
}

type gitRevertIn struct {
	Ref string `json:"ref" jsonschema:"commit/ref to hard-reset the working tree to (destructive: discards uncommitted changes)"`
}
type gitLogIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"max commits; default 20"`
}
type gitCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
}
type gitLogOut struct {
	Commits []gitCommit `json:"commits"`
}

func registerGitTools(s *registrar, d Deps) {
	add(s, "git_status", "Show the git working-tree status of the project (branch, staged, unstaged, untracked).",
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, gitStatusOut, error) {
			dir := resolveDeps(ctx, d).ProjectDir
			if dir == "" {
				return nil, gitStatusOut{}, errNoProject
			}
			out, err := build.Run(ctx, dir, "status", "--porcelain=v1", "--branch")
			if err != nil {
				return nil, gitStatusOut{}, err
			}
			return nil, parseStatus(out), nil
		})

	add(s, "git_diff", "Show the git diff of the project working tree, optionally limited to paths.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in gitDiffIn) (*mcp.CallToolResult, gitDiffOut, error) {
			dir := resolveDeps(ctx, d).ProjectDir
			if dir == "" {
				return nil, gitDiffOut{}, errNoProject
			}
			args := []string{"diff"}
			if len(in.Paths) > 0 {
				args = append(append(args, "--"), in.Paths...)
			}
			out, err := build.Run(ctx, dir, args...)
			if err != nil {
				return nil, gitDiffOut{}, err
			}
			return nil, gitDiffOut{Diff: out}, nil
		})

	add(s, "git_checkpoint", "Commit a checkpoint. Stages the given paths (or all tracked+new except Saved/Intermediate/DerivedDataCache) and commits. Never bypasses hooks.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in gitCheckpointIn) (*mcp.CallToolResult, gitCheckpointOut, error) {
			dir := resolveDeps(ctx, d).ProjectDir
			if dir == "" {
				return nil, gitCheckpointOut{}, errNoProject
			}
			if strings.TrimSpace(in.Message) == "" {
				return nil, gitCheckpointOut{}, errors.New("commit message required")
			}
			addArgs := []string{"add", "--"}
			if len(in.Paths) > 0 {
				addArgs = append(addArgs, in.Paths...)
			} else {
				addArgs = append(addArgs, ".")
				addArgs = append(addArgs, gitExcludes...)
			}
			if _, err := build.Run(ctx, dir, addArgs...); err != nil {
				return nil, gitCheckpointOut{}, err
			}
			if _, err := build.Run(ctx, dir, "commit", "-m", in.Message); err != nil {
				return nil, gitCheckpointOut{}, err
			}
			hash, _ := build.Run(ctx, dir, "rev-parse", "HEAD")
			return nil, gitCheckpointOut{Commit: strings.TrimSpace(hash), Message: in.Message}, nil
		})

	add(s, "git_revert_to", "Hard-reset the project working tree to a ref (destructive: discards uncommitted changes). Use to roll back a bad edit in an unattended run.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in gitRevertIn) (*mcp.CallToolResult, gitCheckpointOut, error) {
			dir := resolveDeps(ctx, d).ProjectDir
			if dir == "" {
				return nil, gitCheckpointOut{}, errNoProject
			}
			if strings.TrimSpace(in.Ref) == "" {
				return nil, gitCheckpointOut{}, errors.New("ref required")
			}
			if _, err := build.Run(ctx, dir, "reset", "--hard", in.Ref); err != nil {
				return nil, gitCheckpointOut{}, err
			}
			hash, _ := build.Run(ctx, dir, "rev-parse", "HEAD")
			return nil, gitCheckpointOut{Commit: strings.TrimSpace(hash), Message: "reset --hard " + in.Ref}, nil
		})

	add(s, "git_log", "Show recent commits (hash, subject, date).",
		func(ctx context.Context, _ *mcp.CallToolRequest, in gitLogIn) (*mcp.CallToolResult, gitLogOut, error) {
			dir := resolveDeps(ctx, d).ProjectDir
			if dir == "" {
				return nil, gitLogOut{}, errNoProject
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 20
			}
			out, err := build.Run(ctx, dir, "log", "-n", itoa(limit), "--pretty=format:%H\x1f%s\x1f%cI")
			if err != nil {
				return nil, gitLogOut{}, err
			}
			return nil, gitLogOut{Commits: parseLog(out)}, nil
		})
}

func parseStatus(out string) gitStatusOut {
	st := gitStatusOut{Staged: []string{}, Unstaged: []string{}, Untracked: []string{}}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			br := strings.TrimPrefix(line, "## ")
			if i := strings.IndexAny(br, ".\t "); i >= 0 {
				br = br[:i]
			}
			st.Branch = br
			continue
		}
		if len(line) < 3 {
			continue
		}
		x, y, path := line[0], line[1], strings.TrimSpace(line[3:])
		switch {
		case x == '?' && y == '?':
			st.Untracked = append(st.Untracked, path)
		default:
			if x != ' ' && x != '?' {
				st.Staged = append(st.Staged, path)
			}
			if y != ' ' && y != '?' {
				st.Unstaged = append(st.Unstaged, path)
			}
		}
	}
	return st
}

func parseLog(out string) []gitCommit {
	commits := []gitCommit{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, gitCommit{Hash: parts[0], Subject: parts[1], Date: parts[2]})
	}
	return commits
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
