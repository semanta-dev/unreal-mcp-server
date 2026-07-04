package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/affordances"
	"github.com/jdziat/unreal-mcp-server/internal/headless"
)

type headlessRunIn struct {
	Commandlet string   `json:"commandlet,omitempty" jsonschema:"a commandlet to -run, e.g. DataValidation, ResavePackages"`
	ExecCmds   []string `json:"exec_cmds,omitempty" jsonschema:"console commands to batch via -ExecCmds (Quit is appended for you if you include it)"`
	RunTests   string   `json:"run_tests,omitempty" jsonschema:"automation test filter, e.g. Project.Functional (expands to Automation RunTests <filter>;Quit)"`
	TimeoutS   float64  `json:"timeout_s,omitempty" jsonschema:"max seconds before aborting; default 180, max 600"`
	WithGPU    bool     `json:"with_gpu,omitempty" jsonschema:"render with a GPU (default is -nullrhi for pure-logic jobs)"`
}

type affordancesIn struct {
	Tool        string `json:"tool,omitempty" jsonschema:"return just this tool's affordance"`
	OfflineOnly bool   `json:"offline_only,omitempty" jsonschema:"return only tools that need no live editor"`
}

// registerHeadlessTools adds P6: run work in a SEPARATE editor-cmd process (so a
// commandlet/automation batch doesn't tie up the interactive editor's single
// command channel) and the affordance manifest an agent plans against.
func registerHeadlessTools(s *mcp.Server, d Deps) {
	add(s, "headless_run",
		"Run a commandlet, an -ExecCmds batch, or automation tests in a SEPARATE headless UnrealEditor-Cmd process — off the interactive editor's command channel. Blocks up to timeout; returns exit code + log tail + test summary.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in headlessRunIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			uproject, err := uprojectPath(resolveDeps(ctx, d).ProjectDir)
			if err != nil {
				return nil, nil, err
			}
			if in.Commandlet == "" && in.RunTests == "" && len(in.ExecCmds) == 0 {
				return nil, nil, fmt.Errorf("provide commandlet, run_tests, or exec_cmds")
			}
			timeout := 180 * time.Second
			if in.TimeoutS > 0 {
				timeout = secs(in.TimeoutS)
			}
			if timeout > 600*time.Second {
				timeout = 600 * time.Second
			}
			cmd := headless.Cmd{
				Editor:     headless.EditorCmdFromEngineDir(resolveDeps(ctx, d).EngineDir),
				Project:    uproject,
				Commandlet: in.Commandlet,
				ExecCmds:   in.ExecCmds,
				RunTests:   in.RunTests,
				NullRHI:    !in.WithGPU,
				Unattended: true,
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			res, runErr := headless.Run(runCtx, cmd)
			out := map[string]any{
				"exit_code": res.ExitCode,
				"timed_out": res.TimedOut,
				"ok":        res.ExitCode == 0 && !res.TimedOut,
				"log_tail":  lastLines(res.Stdout, 40),
				"cmdline":   res.Cmdline,
			}
			// Surface the launch/exec failure and stderr — otherwise a bad engine
			// path or misconfigured run looks like a silent empty success.
			if runErr != nil {
				out["error"] = runErr.Error()
			}
			if st := strings.TrimSpace(res.Stderr); st != "" {
				out["stderr_tail"] = lastLines(res.Stderr, 20)
			}
			if in.RunTests != "" {
				tests := summarizeAutomation(res.Stdout)
				out["tests"] = tests
				// A clean exit with zero recognized test lines is NOT a pass (bad
				// filter / premature quit / no matching tests).
				if tests["passed"] == 0 && tests["failed"] == 0 {
					out["ok"] = false
					out["warning"] = "no automation test results parsed — check the test filter matched anything"
				} else if failed, _ := tests["failed"].(int); failed > 0 {
					out["ok"] = false
				}
			}
			return nil, out, nil
		})

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
