package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/build"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/logtail"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

type buildCompileIn struct {
	Strategy string `json:"strategy,omitempty" jsonschema:"auto|livecoding|full (default auto: classify from the git diff)"`
}
type jobStartOut struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}
type jobIDIn struct {
	JobID string `json:"job_id"`
}

func registerBuildTools(s *mcp.Server, d Deps) {
	add(s, "build_compile",
		"Compile the project's C++ (async job). strategy=auto classifies from the git diff: header/reflection/new-file changes -> full Build.bat rebuild (closes+reopens the editor); body-only changes -> Live Coding. Poll job_status for streamed progress and structured diagnostics.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in buildCompileIn) (*mcp.CallToolResult, jobStartOut, error) {
			rd := resolveDeps(ctx, d)
			if rd.ProjectDir == "" || rd.EngineDir == "" {
				return nil, jobStartOut{}, errors.New("build_compile requires -project and -engine")
			}
			job := rd.Jobs.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
				return runBuildCompile(jctx, rd, in.Strategy, progress)
			})
			return nil, jobStartOut{JobID: job.ID, Status: string(jobs.Running)}, nil
		})

	add(s, "job_status", "Poll an async job (build_compile, editor_restart, project_ensure_open) for status, streamed progress, and result.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in jobIDIn) (*mcp.CallToolResult, map[string]any, error) {
			j, ok := resolveDeps(ctx, d).Jobs.Get(in.JobID)
			if !ok {
				return nil, nil, fmt.Errorf("no such job: %s", in.JobID)
			}
			snap := j.Snapshot()
			return nil, map[string]any{
				"status": string(snap.Status), "progress_lines": snap.Progress,
				"result": snap.Result, "error": snap.Err,
			}, nil
		})

	add(s, "job_cancel", "Request cancellation of an async job.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in jobIDIn) (*mcp.CallToolResult, map[string]any, error) {
			j, ok := resolveDeps(ctx, d).Jobs.Get(in.JobID)
			if !ok {
				return nil, nil, fmt.Errorf("no such job: %s", in.JobID)
			}
			j.Cancel()
			return nil, map[string]any{"cancelled": in.JobID}, nil
		})
}

// runBuildCompile classifies + compiles. Live Coding path stays in-editor; full
// path closes the editor, runs Build.bat, and relaunches. Live-gated (needs the
// editor + engine); the classifier/parser/log-reader beneath it are unit-tested.
func runBuildCompile(ctx context.Context, d Deps, requested string, progress func(string)) (build.Result, error) {
	strat, reason := build.ResolveStrategy(ctx, d.ProjectDir, requested)
	progress(fmt.Sprintf("strategy=%s (%s)", strat, reason))

	if strat == build.StrategyLiveCoding {
		res, escalate := runLiveCoding(ctx, d, progress)
		if !escalate {
			res.Reason = reason
			return res, nil
		}
		progress("Live Coding could not patch (new/renamed symbol); escalating to full rebuild")
		strat = build.StrategyFull
	}

	res, err := runFullRebuild(ctx, d, progress)
	if err != nil {
		return res, err
	}
	res.Reason = reason
	return res, nil
}

// runLiveCoding triggers LiveCoding.Compile and tails the log for the result.
// Returns (result, escalateToFull).
func runLiveCoding(ctx context.Context, d Deps, progress func(string)) (build.Result, bool) {
	logPath := logtail.LogPath(d.ProjectDir)
	offset := logtail.Size(logPath)

	progress("triggering LiveCoding.Compile")
	_, err := d.Bridge.RunPython(ctx, `unreal.SystemLibrary.execute_console_command(None, "LiveCoding.Compile")`, uexec.ModeExecFile)
	if err != nil {
		return build.Result{Strategy: build.StrategyLiveCoding, Success: false, Reason: err.Error()}, false
	}
	if logPath == "" {
		return build.Result{Strategy: build.StrategyLiveCoding, Success: false, Reason: "no project log to read Live Coding result"}, false
	}

	deadline := time.Now().Add(120 * time.Second)
	var acc strings.Builder
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return build.Result{Strategy: build.StrategyLiveCoding, Success: false, Reason: "cancelled"}, false
		case <-time.After(1 * time.Second):
		}
		chunk, newOffset, rerr := logtail.ReadFrom(logPath, offset)
		if rerr != nil {
			continue
		}
		offset = newOffset
		if chunk == "" {
			continue
		}
		acc.WriteString(chunk)
		for _, line := range strings.Split(chunk, "\n") {
			if strings.Contains(line, "LogLiveCoding") {
				progress(strings.TrimSpace(line))
			}
		}
		text := acc.String()
		low := strings.ToLower(text)
		switch {
		case strings.Contains(text, "LiveCoding") && strings.Contains(low, "patching complete") ||
			strings.Contains(text, "Live coding succeeded"):
			return build.Result{Strategy: build.StrategyLiveCoding, Success: true, Diagnostics: build.ParseDiagnostics(text)}, false
		case strings.Contains(low, "compile failed") || strings.Contains(low, "live coding failed"):
			ds := build.ParseDiagnostics(text)
			// New/renamed reflected symbols can't be patched -> escalate.
			if strings.Contains(low, "cannot be applied") || strings.Contains(low, "was added") || strings.Contains(low, "new function") {
				return build.Result{Strategy: build.StrategyLiveCoding, Success: false, Diagnostics: ds}, true
			}
			return build.Result{Strategy: build.StrategyLiveCoding, Success: false, Diagnostics: ds, RawTail: tailStr(text)}, false
		}
	}
	return build.Result{Strategy: build.StrategyLiveCoding, Success: false, Reason: "Live Coding timed out", RawTail: tailStr(acc.String())}, false
}

// runFullRebuild saves, closes the editor, runs Build.bat, and relaunches +
// reopens the prior map.
func runFullRebuild(ctx context.Context, d Deps, progress func(string)) (build.Result, error) {
	uproject := lifecycle.FindUproject(d.ProjectDir)
	if uproject == "" {
		return build.Result{}, errors.New("no .uproject under -project")
	}
	target := lifecycle.EditorTarget(uproject)

	// Remember the current map, and save while the editor is still up (both paths).
	priorMap := currentLevelPath(ctx, d.Bridge)
	_, _ = d.Bridge.Call(ctx, "save_all", map[string]any{})

	// Daemon: a full rebuild relaunches the editor, so it MUST go through the §3.1
	// controlled restart — otherwise the session's lease would be lost and the new
	// editor orphaned. The controller closes the editor, runs Build.bat with nothing
	// holding the DLL, relaunches with the SAME instance token, and re-pins the lease.
	if d.Restart != nil {
		var res build.Result
		var buildErrored bool
		err := d.Restart(ctx, func(rctx context.Context) error {
			progress("running Build.bat " + target)
			r, berr := build.RunFull(rctx, d.EngineDir, target, uproject, progress)
			res = r
			buildErrored = berr != nil
			return berr // infra failure only; compile errors travel in res, still relaunch
		})
		if err != nil {
			// If the build itself COMPLETED (only the post-build relaunch failed), keep
			// its diagnostics and note the relaunch failure instead of dropping the
			// result — a failed job discards the result, so fold it into a success shape.
			if !buildErrored && res.Strategy != "" {
				res.Reason = strings.TrimSpace(res.Reason + "; editor relaunch failed (lease dropped — re-attach): " + err.Error())
				return res, nil
			}
			return res, err // pre-build (BeginRestart) or infra build failure
		}
		if priorMap != "" {
			progress("editor relaunched (lease preserved); reopen prior map when ready: " + priorMap)
		}
		return res, nil
	}

	// Single-project: close, build, relaunch a fresh editor (no lease to preserve).
	pid := editorPID(ctx, d.Bridge)
	progress("closing the editor for a full rebuild")
	_, _ = d.Bridge.RunPython(ctx, `unreal.SystemLibrary.execute_console_command(None, "quit")`, uexec.ModeExecFile)
	closeEditor(pid, progress)

	progress("running Build.bat " + target)
	res, err := build.RunFull(ctx, d.EngineDir, target, uproject, progress)
	if err != nil {
		return res, err
	}
	if !res.Success {
		progress("build failed; not relaunching (fix errors and retry)")
		return res, nil
	}

	progress("relaunching editor")
	if _, err := lifecycle.Launch(d.EngineDir, uproject, "-nosplash"); err != nil {
		progress("relaunch failed: " + err.Error())
		return res, nil
	}
	if priorMap != "" {
		progress("editor relaunched; reopen prior map when ready: " + priorMap)
	}
	return res, nil
}

func registerLifecycleTools(s *mcp.Server, d Deps) {
	add(s, "project_ensure_open",
		"Ensure the editor is open with the project (launches it if no editor is discovered). Async job: poll job_status; waits up to timeout_s for the editor to answer.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in ensureOpenIn) (*mcp.CallToolResult, jobStartOut, error) {
			rd := resolveDeps(ctx, d)
			if rd.EngineDir == "" {
				return nil, jobStartOut{}, errors.New("project_ensure_open requires -engine")
			}
			job := rd.Jobs.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
				return ensureOpen(jctx, rd, in, progress)
			})
			return nil, jobStartOut{JobID: job.ID, Status: string(jobs.Running)}, nil
		})

	add(s, "editor_restart",
		"Save, quit, and relaunch the editor (async job). Useful after a full rebuild or to recover a wedged editor.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in editorRestartIn) (*mcp.CallToolResult, jobStartOut, error) {
			rd := resolveDeps(ctx, d)
			if rd.EngineDir == "" {
				return nil, jobStartOut{}, errors.New("editor_restart requires -project and -engine")
			}
			job := rd.Jobs.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
				if in.Save {
					_, _ = rd.Bridge.Call(jctx, "save_all", map[string]any{})
				}
				// Daemon: a restart relaunches the editor, so it must go through the
				// §3.1 controlled restart to preserve the lease + track the new editor.
				if rd.Restart != nil {
					progress("controlled restart (lease preserved)")
					if err := rd.Restart(jctx, nil); err != nil {
						return nil, err
					}
					return map[string]any{"restarted": true, "lease_preserved": true}, nil
				}
				// Single-project: close + relaunch a fresh editor.
				pid := editorPID(jctx, rd.Bridge)
				_, _ = rd.Bridge.RunPython(jctx, `unreal.SystemLibrary.execute_console_command(None, "quit")`, uexec.ModeExecFile)
				closeEditor(pid, progress)
				uproject := lifecycle.FindUproject(rd.ProjectDir)
				if uproject == "" {
					return nil, errors.New("no .uproject under -project")
				}
				newPID, err := lifecycle.Launch(rd.EngineDir, uproject, "-nosplash")
				if err != nil {
					return nil, err
				}
				return map[string]any{"editor_pid": newPID}, nil
			})
			return nil, jobStartOut{JobID: job.ID, Status: string(jobs.Running)}, nil
		})
}

type ensureOpenIn struct {
	Uproject string  `json:"uproject,omitempty"`
	Map      string  `json:"map,omitempty"`
	TimeoutS float64 `json:"timeout_s,omitempty" jsonschema:"seconds to wait for the editor to answer; default 300"`
}
type editorRestartIn struct {
	Save     bool    `json:"save,omitempty"`
	TimeoutS float64 `json:"timeout_s,omitempty"`
}

// --- helpers (live-gated) ---

func ensureOpen(ctx context.Context, d Deps, in ensureOpenIn, progress func(string)) (any, error) {
	// Already reachable?
	if _, err := d.Bridge.Call(ctx, "editor_status", map[string]any{}); err == nil {
		progress("editor already open")
		return map[string]any{"launched": false, "ready": true}, nil
	}
	uproject := in.Uproject
	if uproject == "" {
		uproject = lifecycle.FindUproject(d.ProjectDir)
	}
	if uproject == "" {
		return nil, errors.New("no .uproject to open")
	}
	progress("launching editor: " + uproject)
	args := []string{"-nosplash"}
	if in.Map != "" {
		args = append(args, in.Map)
	}
	pid, err := lifecycle.Launch(d.EngineDir, uproject, args...)
	if err != nil {
		return nil, err
	}
	timeout := 300 * time.Second
	if in.TimeoutS > 0 {
		timeout = time.Duration(in.TimeoutS * float64(time.Second))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		if _, err := d.Bridge.Call(ctx, "editor_status", map[string]any{}); err == nil {
			progress("editor ready")
			return map[string]any{"launched": true, "editor_pid": pid, "ready": true}, nil
		}
		progress("waiting for editor to answer...")
	}
	return map[string]any{"launched": true, "editor_pid": pid, "ready": false}, errors.New("editor did not become ready within timeout")
}

func waitForEditorExit(pid int, timeout time.Duration, progress func(string)) {
	if pid <= 0 {
		time.Sleep(3 * time.Second) // best-effort settle
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !lifecycle.IsAlive(pid) {
			progress("editor process exited")
			return
		}
		time.Sleep(1 * time.Second)
	}
	progress("editor did not exit within timeout; continuing")
}

// closeEditor waits briefly for the editor to exit gracefully (the caller has already sent the
// `quit` console command) and then FORCE-KILLS any survivor. `quit` only ends PIE, not the editor
// when no PIE is running, so without this the editor keeps holding the module DLL / Live Coding
// lock and a full rebuild fails ("Unable to build while Live Coding is active"). Making the close
// reliable here removes the need for any out-of-band manual force-kill (whose churn was the root
// of editor/MCP relaunch wedges).
func closeEditor(pid int, progress func(string)) {
	waitForEditorExit(pid, 8*time.Second, progress)
	if pid > 0 && lifecycle.IsAlive(pid) {
		progress(fmt.Sprintf("editor still running after quit; force-killing PID %d to unblock the rebuild", pid))
		if err := lifecycle.Kill(pid); err != nil {
			progress("force-kill failed: " + err.Error())
		}
		waitForEditorExit(pid, 20*time.Second, progress)
	}
}

func editorPID(ctx context.Context, b *bridge.Bridge) int {
	raw, err := b.Call(ctx, "editor_status", map[string]any{})
	if err != nil {
		return 0
	}
	var st struct {
		PID int `json:"editor_pid"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.PID
}

func currentLevelPath(ctx context.Context, b *bridge.Bridge) string {
	raw, err := b.Call(ctx, "editor_status", map[string]any{})
	if err != nil {
		return ""
	}
	var st struct {
		Level string `json:"current_level"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.Level
}

func tailStr(s string) string {
	if len(s) <= 4000 {
		return s
	}
	return "…" + s[len(s)-4000:]
}
