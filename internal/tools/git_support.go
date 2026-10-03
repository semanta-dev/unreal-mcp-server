package tools

import (
	"context"
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/build"
)

// Never stage these generated/machine-local trees in a checkpoint.
var gitExcludes = []string{":(exclude)Saved", ":(exclude)Intermediate", ":(exclude)DerivedDataCache"}

// stageExcludes is gitExcludes minus the trees .gitignore already ignores: `git add`
// fails (exit 1, after staging the rest) when a pathspec names an ignored path, even
// an exclude — and a typical UE project ignores all three.
func stageExcludes(ctx context.Context, dir string) []string {
	var out []string
	for _, ex := range gitExcludes {
		name := strings.TrimPrefix(ex, ":(exclude)")
		if _, err := build.Run(ctx, dir, "check-ignore", "-q", "--no-index", name+"/"); err == nil {
			continue // ignored: git skips it anyway
		}
		out = append(out, ex)
	}
	return out
}

type gitStatusOut struct {
	Branch    string   `json:"branch"`
	Staged    []string `json:"staged"`
	Unstaged  []string `json:"unstaged"`
	Untracked []string `json:"untracked"`
}

type gitCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
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
