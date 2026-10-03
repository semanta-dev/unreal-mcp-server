package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/affordances"
)

type affordancesIn struct {
	Tool        string `json:"tool,omitempty" jsonschema:"return just this tool's affordance"`
	OfflineOnly bool   `json:"offline_only,omitempty" jsonschema:"return only tools that need no live editor"`
}

// registerHeadlessTools adds P6: run work in a SEPARATE editor-cmd process (so a
// commandlet/automation batch doesn't tie up the interactive editor's single
// command channel) and the affordance manifest an agent plans against.
func registerHeadlessTools(s *registrar, d Deps) {
	add(s, "affordances",
		"The tool capability manifest: which tools are offline vs need a live editor / PIE / navmesh / the C++ plugin, and which mutate state. Plan against this instead of discovering constraints by failing.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in affordancesIn) (*mcp.CallToolResult, map[string]any, error) {
			if in.Tool != "" {
				a, ok := affordances.ByTool(in.Tool)
				if !ok {
					return nil, map[string]any{"found": false, "tool": in.Tool}, nil
				}
				return nil, map[string]any{"found": true, "affordance": a}, nil
			}
			if in.OfflineOnly {
				return nil, map[string]any{"affordances": affordances.Offline()}, nil
			}
			return nil, map[string]any{"affordances": affordances.Registry()}, nil
		})
}

func uprojectPath(projectDir string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(projectDir, "*.uproject"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no .uproject in %s", projectDir)
	}
	return matches[0], nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// summarizeAutomation pulls the pass/fail tally from UE's LogAutomationController
// result lines ("Test Completed. Result={Passed|Failed}"). Anchored to the exact
// form (no fuzzy "...passed" fallback that could mis-tally incidental log lines).
func summarizeAutomation(stdout string) map[string]any {
	passed, failed := 0, 0
	for _, ln := range strings.Split(stdout, "\n") {
		l := strings.ToLower(ln)
		switch {
		case strings.Contains(l, "test completed. result={passed}"):
			passed++
		case strings.Contains(l, "test completed. result={failed}"):
			failed++
		}
	}
	return map[string]any{"passed": passed, "failed": failed, "ok": failed == 0}
}
