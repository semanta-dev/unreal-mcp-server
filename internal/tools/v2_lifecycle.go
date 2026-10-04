package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/build"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// v2 editor lifecycle, build and git_revert (OVERHAUL_PLAN.md §2.3 rows 28, 34, 35;
// §2.5 safe shutdown + editor-aware revert).

func lifecycleSpecs() []*spec.Spec {
	return []*spec.Spec{editorLifecycleSpec(), buildSpec(), gitRevertSpec()}
}

// Process control, swappable in tests.
var (
	killPID      = lifecycle.Kill
	pidAlive     = lifecycle.IsAlive
	launchEditor = lifecycle.Launch
	// gracefulQuitWait is how long a graceful quit may take before the kill fallback.
	gracefulQuitWait = 30 * time.Second
)

// --- safe shutdown (§2.5) --------------------------------------------------------

type pkgState struct {
	Dirty  []string        `json:"dirty"`
	Loaded map[string]bool `json:"loaded"`
	PIE    bool            `json:"pie"`
	Map    string          `json:"map"`
	PID    int             `json:"editor_pid"`
}

func packagesState(ctx context.Context, c *spec.Call, pkgs []string) (*pkgState, error) {
	b, err := v2Bridge(c)
	if err != nil {
		return nil, err
	}
	raw, err := b.Call(ctx, "packages_state", map[string]any{"packages": pkgs})
	if err != nil {
		return nil, err
	}
	var st pkgState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decode packages_state: %w", err)
	}
	return &st, nil
}

func dirtyError(dirty []string, what string) *envelope.Error {
	return envelope.New(envelope.Precondition, "%d unsaved package(s) would be lost by %s", len(dirty), what).
		WithDetail("dirty", dirty).
		WithHint("save them (level op=save_all), or pass discard_dirty=true to throw them away")
}

// prepareShutdown is the first half of the safe-shutdown routine: refuse when anything
// is unsaved (unless discard), stop PIE, then re-check the dirty set immediately
// before the editor goes — a package can turn dirty in between (autosave tick, live
// coding, PIE stop). Returns the state at quit time.
func prepareShutdown(ctx context.Context, c *spec.Call, discard bool, what string, progress func(string)) (*pkgState, error) {
	st, err := packagesState(ctx, c, nil)
	if err != nil {
		return nil, err
	}
	if len(st.Dirty) > 0 && !discard {
		return nil, dirtyError(st.Dirty, what)
	}
	if st.PIE {
		progress("stopping PIE")
		forgetGameWorld(c)
		if _, err := v2Op(ctx, c, "pie_stop", nil); err != nil {
			return nil, err
		}
		if err := waitPIE(ctx, c, false, 20*time.Second); err != nil {
			return nil, err
		}
	}
	st2, err := packagesState(ctx, c, nil)
	if err != nil {
		return nil, err
	}
	if len(st2.Dirty) > 0 && !discard {
		return nil, dirtyError(st2.Dirty, what)
	}
	return st2, nil
}

type shutdownReport struct {
	Map          string   `json:"map,omitempty"`
	PID          int      `json:"editor_pid"`
	Graceful     bool     `json:"graceful"`
	Discarded    []string `json:"discarded_dirty,omitempty"`
	KillFallback bool     `json:"kill_fallback,omitempty"`
	DirtyAtKill  []string `json:"dirty_at_kill,omitempty"`
}

// quitGracefully asks a clean editor to exit and waits for its PID. quit_editor
// refuses (PRECONDITION) if anything turned dirty since the re-check: that aborts the
// shutdown — nothing is killed. If the editor has not exited after gracefulQuitWait,
// the dirty set is re-queried: an editor that still answers with unsaved packages is
// left alone (PRECONDITION); otherwise killFallback=true tells the caller to kill it.
func quitGracefully(ctx context.Context, c *spec.Call, pid int, progress func(string)) (killFallback bool, err error) {
	progress("quitting the editor")
	_, qerr := v2Op(ctx, c, "quit_editor", nil)
	var oe *bridge.OpError
	if errors.As(qerr, &oe) && oe.Code == "PRECONDITION" {
		dirty, _ := oe.Details["dirty"].([]any)
		names := make([]string, 0, len(dirty))
		for _, d := range dirty {
			names = append(names, fmt.Sprint(d))
		}
		return false, dirtyError(names, "the shutdown (packages became dirty during it)")
	}
	if qerr != nil {
		progress("quit_editor: " + qerr.Error() + " (expected while the editor exits)")
	}
	exited, werr := waitExit(ctx, pid, gracefulQuitWait)
	if werr != nil {
		// Cancelled mid-shutdown: never escalate to a kill on a cancellation.
		return false, envelope.New(envelope.Cancelled, "the shutdown was cancelled; the editor was not killed")
	}
	if exited {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, envelope.New(envelope.Cancelled, "the shutdown was cancelled; the editor was not killed")
	}
	if st, perr := packagesState(ctx, c, nil); perr == nil && len(st.Dirty) > 0 {
		return false, dirtyError(st.Dirty, "killing the editor that did not quit")
	}
	progress(fmt.Sprintf("the editor did not exit within %s; killing it", gracefulQuitWait))
	return true, nil
}

// closeEditorSafely is the second half (stdio): nothing dirty ⇒ a graceful quit
// (killed only after it hangs with nothing unsaved); dirty + discard ⇒ kill (a
// graceful quit would raise the save-changes modal).
func closeEditorSafely(ctx context.Context, c *spec.Call, st *pkgState, progress func(string)) (shutdownReport, error) {
	rep := shutdownReport{Map: st.Map, PID: st.PID}
	if len(st.Dirty) == 0 {
		rep.Graceful = true
		fallback, err := quitGracefully(ctx, c, st.PID, progress)
		if err != nil {
			return rep, err
		}
		if !fallback {
			return rep, nil
		}
		rep.KillFallback = true
	} else {
		rep.Discarded = st.Dirty
		progress(fmt.Sprintf("discarding %d unsaved package(s) and killing the editor", len(st.Dirty)))
	}
	_ = killPID(st.PID)
	_, _ = waitExit(context.WithoutCancel(ctx), st.PID, 20*time.Second)
	return rep, nil
}

// waitExit waits up to d for pid to exit. A cancelled ctx stops the wait (exited
// false, ctx.Err()).
func waitExit(ctx context.Context, pid int, d time.Duration) (bool, error) {
	if pid <= 0 {
		return true, nil
	}
	deadline := time.Now().Add(d)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := sleepCtx(ctx, 250*time.Millisecond); err != nil {
			return false, err
		}
	}
	return true, nil
}

// waitReady polls editor_ping until the (re)launched editor answers.
func waitReady(ctx context.Context, c *spec.Call, timeout time.Duration, progress func(string)) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := v2Op(ctx, c, "editor_ping", nil); err == nil {
			progress("editor ready")
			return nil
		}
		if time.Now().After(deadline) {
			return envelope.New(envelope.Timeout, "the editor did not answer within %s", timeout)
		}
		progress("waiting for the editor")
		if err := sleepCtx(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// restartWith closes the editor safely, runs step with no editor up, and brings it
// back on the same map (mapOverride, when set, replaces it — "" keeps st.Map; use
// "-" for the default map). Daemon: the controlled restart keeps the session's lease
// and runs the same graceful stop. A failing step never leaves the editor down: it is
// relaunched and the step's error returned.
func restartWith(ctx context.Context, c *spec.Call, st *pkgState, step func(context.Context) error, mapOverride string, progress func(string)) (shutdownReport, error) {
	reopen := st.Map
	if mapOverride == "-" {
		reopen = ""
	} else if mapOverride != "" {
		reopen = mapOverride
	}
	if c.Deps.Restart != nil {
		rep := shutdownReport{Map: reopen, PID: st.PID}
		plan := session.RestartPlan{Build: step, Map: reopen}
		if len(st.Dirty) == 0 {
			rep.Graceful = true
			plan.Stop = func(sctx context.Context) error {
				fallback, err := quitGracefully(sctx, c, st.PID, progress)
				rep.KillFallback = fallback // the controlled restart kills whatever is left
				return err
			}
		} else {
			rep.Discarded = st.Dirty
		}
		progress("controlled restart (lease preserved)")
		return rep, c.Deps.Restart(ctx, plan)
	}
	if c.Deps.EngineDir == "" {
		return shutdownReport{}, envelope.New(envelope.Precondition, "restarting the editor needs the engine directory (-engine / UMCP_ENGINE_DIR)")
	}
	uproject := lifecycle.FindUproject(c.Deps.ProjectDir)
	if uproject == "" {
		return shutdownReport{}, envelope.New(envelope.Precondition, "no .uproject in %s", c.Deps.ProjectDir)
	}
	rep, err := closeEditorSafely(ctx, c, st, progress)
	if err != nil {
		return rep, err // refused: the editor is still up, nothing was changed
	}
	rep.Map = reopen
	var stepErr error
	if step != nil {
		stepErr = step(ctx)
	}
	// The editor is down: bring it back even if the job was cancelled meanwhile.
	up := context.WithoutCancel(ctx)
	args := []string{"-nosplash"}
	if reopen != "" {
		args = append(args, reopen)
	}
	progress("relaunching the editor")
	if _, err := launchEditor(c.Deps.EngineDir, uproject, args...); err != nil {
		return rep, errors.Join(stepErr, fmt.Errorf("relaunch: %w", err))
	}
	if err := waitReady(up, c, 300*time.Second, progress); err != nil {
		return rep, errors.Join(stepErr, err)
	}
	return rep, stepErr
}

// --- editor_lifecycle ------------------------------------------------------------

type editorLifecycleIn struct {
	Op           string  `json:"op" jsonschema:"ensure_open | restart | reclaim"`
	Map          string  `json:"map,omitempty" jsonschema:"ensure_open: level to open on launch"`
	Save         bool    `json:"save,omitempty" jsonschema:"restart: save every dirty package first"`
	DiscardDirty bool    `json:"discard_dirty,omitempty" jsonschema:"restart: THROW AWAY unsaved packages instead of failing with PRECONDITION"`
	TimeoutS     float64 `json:"timeout_s,omitempty" jsonschema:"ensure_open: seconds to wait for the editor to answer (default 300)"`
	WaitS        float64 `json:"wait_s,omitempty" jsonschema:"ensure_open/restart: wait up to this many seconds (max 25) before returning the job"`
}

func editorLifecycleSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "ensure_open", Summary: "launch the editor if none answers", Tier: spec.Mutating, Async: true, Rejects: []string{"save", "discard_dirty"}, Needs: []string{"project", "engine"}},
		{Name: "restart", Summary: "safe shutdown + relaunch on the same map", Tier: spec.Destructive, Async: true, Rejects: []string{"map"},
			Reaches: []string{"packages_state", "pie_stop", "editor_ping", "quit_editor", "save_all"}, Needs: []string{"project", "engine"}},
		// Mutating, not Ephemeral: reclaiming takes the channel away from whichever client holds it.
		{Name: "reclaim", Summary: "retake a command channel another client stole", Tier: spec.Mutating, Idempotent: true, Timeout: sync8,
			Rejects: []string{"map", "save", "discard_dirty", "timeout_s", "wait_s"}, Reaches: []string{"editor_ping"}},
	}
	return &spec.Spec{
		Name: "editor_lifecycle", Title: "Editor process", Toolset: spec.Core, Timeout: sync8, Max: sync28, Ops: ops,
		Description: "Start, restart or reconnect the editor.\n- ensure_open (job): launch it if none answers.\n- restart (job): safe shutdown — PRECONDITION listing unsaved packages (save=true saves, discard_dirty=true drops them), stop PIE, graceful quit (kill after 30 s, reported), relaunch on the same map. Daemon: lease kept; editor calls get retryable EDITOR_BUSY meanwhile. Cancel never kills.\n- reclaim: retake a command channel another client took.",
		Schema:      spec.SchemaFor[editorLifecycleIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"project_ensure_open", "editor_restart"},
		Handler:     editorLifecycle,
	}
}

func editorLifecycle(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	if err := notWhileRestarting(c); err != nil {
		return nil, err
	}
	var in editorLifecycleIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	b, err := v2Bridge(c)
	if err != nil {
		return nil, err
	}
	if c.Op.Name == "reclaim" {
		if !b.Reclaim() {
			return nil, envelope.New(envelope.Unsupported, "this connection has no command channel to reclaim")
		}
		out, err := v2Op(ctx, c, "editor_ping", nil)
		if err != nil {
			return nil, err
		}
		out["reclaimed"] = true
		return &spec.Result{Data: out, Summary: "command channel reclaimed"}, nil
	}
	reg, err := jobsOf(c)
	if err != nil {
		return nil, err
	}
	if c.Op.Name == "ensure_open" {
		j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
			return ensureOpenV2(jctx, c, in, progress)
		})
		return &spec.Result{Job: j}, nil
	}
	// restart: the dirty check runs now, so an unsaved package is an immediate PRECONDITION.
	if in.Save {
		if _, err := v2Op(ctx, c, "save_all", nil); err != nil {
			return nil, err
		}
	}
	st, err := packagesState(ctx, c, nil)
	if err != nil {
		return nil, err
	}
	if len(st.Dirty) > 0 && !in.DiscardDirty {
		return nil, dirtyError(st.Dirty, "a restart")
	}
	j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
		st, err := prepareShutdown(jctx, c, in.DiscardDirty, "a restart", progress)
		if err != nil {
			return nil, err
		}
		rep, err := restartWith(jctx, c, st, nil, "", progress)
		if err != nil {
			return nil, err
		}
		return map[string]any{"restarted": true, "shutdown": rep, "lease_preserved": c.Deps.Restart != nil}, nil
	})
	return &spec.Result{Job: j}, nil
}

func ensureOpenV2(ctx context.Context, c *spec.Call, in editorLifecycleIn, progress func(string)) (any, error) {
	if _, err := v2Op(ctx, c, "editor_ping", nil); err == nil {
		progress("editor already open")
		return map[string]any{"launched": false, "ready": true}, nil
	}
	if c.Deps.EngineDir == "" {
		return nil, envelope.New(envelope.Precondition, "launching the editor needs the engine directory (-engine / UMCP_ENGINE_DIR)")
	}
	uproject := lifecycle.FindUproject(c.Deps.ProjectDir)
	if uproject == "" {
		return nil, envelope.New(envelope.Precondition, "no .uproject in %s", c.Deps.ProjectDir)
	}
	args := []string{"-nosplash"}
	if in.Map != "" {
		args = append(args, in.Map)
	}
	progress("launching " + filepath.Base(uproject))
	pid, err := launchEditor(c.Deps.EngineDir, uproject, args...)
	if err != nil {
		return nil, envelope.New(envelope.OperationFailed, "%v", err)
	}
	if err := waitReady(ctx, c, secs(orDefault(in.TimeoutS, 300)), progress); err != nil {
		return nil, err
	}
	return map[string]any{"launched": true, "editor_pid": pid, "ready": true}, nil
}

// --- build -----------------------------------------------------------------------

type buildIn struct {
	Strategy string  `json:"strategy,omitempty" jsonschema:"auto (default) | livecoding | ubt"`
	WaitS    float64 `json:"wait_s,omitempty" jsonschema:"wait up to this many seconds (max 25) before returning the job"`
}

func buildSpec() *spec.Spec {
	return &spec.Spec{
		Name: "build", Title: "Compile C++", Toolset: spec.Core, Timeout: sync8,
		Ops:         []spec.OpSpec{{Tier: spec.Mutating, Async: true, Reaches: []string{"save_all", "packages_state", "pie_stop", "editor_ping", "quit_editor"}, Needs: []string{"project", "engine"}}},
		Description: "Compile the project's C++ (async job). strategy=auto picks from the git diff: header/reflection/new files → ubt (save, safe editor shutdown, Build.bat, relaunch on the same map); body-only → livecoding (escalates to ubt if it cannot patch). A hung editor: EDITOR_UNREACHABLE. Result: success, strategy, reason, diagnostics.",
		Schema:      spec.SchemaFor[buildIn](map[string][]any{"strategy": {"auto", "livecoding", "ubt"}}),
		Replaces:    []string{"build_compile", "live_coding_compile"},
		Handler: func(_ context.Context, c *spec.Call) (*spec.Result, error) {
			if err := notWhileRestarting(c); err != nil {
				return nil, err
			}
			var in buildIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			dir, err := projectDir(c)
			if err != nil {
				return nil, err
			}
			if c.Deps.EngineDir == "" {
				return nil, envelope.New(envelope.Precondition, "build needs the engine directory (-engine / UMCP_ENGINE_DIR)")
			}
			reg, err := jobsOf(c)
			if err != nil {
				return nil, err
			}
			requested := map[string]string{"": "auto", "auto": "auto", "livecoding": "livecoding", "ubt": "full"}[in.Strategy]
			j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
				return runBuild(jctx, c, dir, requested, progress)
			})
			return &spec.Result{Job: j}, nil
		},
	}
}

func runBuild(ctx context.Context, c *spec.Call, dir, requested string, progress func(string)) (build.Result, error) {
	strat, reason := build.ResolveStrategy(ctx, dir, requested)
	progress(fmt.Sprintf("strategy=%s (%s)", strat, reason))
	if strat == build.StrategyLiveCoding {
		res, escalate := liveCoding(ctx, c, dir, progress)
		if !escalate {
			res.Reason = reason
			return res, nil
		}
		progress("Live Coding cannot patch this change (new/renamed reflected symbol); escalating to a full build")
	}
	uproject := lifecycle.FindUproject(dir)
	if uproject == "" {
		return build.Result{}, envelope.New(envelope.Precondition, "no .uproject in %s", dir)
	}
	target := lifecycle.EditorTarget(uproject)
	var res build.Result
	step := func(sctx context.Context) error {
		progress("running Build.bat " + target)
		r, err := build.RunFull(sctx, c.Deps.EngineDir, target, uproject, progress)
		res = r
		return err // infra failure only; compile errors travel in res
	}
	if _, err := v2Op(ctx, c, "editor_ping", nil); err != nil {
		// Not answering is not the same as not running: an editor stuck in a modal
		// dialog still holds the project's DLLs, and UBT would fail to link (LNK1104).
		if pids := editorPIDsForProject(uproject); len(pids) > 0 {
			return res, envelope.New(envelope.EditorUnreachable,
				"an editor for this project is running (pid %v) but not answering; a full build would fail on its locked binaries", pids).
				WithHint("it may be waiting on a dialog (enable the desktop toolset; desktop_capture op=window shows it), or its " +
					"Python remote execution is off or bound to another client; close it to build")
		}
		progress("no editor running: building directly")
		if err := step(ctx); err != nil {
			return res, err
		}
		res.Reason = reason
		return res, nil
	}
	progress("saving dirty packages before closing the editor")
	if _, err := v2Op(ctx, c, "save_all", nil); err != nil {
		return res, err
	}
	st, err := prepareShutdown(ctx, c, false, "a full rebuild", progress)
	if err != nil {
		return res, err
	}
	if _, err := restartWith(ctx, c, st, step, "", progress); err != nil {
		if res.Strategy != "" {
			// The build itself ran: keep its diagnostics and report the relaunch failure.
			res.Reason = strings.TrimSpace(reason + "; editor relaunch failed: " + err.Error())
			return res, nil
		}
		return res, err
	}
	res.Reason = reason
	return res, nil
}

// liveCoding triggers LiveCoding.Compile and tails the log for the outcome.
// Returns (result, escalateToFull).
func liveCoding(ctx context.Context, c *spec.Call, dir string, progress func(string)) (build.Result, bool) {
	logPath := logs.LogPath(dir)
	offset := logs.LogSize(logPath)
	progress("triggering LiveCoding.Compile")
	if _, err := v2Op(ctx, c, "console", map[string]any{"command": "LiveCoding.Compile", "world": "editor"}); err != nil {
		return build.Result{Strategy: build.StrategyLiveCoding, Reason: err.Error()}, false
	}
	deadline := time.Now().Add(120 * time.Second)
	var acc strings.Builder
	for time.Now().Before(deadline) {
		if sleepCtx(ctx, time.Second) != nil {
			return build.Result{Strategy: build.StrategyLiveCoding, Reason: "cancelled"}, false
		}
		chunk, next, err := logs.ReadFrom(logPath, offset)
		if err != nil || chunk == "" {
			continue
		}
		offset = next
		acc.WriteString(chunk)
		for _, line := range strings.Split(chunk, "\n") {
			if strings.Contains(line, "LogLiveCoding") {
				progress(strings.TrimSpace(line))
			}
		}
		text := acc.String()
		low := strings.ToLower(text)
		switch {
		case strings.Contains(text, "LiveCoding") && strings.Contains(low, "patching complete") || strings.Contains(text, "Live coding succeeded"):
			return build.Result{Strategy: build.StrategyLiveCoding, Success: true, Diagnostics: build.ParseDiagnostics(text)}, false
		case strings.Contains(low, "compile failed") || strings.Contains(low, "live coding failed"):
			ds := build.ParseDiagnostics(text)
			if strings.Contains(low, "cannot be applied") || strings.Contains(low, "was added") || strings.Contains(low, "new function") {
				return build.Result{Strategy: build.StrategyLiveCoding, Diagnostics: ds}, true
			}
			return build.Result{Strategy: build.StrategyLiveCoding, Diagnostics: ds, RawTail: tailStr(text)}, false
		}
	}
	return build.Result{Strategy: build.StrategyLiveCoding, Reason: "Live Coding timed out", RawTail: tailStr(acc.String())}, false
}

func tailStr(s string) string {
	if len(s) <= 4000 {
		return s
	}
	return "…" + s[len(s)-4000:]
}

// --- git_revert (§2.5) -----------------------------------------------------------

type gitRevertIn struct {
	To           string  `json:"to" jsonschema:"a checkpoint: umcp/cp/<n> or just <n> (from git op=checkpoint / op=log)"`
	DiscardDirty bool    `json:"discard_dirty,omitempty" jsonschema:"if the editor must close: THROW AWAY unsaved packages instead of PRECONDITION"`
	DryRun       bool    `json:"dry_run,omitempty" jsonschema:"report what would change (files, editor restart) without doing it"`
	WaitS        float64 `json:"wait_s,omitempty" jsonschema:"wait up to this many seconds (max 25) before returning the job"`
}

func gitRevertSpec() *spec.Spec {
	return &spec.Spec{
		Name: "git_revert", Title: "Revert to a checkpoint", Toolset: spec.Core, Timeout: sync20, Max: sync28,
		Ops:         []spec.OpSpec{{Tier: spec.Destructive, Async: true, Required: []string{"to"}, Reaches: []string{"packages_state", "pie_stop", "editor_ping", "quit_editor"}, Needs: []string{"project"}}},
		Description: "Restore the project's files to a git op=checkpoint (umcp/cp/<n> only, else PRECONDITION): changed files are restored, files added since are deleted, untracked files are kept. History is not rewritten (the revert shows as working-tree changes). If the editor has any of those assets loaded it is closed safely first (PRECONDITION listing unsaved packages unless discard_dirty) and relaunched on the same map; otherwise they are reported possibly_stale. All-or-nothing via a backup in Saved/MCP/revert-backup. rebuild_required means C++ changed: run build.",
		Schema:      spec.SchemaFor[gitRevertIn](nil, "to"),
		Replaces:    []string{"git_revert_to"},
		Handler:     gitRevert,
	}
}

type fileChange struct {
	Status string // git name-status: A (added since the checkpoint), M, D, T ...
	Path   string
}

// contentPackage maps Content/**.uasset|.umap to its long package name (/Game/...,
// or /<Plugin>/... for Plugins/<Plugin>/Content).
func contentPackage(path string) (string, bool) {
	p := filepath.ToSlash(path)
	ext := filepath.Ext(p)
	if ext != ".uasset" && ext != ".umap" {
		return "", false
	}
	p = strings.TrimSuffix(p, ext)
	if rest, ok := strings.CutPrefix(p, "Content/"); ok {
		return "/Game/" + rest, true
	}
	if rest, ok := strings.CutPrefix(p, "Plugins/"); ok {
		if plugin, sub, ok := strings.Cut(rest, "/Content/"); ok {
			return "/" + filepath.Base(plugin) + "/" + sub, true
		}
	}
	return "", false
}

func gitRevert(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	if err := notWhileRestarting(c); err != nil {
		return nil, err
	}
	var in gitRevertIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	dir, err := gitDir(ctx, c)
	if err != nil {
		return nil, err
	}
	tag := in.To
	if !strings.HasPrefix(tag, "umcp/cp/") {
		tag = "umcp/cp/" + tag
	}
	if !cpTag.MatchString(tag) {
		return nil, envelope.New(envelope.Precondition, "git_revert only goes back to checkpoints (umcp/cp/<n>), not %q", in.To).
			WithHint("make one with git op=checkpoint; list them with git op=log")
	}
	if _, err := build.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag+"^{commit}"); err != nil {
		return nil, envelope.New(envelope.NotFound, "no checkpoint %s", tag).WithHint("git op=log lists the checkpoints")
	}
	// Working tree (staged + unstaged) vs the checkpoint, excluding generated trees.
	// --relative: paths relative to the project dir even when it is a subdirectory of
	// the repository (that is where they are removed/checked out from); -z: no quoting.
	out, err := build.Run(ctx, dir, append([]string{"diff", "--name-status", "--no-renames", "--relative", "-z", tag, "--", "."}, gitExcludes...)...)
	if err != nil {
		return nil, gitFail("diff", err)
	}
	var changes []fileChange
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != "" {
			changes = append(changes, fileChange{Status: fields[i][:1], Path: filepath.FromSlash(fields[i+1])})
		}
	}
	untrackedOut, _ := build.Run(ctx, dir, append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--", "."}, gitExcludes...)...)
	untracked := []string{}
	for _, u := range strings.Split(strings.TrimRight(untrackedOut, "\x00"), "\x00") {
		if u != "" {
			untracked = append(untracked, filepath.FromSlash(u))
		}
	}
	var pkgs []string
	rebuild := false
	for _, ch := range changes {
		if pkg, ok := contentPackage(ch.Path); ok {
			pkgs = append(pkgs, pkg)
		}
		p := filepath.ToSlash(ch.Path)
		if strings.HasPrefix(p, "Source/") || strings.HasSuffix(p, ".Build.cs") || strings.HasSuffix(p, ".Target.cs") {
			rebuild = true
		}
	}
	plan := map[string]any{"checkpoint": tag, "changes": len(changes), "rebuild_required": rebuild,
		"untracked_kept": slashAll(untracked)}
	deletedPkgs := map[string]bool{} // packages the revert removes (added since the checkpoint)
	for _, ch := range changes {
		if pkg, ok := contentPackage(ch.Path); ok && ch.Status == "A" {
			deletedPkgs[pkg] = true
		}
	}
	if len(changes) == 0 {
		plan["reverted_files"], plan["editor_restarted"] = []string{}, false
		return &spec.Result{Data: plan, Summary: "already at " + tag}, nil
	}

	// Is the editor up, and does it have any reverted package loaded?
	var st *pkgState
	restart := false
	if _, perr := v2Op(ctx, c, "editor_ping", nil); perr == nil {
		if st, err = packagesState(ctx, c, pkgs); err != nil {
			return nil, err
		}
		var loaded, stale []string
		for _, p := range pkgs {
			if st.Loaded[p] {
				loaded = append(loaded, p)
			} else {
				stale = append(stale, p)
			}
		}
		if len(loaded) > 0 {
			restart = true
			plan["loaded_packages"] = loaded
			// ANY dirty package (reverted or not) would be lost by the restart.
			if len(st.Dirty) > 0 && !in.DiscardDirty {
				return nil, dirtyError(st.Dirty, "closing the editor for the revert").WithDetail("loaded_packages", loaded)
			}
		} else {
			plan["possibly_stale"] = stale // the directory watcher rescans them
		}
	}
	plan["editor_restart"] = restart
	files := make([]string, len(changes))
	for i, ch := range changes {
		files[i] = ch.Status + " " + filepath.ToSlash(ch.Path)
	}
	if in.DryRun {
		plan["files"], plan["dry_run"] = files, true
		return &spec.Result{Data: plan, Summary: fmt.Sprintf("would revert %d files (editor restart: %v)", len(changes), restart)}, nil
	}
	reg, err := jobsOf(c)
	if err != nil {
		return nil, err
	}
	j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
		backup := filepath.Join(dir, "Saved", "MCP", "revert-backup", time.Now().UTC().Format("20060102T150405Z"))
		var reverted, deleted []string
		step := func(sctx context.Context) error {
			var err error
			reverted, deleted, err = revertFiles(sctx, dir, tag, changes, backup, progress)
			return err
		}
		res := map[string]any{"checkpoint": tag, "rebuild_required": rebuild, "untracked_kept": plan["untracked_kept"],
			"backup_dir": filepath.ToSlash(backup), "editor_restarted": restart}
		if v, ok := plan["possibly_stale"]; ok {
			res["possibly_stale"] = v
		}
		if restart {
			st, err := prepareShutdown(jctx, c, in.DiscardDirty, "closing the editor for the revert", progress)
			if err != nil {
				return nil, err
			}
			reopen := ""
			if deletedPkgs[st.Map] {
				reopen = "-" // the open map did not exist at the checkpoint: relaunch on the default map
			}
			rep, err := restartWith(jctx, c, st, step, reopen, progress)
			res["shutdown"], res["discarded_dirty"] = rep, rep.Discarded
			if err != nil {
				return nil, err
			}
		} else if err := step(jctx); err != nil {
			return nil, err
		}
		res["reverted_files"], res["deleted_files"] = slashAll(reverted), slashAll(deleted)
		if rebuild {
			res["hint"] = "C++ changed: run build"
		}
		return res, nil
	})
	return &spec.Result{Job: j}, nil
}

// revertFiles backs up the current version of every changed path, then restores the
// checkpoint's versions (git checkout) and deletes files added since. On any failure
// the backup is put back: all-or-nothing.
func revertFiles(ctx context.Context, dir, tag string, changes []fileChange, backup string, progress func(string)) (reverted, deleted []string, err error) {
	existed := map[string]bool{}
	for _, ch := range changes {
		src := filepath.Join(dir, ch.Path)
		if _, serr := os.Stat(src); serr == nil {
			existed[ch.Path] = true
			if cerr := copyFile(src, filepath.Join(backup, ch.Path)); cerr != nil {
				return nil, nil, fmt.Errorf("backup %s: %w", ch.Path, cerr)
			}
		}
	}
	progress(fmt.Sprintf("backed up %d files to %s", len(existed), filepath.ToSlash(backup)))
	rollback := func(cause error) error {
		for _, ch := range changes {
			dst := filepath.Join(dir, ch.Path)
			if existed[ch.Path] {
				_ = copyFile(filepath.Join(backup, ch.Path), dst)
			} else {
				_ = os.Remove(dst) // restored by the checkout, did not exist before
			}
		}
		return fmt.Errorf("revert failed and was rolled back from %s: %w", backup, cause)
	}
	var restore []string
	for _, ch := range changes {
		if ch.Status == "A" {
			if rerr := os.Remove(filepath.Join(dir, ch.Path)); rerr != nil && !os.IsNotExist(rerr) {
				return nil, nil, rollback(rerr)
			}
			deleted = append(deleted, ch.Path)
		} else {
			restore = append(restore, ch.Path)
		}
	}
	for i := 0; i < len(restore); i += 100 { // bounded command lines
		batch := restore[i:min(i+100, len(restore))]
		if _, gerr := build.Run(ctx, dir, append([]string{"checkout", tag, "--"}, batch...)...); gerr != nil {
			return nil, nil, rollback(gerr)
		}
	}
	// The index may still hold changes staged since the checkpoint for deleted paths.
	// (Never with an empty list: "git reset -q --" alone would reset the whole index.)
	if len(deleted) > 0 {
		_, _ = build.Run(ctx, dir, append([]string{"reset", "-q", "--"}, deleted...)...)
	}
	sort.Strings(restore)
	sort.Strings(deleted)
	return restore, deleted, nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// editorPIDsForProject finds running editors of a project (a var for tests).
var editorPIDsForProject = lifecycle.EditorPIDsForProject

// slashAll reports paths with forward slashes (as git and the rest of the API do), never nil.
func slashAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.ToSlash(p)
	}
	return out
}
