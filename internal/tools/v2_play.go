package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/crash"
	"github.com/jdziat/unreal-mcp-server/internal/desktop"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/scenespec"
	"github.com/jdziat/unreal-mcp-server/internal/snapshot"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

// v2 play / world / snapshot / visual / scene / audio tools (OVERHAUL_PLAN.md §2.3
// rows 16–25, 27; §2.5).

func playSpecs() []*spec.Spec {
	return []*spec.Spec{pieSpec(), pieObserveSpec(), pieWaitSpec(), worldQuerySpec(), snapshotSpec(),
		snapshotRestoreSpec(), screenshotSpec(), captureSpec(), sceneSpec(), sceneClearSpec(), audioSpec()}
}

// pollTimeout is how long a polling loop may run: the requested window (default
// def), cut short so the final answer (met:false) is returned before the call's own
// deadline — keeping a margin of a fifth of the window, at most a second.
func pollTimeout(ctx context.Context, requestedS float64, def time.Duration) time.Duration {
	timeout := def
	if requestedS > 0 {
		timeout = time.Duration(requestedS * float64(time.Second))
	}
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if lim := left - min(time.Second, left/5); lim < timeout {
			timeout = lim
		}
	}
	return timeout
}

// --- pie -------------------------------------------------------------------------

type pieIn struct {
	Op        string    `json:"op" jsonschema:"start | stop | input | cursor | ui_click"`
	Simulate  bool      `json:"simulate,omitempty" jsonschema:"start: Simulate In Editor (the world runs, no player is possessed)"`
	IgnoreBP  bool      `json:"ignore_blueprint_errors,omitempty" jsonschema:"start: play despite Blueprint compile errors (needs the plugin)"`
	Wait      *bool     `json:"wait,omitempty" jsonschema:"start/stop: wait until PIE is actually running/stopped (default true)"`
	Key       string    `json:"key,omitempty" jsonschema:"input: UE key name, e.g. W, SpaceBar, MouseX, Gamepad_LeftX"`
	Action    string    `json:"action,omitempty" jsonschema:"input: tap (default) | press | release | hold | axis | release_all; cursor: move | click (default) | drag | release"`
	Value     *float64  `json:"value,omitempty" jsonschema:"input action=axis: sent every tick (a mouse axis: that frame's delta)"`
	DurationS float64   `json:"duration_s,omitempty" jsonschema:"input hold (default 1) / axis (0.1); cursor drag (0.3): seconds"`
	Position  []float64 `json:"position,omitempty" jsonschema:"cursor: [x, y] viewport pixels (not for release)"`
	To        []float64 `json:"to,omitempty" jsonschema:"cursor action=drag: [x, y] end"`
	Button    string    `json:"button,omitempty" jsonschema:"cursor/ui_click: mouse button (default LeftMouseButton)"`
	Widget    string    `json:"widget,omitempty" jsonschema:"ui_click: name of a widget on screen"`
}

var (
	pieInputOnly  = []string{"key", "value"}
	piePointer    = []string{"position", "to", "button", "widget"}
	pieStartFlags = []string{"simulate", "ignore_blueprint_errors", "wait"}
)

func pieSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "start", Summary: "start Play In Editor (or Simulate)", Tier: spec.Ephemeral, Idempotent: true, Rejects: concat([]string{"action", "duration_s"}, pieInputOnly, piePointer), Timeout: sync28, Reaches: []string{"pie_preflight", "pie_start", "editor_ping"}},
		{Name: "stop", Summary: "stop PIE (game-world changes are discarded)", Tier: spec.Ephemeral, Idempotent: true, Rejects: concat([]string{"simulate", "ignore_blueprint_errors", "action", "duration_s"}, pieInputOnly, piePointer), Reaches: []string{"pie_stop", "editor_ping"}},
		{Name: "input", Summary: "inject a key/button or an analog axis into the running game", Tier: spec.Ephemeral, Rejects: concat(pieStartFlags, piePointer), Reaches: []string{"pie_input", "pie_axis_stats"}, Needs: []string{"pie", "plugin", "plugin>=5 for axis"}},
		{Name: "cursor", Summary: "move/click/drag the game's cursor (viewport pixels); release gives it back", Tier: spec.Ephemeral, Rejects: concat(pieStartFlags, pieInputOnly, []string{"widget"}), Reaches: []string{"pie_cursor"}, Needs: []string{"pie", "plugin>=5"}},
		{Name: "ui_click", Summary: "click a visible live widget by name", Tier: spec.Ephemeral, Required: []string{"widget"}, Rejects: concat(pieStartFlags, pieInputOnly, []string{"action", "duration_s", "position", "to"}), Reaches: []string{"pie_ui_click"}, Needs: []string{"pie", "plugin>=5"}},
	}
	return &spec.Spec{
		Name: "pie", Title: "Play In Editor", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Play In Editor.\n- start (simulate=true: no player); waits until running.\n- stop (pie-world changes are discarded).\n" +
			"- input: tap/press/release/hold `key` like a player; action=axis value=… sends an analog axis every tick for duration_s " +
			"(hold/axis durations are game time: paused, they wait).\n" +
			"- cursor: move/click/drag at position=[x,y] (viewport pixels, to=[x,y]) through Slate — your OS cursor is never moved; " +
			"the game's cursor stays until action=release.\n" +
			"- ui_click widget=name: click a visible widget (refused if hidden, ambiguous or covered). Needs the UnrealMCP plugin.",
		Schema: spec.SchemaFor[pieIn](map[string][]any{"op": spec.OpEnum(ops...),
			"action": {"tap", "press", "release", "hold", "axis", "release_all", "move", "click", "drag"}}, "op"),
		Replaces: []string{"start_play", "stop_play", "pie_input"},
		Handler:  pieHandler,
	}
}

// axisWaitMax is the longest axis hold the input call waits out (a sync call's budget).
const axisWaitMax = 5.0

// awaitAxis waits for a short axis hold to finish and adds what it sent: {ticks, total}
// (axis input is per frame, so its effect is ticks × value, whatever the frame rate).
// A longer hold returns at once with done:false.
func awaitAxis(ctx context.Context, c *spec.Call, key string, durationS float64, out map[string]any) {
	if durationS > axisWaitMax {
		out["done"] = false
		return
	}
	deadline := time.Now().Add(secs(durationS + 2))
	for time.Now().Before(deadline) {
		if sleepCtx(ctx, 50*time.Millisecond) != nil {
			return
		}
		st, err := v2Op(ctx, c, "pie_axis_stats", map[string]any{"key": key})
		if err != nil {
			return
		}
		if st["active"] != true {
			out["done"], out["ticks"], out["total"] = true, st["ticks"], st["total"]
			return
		}
	}
	out["done"] = false
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// pieActions are the actions each op takes (one enum serves input and cursor).
var pieActions = map[string]map[string]bool{
	"input":  {"": true, "tap": true, "press": true, "release": true, "hold": true, "axis": true, "release_all": true},
	"cursor": {"": true, "move": true, "click": true, "drag": true, "release": true},
}

// pieInputHandler runs input / cursor / ui_click (the plugin's control subsystem).
func pieInputHandler(ctx context.Context, c *spec.Call, in pieIn) (*spec.Result, error) {
	if ok := pieActions[c.Op.Name]; ok != nil && !ok[in.Action] {
		return nil, envelope.New(envelope.InvalidArgument, "%s takes action %s, not %q", c.Op.Name,
			map[string]string{"input": "tap | press | release | hold | axis | release_all", "cursor": "move | click | drag | release"}[c.Op.Name], in.Action)
	}
	switch c.Op.Name {
	case "input":
		if (in.Key == "") != (in.Action == "release_all") {
			return nil, envelope.New(envelope.InvalidArgument, "input needs key (except action=release_all, which takes none)")
		}
		if (in.Action == "axis") != (in.Value != nil) {
			return nil, envelope.New(envelope.InvalidArgument, "value goes with action=axis, and action=axis needs value")
		}
		out, err := v2Op(ctx, c, "pie_input", pick(c.Args, "key", "action", "duration_s", "value"))
		if err == nil && in.Action == "axis" {
			awaitAxis(ctx, c, in.Key, orDefault(in.DurationS, 0.1), out)
		}
		return &spec.Result{Data: out, Summary: fmt.Sprintf("%s %s", orStr(in.Action, "tap"), in.Key)}, err
	case "cursor":
		if in.Action == "release" {
			if in.Position != nil || in.To != nil {
				return nil, envelope.New(envelope.InvalidArgument, "action=release takes no position or to")
			}
			out, err := v2Op(ctx, c, "pie_cursor", map[string]any{"action": "release"})
			return &spec.Result{Data: out, Summary: "cursor released"}, err
		}
		if len(in.Position) != 2 || (in.To != nil && len(in.To) != 2) {
			return nil, envelope.New(envelope.InvalidArgument, "position (and to) are [x, y] in viewport pixels")
		}
		if (in.Action == "drag") != (in.To != nil) {
			return nil, envelope.New(envelope.InvalidArgument, "to goes with action=drag, and action=drag needs to")
		}
		out, err := v2Op(ctx, c, "pie_cursor", pick(c.Args, "action", "position", "to", "button", "duration_s"))
		return &spec.Result{Data: out, Summary: fmt.Sprintf("cursor %s at %v", orStr(in.Action, "click"), in.Position)}, err
	}
	out, err := v2Op(ctx, c, "pie_ui_click", pick(c.Args, "widget", "button"))
	return &spec.Result{Data: out, Summary: "clicked " + in.Widget}, err
}

// pieMinPoll is the least time worth polling for PIE to start (a var for tests).
var pieMinPoll = 5 * time.Second

func pieHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in pieIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if c.Op.Name != "start" && c.Op.Name != "stop" {
		return pieInputHandler(ctx, c, in)
	}
	want := c.Op.Name == "start"
	py := map[bool]string{true: "pie_start", false: "pie_stop"}[want]
	var pre map[string]any
	if want {
		var perr error
		if pre, perr = v2Op(ctx, c, "pie_preflight", pick(c.Args, "ignore_blueprint_errors")); perr != nil {
			if e := envelope.Classify(perr, c.Op.Tier > spec.ReadOnly); e.Details["blueprints"] != nil {
				return nil, e.WithHint("fix them (the editor log names the errors), or pass ignore_blueprint_errors=true to play anyway")
			}
			return nil, perr // PIE was not requested
		}
	}
	out, err := v2Op(ctx, c, py, pick(c.Args, "simulate"))
	// A new (or no) game world: the next game_command reads its epoch first.
	forgetGameWorld(c)
	for k, v := range pre {
		if out != nil {
			out[k] = v
		}
	}
	if err != nil {
		// v2Op returns the bridge's error; the envelope is built here so the hint lands.
		if e := envelope.Classify(err, c.Op.Tier > spec.ReadOnly); e.Details["blueprints"] != nil {
			return nil, e.WithHint("fix them (the editor log names the errors), or pass ignore_blueprint_errors=true to play anyway")
		}
	}
	pid, _ := out["editor_pid"].(float64)
	delete(out, "editor_pid")
	if err != nil || (in.Wait != nil && !*in.Wait) {
		return &spec.Result{Data: out, Summary: "PIE " + c.Op.Name + " requested"}, err
	}
	if dl, ok := ctx.Deadline(); ok && want && time.Until(dl) < pieMinPoll {
		// A slow pre-flight used the call: PIE is requested, and a timeout here would
		// only invite a retry of a start that is already under way.
		out["pie"] = "requested"
		out["note"] = "the Blueprint pre-flight used most of this call; confirm with editor op=ping (pie)"
		return &spec.Result{Data: out, Summary: "PIE requested"}, nil
	}
	// PIE begins/ends on a later editor tick: poll until the state flips. Each ping is
	// short so a game thread blocked by a modal dialog is noticed, not waited out.
	started := time.Now()
	deadline := started.Add(pollTimeout(ctx, 0, 15*time.Second))
	for {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ping, perr := v2Op(pctx, c, "editor_ping", nil)
		cancel()
		if perr == nil && ping["pie"] == want {
			out["pie"] = want
			return &spec.Result{Data: out, Summary: map[bool]string{true: "PIE is running", false: "PIE stopped"}[want]}, nil
		}
		if perr != nil && ctx.Err() == nil {
			if e := cancelPIEBlueprintDialog(int(pid), in.IgnoreBP); e != nil {
				return nil, e
			}
		}
		if time.Now().After(deadline) {
			if perr != nil {
				// The editor stopped answering (e.g. crashed on PIE start): say so, with the crash if any.
				e := envelope.Classify(perr, true)
				if wins := editorDialogs(int(pid)); len(wins) > 0 {
					// Reported, never touched: a dialog may be what holds the game thread.
					e.WithDetail("editor_windows", wins).WithHint("the editor may be waiting on a dialog: enable the desktop " +
						"toolset, look with desktop_capture op=window hwnd=<hwnd>, answer it with desktop_input")
				}
				if c.Deps.ProjectDir != "" {
					if rep, _ := crash.FromCrashDir(c.Deps.ProjectDir, started); rep != nil {
						e.WithDetail("crash", rep)
					}
				}
				return nil, e
			}
			return nil, envelope.New(envelope.Timeout, "PIE did not %s in time", c.Op.Name).
				WithHint("check the editor log (logs op=tail) — a compile error or a modal dialog can block PIE")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// editorDialogs lists the top-level windows of the editor process other than its
// main window (Windows only; nil elsewhere or without a pid). An editor whose game
// thread is held by a modal dialog stops answering while it waits for a human.
var editorDialogs = func(pid int) []desktop.Window {
	if pid <= 0 {
		return nil
	}
	all, err := desktop.ListWindows("")
	if err != nil {
		return nil
	}
	var out []desktop.Window
	for _, w := range all {
		if w.PID == pid && !strings.HasSuffix(w.Title, "Unreal Editor") {
			out = append(out, w)
		}
	}
	return out
}

var closeWindow = desktop.CloseWindow

const bpErrorsDialog = "Blueprint Compilation Errors"

// cancelPIEBlueprintDialog cancels PIE's "Blueprint Compilation Errors" dialog
// (WM_CLOSE = its Cancel) when the editor shows it. Other windows are only reported,
// with the timeout: progress windows and docked-out tabs look the same from outside.
func cancelPIEBlueprintDialog(pid int, ignoreAsked bool) *envelope.Error {
	for _, w := range editorDialogs(pid) {
		if w.Title != bpErrorsDialog {
			continue
		}
		hint := "fix the Blueprints (logs op=tail names them), or pass ignore_blueprint_errors=true (needs the UnrealMCP plugin)"
		if ignoreAsked {
			hint = "ignore_blueprint_errors needs a current UnrealMCP plugin (copy plugin/UnrealMCP into the project, " +
				"build strategy=ubt); or fix the Blueprints (logs op=tail names them)"
		}
		e := envelope.New(envelope.Precondition, "PIE stopped at the editor's %q dialog, which was cancelled", bpErrorsDialog).
			WithHint(hint).WithDetail("modal", w.Title)
		if err := closeWindow(w.HWND); err != nil {
			e.Message = fmt.Sprintf("PIE is stopped at the editor's %q dialog (closing it failed: %v)", bpErrorsDialog, err)
		}
		return e
	}
	return nil
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// --- pie_observe / pie_wait ------------------------------------------------------

type pieObserveIn struct {
	Actors     []string `json:"actors,omitempty" jsonschema:"actor labels to detail (unknown ones are listed in missing)"`
	Pawn       bool     `json:"pawn,omitempty" jsonschema:"include the player pawn's location, velocity and speed"`
	Player     int      `json:"player,omitempty" jsonschema:"pawn: local player index (default 0)"`
	Include    []string `json:"include,omitempty" jsonschema:"glob patterns of property names to include (default all, minus engine noise)"`
	Exclude    []string `json:"exclude,omitempty" jsonschema:"glob patterns of property names to exclude"`
	Properties []string `json:"properties,omitempty" jsonschema:"read exactly these properties (keeps your key names for predicates)"`
	MaxProps   int      `json:"max_props,omitempty" jsonschema:"cap on properties per object (default 48)"`
}

func pieObserveSpec() *spec.Spec {
	return &spec.Spec{
		Name: "pie_observe", Title: "Observe the running game", Toolset: spec.Core, Timeout: sync15, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"pie_observe"}, Needs: []string{"pie"}}},
		Description: "Read the running game (PIE): gamestate properties (discovered by reflection), a class histogram " +
			"`counts`, detailed state for `actors`, and with pawn=true the player pawn's location/velocity/speed. " +
			"The output schema is what pie_wait predicates address (gamestate.Prop, counts.Class, pawn.speed).",
		Schema:   spec.SchemaFor[pieObserveIn](nil),
		Replaces: []string{"pie_observe", "pawn_state"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			out, err := v2Op(ctx, c, "pie_observe", pick(c.Args, "actors", "pawn", "player", "include", "exclude", "properties", "max_props"))
			if err != nil {
				return nil, err
			}
			n := 0
			if counts, ok := out["counts"].(map[string]any); ok {
				for _, v := range counts {
					f, _ := v.(float64)
					n += int(f)
				}
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%d actors in the running game", n)}, nil
		},
	}
}

type pieWaitIn struct {
	Predicate  string   `json:"predicate" jsonschema:"conditions over pie_observe output joined by and/or/not, e.g. 'gamestate.wave >= 2 and counts.Enemy >= 1'; or an object path: '@subsystem:Class.Getter().field >= 3' (BlueprintPure/const getters only)"`
	TimeoutS   float64  `json:"timeout_s,omitempty" jsonschema:"give up after this many seconds (default 20; up to 600 — beyond 25 the wait continues as a job)"`
	WaitS      float64  `json:"wait_s,omitempty" jsonschema:"a job wait (timeout_s > 25): return after this many seconds (max 25), then follow it with job"`
	IntervalS  float64  `json:"interval_s,omitempty" jsonschema:"seconds between observations (default 0.25)"`
	Properties []string `json:"properties,omitempty" jsonschema:"pin exact gamestate property names so the predicate can use them verbatim"`
	Pawn       bool     `json:"pawn,omitempty" jsonschema:"observe the pawn too (needed for pawn.* predicates)"`
}

// maxWaitS bounds a pie_wait that continues as a job.
const maxWaitS = 600

func pieWaitSpec() *spec.Spec {
	return &spec.Spec{
		Name: "pie_wait", Title: "Wait for a game condition", Toolset: spec.Core, Timeout: sync28, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.ReadOnly, Idempotent: true, Required: []string{"predicate"}, Reaches: []string{"pie_observe", "observe_paths"}}},
		Description: "Poll the running game until `predicate` holds → {met, pie_running, elapsed_s, polls, final_state}. " +
			"met=false on timeout is an answer, not an error. Waits through PIE starting up; returns at once if PIE " +
			"stops. timeout_s > 25 runs as a job (wait_s, then job). Object paths need the plugin. Read-only: to poll a " +
			"function with side effects use actor_call with until.",
		Schema:   spec.SchemaFor[pieWaitIn](nil, "predicate"),
		Replaces: []string{"pie_wait_until"},
		Handler:  pieWait,
	}
}

func pieWait(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in pieWaitIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	pred, err := eval.ParsePredicate(in.Predicate)
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "predicate: %v", err)
	}
	if in.TimeoutS > maxWaitS {
		return nil, envelope.New(envelope.InvalidArgument, "timeout_s is at most %d", maxWaitS)
	}
	interval := 250 * time.Millisecond
	if in.IntervalS > 0 {
		interval = time.Duration(in.IntervalS * float64(time.Second))
	}
	args := pick(c.Args, "properties", "pawn")
	if in.TimeoutS > float64(spec.MaxWait/time.Second) {
		// A long wait continues as a job, polled on its own context (the call returns
		// after wait_s, as every async op does).
		reg, err := jobsOf(c)
		if err != nil {
			return nil, err
		}
		timeout := time.Duration(in.TimeoutS * float64(time.Second))
		j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
			progress(fmt.Sprintf("waiting up to %s for %s", timeout, in.Predicate))
			res, err := waitFor(jctx, c, pred, args, timeout, interval)
			if err != nil {
				return nil, err
			}
			return res.Data, nil
		})
		return &spec.Result{Job: j}, nil
	}
	return waitFor(ctx, c, pred, args, pollTimeout(ctx, in.TimeoutS, 20*time.Second), interval)
}

// observeState reads what a predicate needs: pie_observe for state paths and
// observe_paths for object paths (merged in under each path string).
func observeState(ctx context.Context, c *spec.Call, pred *eval.Predicate, args map[string]any) (map[string]any, error) {
	state := map[string]any{}
	if pred.HasStatePaths() {
		out, err := v2Op(ctx, c, "pie_observe", args)
		if err != nil {
			return nil, err
		}
		state = out
	}
	if paths := pred.ObjectPaths(); len(paths) > 0 {
		out, err := v2Op(ctx, c, "observe_paths", map[string]any{"paths": paths, "world": "pie"})
		if err != nil {
			return nil, err
		}
		values, _ := out["values"].(map[string]any)
		for _, p := range paths {
			state[p] = values[p]
		}
		if errs, _ := out["errors"].(map[string]any); len(errs) > 0 {
			state["object_errors"] = errs
		}
	}
	return state, nil
}

// transientWaitError says whether a poll's error is worth polling past: PIE not
// running (yet), or any error the envelope marks retryable — a remote-exec timeout, an
// unreachable editor, a retryable op error — whatever its Go type (a transport error is
// not a *bridge.OpError; an op error with editor log lines arrives as an envelope.Error).
// A malformed predicate, a non-pure getter or a bad target is not.
func transientWaitError(err error) (transient, notInPIE bool) {
	e := envelope.Classify(err, false)
	notInPIE = e.Code == envelope.PIENotRunning
	return notInPIE || e.Retryable, notInPIE
}

// waitFor polls until the predicate holds, PIE stops after running, or timeout.
func waitFor(ctx context.Context, c *spec.Call, pred *eval.Predicate, args map[string]any, timeout, interval time.Duration) (*spec.Result, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	polls := 0
	seen := false // PIE observed running at least once
	var last map[string]any
	answer := func(met, running bool, why string) *spec.Result {
		return &spec.Result{Data: map[string]any{"met": met, "pie_running": running, "elapsed_s": time.Since(start).Seconds(),
			"polls": polls, "final_state": last}, Summary: why}
	}
	for {
		out, err := observeState(ctx, c, pred, args)
		polls++
		running := err == nil
		if err != nil {
			// PIE still starting (NOT_IN_PIE) or a transient editor error: keep waiting —
			// unless PIE was running and has now ended (game over / stopped): the answer is final.
			transient, notInPIE := transientWaitError(err)
			if !transient {
				return nil, err
			}
			if notInPIE && seen {
				return answer(false, false, "PIE stopped before the condition was met"), nil
			}
		} else {
			seen, last = true, out
			if ok, _ := pred.Eval(out); ok {
				return answer(true, true, "condition met after "+strconv.Itoa(polls)+" polls"), nil
			}
		}
		if time.Now().Add(interval).After(deadline) {
			why := "condition not met within " + timeout.Round(time.Millisecond).String()
			if !seen {
				why += " (PIE never ran — start it with pie op=start)"
			}
			return answer(false, running, why), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// --- world_query -----------------------------------------------------------------

type worldQueryIn struct {
	Op     string    `json:"op" jsonschema:"line_trace | sphere_overlap | nav_path | project_point | instances_count | instances_list"`
	World  string    `json:"world,omitempty" jsonschema:"editor (default) | pie | auto"`
	Start  []float64 `json:"start,omitempty" jsonschema:"line_trace/nav_path: [x, y, z]"`
	End    []float64 `json:"end,omitempty" jsonschema:"line_trace/nav_path: [x, y, z]"`
	Center []float64 `json:"center,omitempty" jsonschema:"sphere_overlap: [x, y, z]"`
	Radius float64   `json:"radius,omitempty" jsonschema:"sphere_overlap: radius (default 100)"`
	Types  []string  `json:"object_types,omitempty" jsonschema:"sphere_overlap: world_static | world_dynamic | pawn | physics_body | vehicle | destructible (default: these six) | object_type_query_N (a project channel)"`
	Point  []float64 `json:"point,omitempty" jsonschema:"project_point: [x, y, z]"`
	Tag    string    `json:"tag,omitempty" jsonschema:"instances_*: only ISM/HISM components with this component tag"`
	Mesh   string    `json:"mesh,omitempty" jsonschema:"instances_*: only components whose mesh path contains this"`
	Limit  int       `json:"limit,omitempty" jsonschema:"instances_list: max instances (default 8192; truncated:true when cut)"`
}

func worldQuerySpec() *spec.Spec {
	q := func(name, summary, py string, req ...string) spec.OpSpec {
		return spec.OpSpec{Name: name, Summary: summary, Tier: spec.ReadOnly, Idempotent: true, Required: req, Reaches: []string{py}}
	}
	navq := func(name, summary, py string, req ...string) spec.OpSpec {
		o := q(name, summary, py, req...)
		o.Needs = []string{"navmesh"}
		return o
	}
	ops := []spec.OpSpec{
		q("line_trace", "is the line from start to end blocked, and by what", "world_query", "start", "end"),
		q("sphere_overlap", "actors overlapping a sphere (of object_types)", "world_query", "center"),
		navq("nav_path", "can the AI walk from start to end", "world_query", "start", "end"),
		navq("project_point", "is the point on the navmesh", "world_query", "point"),
		q("instances_count", "ISM/HISM instance counts by mesh", "instances_count"),
		q("instances_list", "ISM/HISM instance transforms", "instances_list"),
	}
	for i := range ops {
		if ops[i].Name != "sphere_overlap" {
			ops[i].Rejects = append(ops[i].Rejects, "object_types")
		}
	}
	return &spec.Spec{
		Name: "world_query", Title: "Spatial queries", Toolset: spec.World, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Spatial questions (world=editor default, pie, auto; results echo it).\n- line_trace / sphere_overlap: collision.\n- nav_path / project_point: navigation (built navmesh).\n- instances_count / instances_list: ISM/HISM instances, which actor_query cannot see.",
		Schema:      spec.SchemaFor[worldQueryIn](map[string][]any{"op": spec.OpEnum(ops...), "world": {"editor", "pie", "auto"}}, "op"),
		Replaces:    []string{"world_query", "instances_count", "instances_list"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			world, _ := c.Args["world"].(string)
			world = orStr(world, "editor")
			py := c.Op.Name
			var args map[string]any
			if py == "instances_count" || py == "instances_list" {
				args = pick(c.Args, "tag", "mesh", "limit")
			} else {
				py = "world_query"
				args = pick(c.Args, "start", "end", "center", "radius", "point", "object_types")
				args["kind"] = c.Op.Name
			}
			args["world"] = world
			out, err := v2Op(ctx, c, py, args)
			if err != nil {
				return nil, err
			}
			if _, ok := out["world"]; !ok {
				out["world"] = world
			}
			return &spec.Result{Data: out, Summary: c.Op.Name + " done"}, nil
		},
	}
}

// --- snapshot / snapshot_restore -------------------------------------------------

type snapshotIn struct {
	Op          string   `json:"op" jsonschema:"take | diff | list | digest"`
	Name        string   `json:"name,omitempty" jsonschema:"take: snapshot name (default auto; overwrites); diff: the BEFORE snapshot"`
	Against     string   `json:"against,omitempty" jsonschema:"diff: the AFTER snapshot (default: the level right now)"`
	ClassFilter string   `json:"class_filter,omitempty" jsonschema:"take/digest scope=actors: only actors whose class or label contains this"`
	Properties  []string `json:"properties,omitempty" jsonschema:"take: also these properties (e.g. Health); restore resets them"`
	Scope       string   `json:"scope,omitempty" jsonschema:"digest: instances (ISM/HISM, default) | actors"`
	Tag         string   `json:"tag,omitempty" jsonschema:"digest scope=instances: component tag filter"`
	Mesh        string   `json:"mesh,omitempty" jsonschema:"digest scope=instances: mesh path substring filter"`
	PosBucket   float64  `json:"pos_bucket,omitempty" jsonschema:"digest: position quantization in world units (default 1)"`
	RotBucket   float64  `json:"rot_bucket,omitempty" jsonschema:"digest: rotation quantization in degrees (default 1)"`
	Limit       int      `json:"limit,omitempty" jsonschema:"digest instances: max hashed (default 5,000,000; more fails)"`
}

func snapshotSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "take", Summary: "record every actor's path, class, tags, transform (+ properties)", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"snapshot_actors"}, Needs: []string{"project"}},
		{Name: "diff", Summary: "added / removed / moved / retagged / changed between two snapshots (or now)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"name"}, Rejects: []string{"properties"}, Reaches: []string{"snapshot_actors"}, Needs: []string{"project"}},
		{Name: "list", Summary: "stored snapshots", Tier: spec.ReadOnly, Idempotent: true, Rejects: []string{"properties"}, Needs: []string{"project"}},
		{Name: "digest", Summary: "deterministic hash of actor or instance transforms", Tier: spec.ReadOnly, Idempotent: true, Rejects: []string{"properties"}, Reaches: []string{"snapshot_actors", "instances_list"}},
	}
	return &spec.Spec{
		Name: "snapshot", Title: "Level snapshots", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Record and compare the editor level (Saved/MCP/snapshots).\n- take: store `name` (default auto): every actor's path, class, tags, transform (+ `properties`).\n- diff: `name` vs `against` (default: now) → added, removed, moved, retagged, changed (by object path); unloaded World Partition actors are unknown, never removed.\n- list.\n- digest: quantized SHA1 of actor (scope=actors) or ISM/HISM instance transforms; stores nothing.\nPut back transforms and properties with snapshot_restore.",
		Schema:      spec.SchemaFor[snapshotIn](map[string][]any{"op": spec.OpEnum(ops...), "scope": {"instances", "actors"}}, "op"),
		Replaces:    []string{"level_snapshot", "level_diff", "scene_snapshot", "scene_digest"},
		Handler:     snapshotHandler,
	}
}

// currentSnapshot asks the editor for its actors (and the given properties) as a
// snapshot.File. strict (a take): a property no actor in scope has, or a value that
// cannot be restored, is an error — a snapshot never records less than asked. A diff is
// lenient: the level may have lost a property's holders since.
func currentSnapshot(ctx context.Context, c *spec.Call, name, classFilter string, properties []string, strict bool) (*snapshot.File, error) {
	b, err := v2Bridge(c)
	if err != nil {
		return nil, err
	}
	args := map[string]any{"class_filter": classFilter}
	if len(properties) > 0 {
		args["properties"] = properties
	}
	raw, err := b.Call(ctx, "snapshot_actors", args)
	if err != nil {
		return nil, err
	}
	f := &snapshot.File{Name: name, TakenAt: time.Now().UTC(), Properties: properties}
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, fmt.Errorf("decode snapshot_actors: %w", err)
	}
	var extra struct {
		Missing []string         `json:"properties_missing"`
		Errors  []map[string]any `json:"property_errors"`
	}
	_ = json.Unmarshal(raw, &extra)
	if !strict {
		return f, nil
	}
	if len(extra.Missing) > 0 {
		return nil, envelope.New(envelope.InvalidArgument, "no actor in scope has %s (reflected names, e.g. Health or bHidden)", strings.Join(extra.Missing, ", ")).
			WithDetail("missing", extra.Missing)
	}
	if len(extra.Errors) > 0 {
		return nil, envelope.New(envelope.InvalidArgument, "%d property value(s) cannot be restored; the first: %v", len(extra.Errors), extra.Errors[0]).
			WithDetail("property_errors", extra.Errors)
	}
	return f, nil
}

// normProps sorts and de-duplicates a take's properties (two snapshots of the same set compare).
func normProps(ps []string) []string {
	out := slices.Clone(ps)
	slices.Sort(out)
	return slices.Compact(out)
}

func snapshotName(s string) (string, error) {
	if s == "" {
		s = "auto"
	}
	if !snapshot.ValidName(s) {
		return "", envelope.New(envelope.InvalidArgument, "snapshot name %q: use 1-64 letters, digits, '_' or '-'", s)
	}
	return s, nil
}

func loadSnapshot(dir, name string) (*snapshot.File, error) {
	f, err := snapshot.Load(dir, name)
	if errors.Is(err, snapshot.ErrNotFound) {
		return nil, envelope.New(envelope.NotFound, "no snapshot named %q", name).WithHint("snapshot op=list shows the stored ones")
	}
	return f, err
}

func snapshotHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in snapshotIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if c.Op.Name == "digest" {
		return snapshotDigest(ctx, c, in)
	}
	dir, err := projectDir(c)
	if err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "list":
		l, err := snapshot.List(dir)
		if err != nil {
			return nil, err
		}
		return &spec.Result{Data: map[string]any{"snapshots": l}, Summary: fmt.Sprintf("%d snapshots", len(l))}, nil
	case "take":
		name, err := snapshotName(in.Name)
		if err != nil {
			return nil, err
		}
		f, err := currentSnapshot(ctx, c, name, in.ClassFilter, normProps(in.Properties), true)
		if err != nil {
			return nil, err
		}
		p, err := snapshot.Save(dir, f)
		if err != nil {
			return nil, err
		}
		data := map[string]any{"name": name, "file": filepath.ToSlash(p), "actors": len(f.Actors), "world_partition": f.WorldPartition}
		if f.PIERunning {
			data["note"] = "PIE is running: this is the editor level, not the game being played"
		}
		return &spec.Result{Data: data,
			Summary: fmt.Sprintf("snapshot %s: %d actors", name, len(f.Actors))}, nil
	}
	// diff
	name, err := snapshotName(in.Name)
	if err != nil {
		return nil, err
	}
	a, err := loadSnapshot(dir, name)
	if err != nil {
		return nil, err
	}
	var b *snapshot.File
	if in.Against != "" {
		against, err := snapshotName(in.Against)
		if err != nil {
			return nil, err
		}
		if b, err = loadSnapshot(dir, against); err != nil {
			return nil, err
		}
		if b.ClassFilter != a.ClassFilter {
			return nil, envelope.New(envelope.InvalidArgument, "%s was taken with class_filter %q and %s with %q; they cannot be compared",
				name, a.ClassFilter, against, b.ClassFilter)
		}
		if strings.Join(a.Properties, ",") != strings.Join(b.Properties, ",") {
			return nil, envelope.New(envelope.InvalidArgument, "%s recorded properties %v and %s %v; they cannot be compared",
				name, a.Properties, against, b.Properties)
		}
	} else if b, err = currentSnapshot(ctx, c, "current", a.ClassFilter, a.Properties, false); err != nil { // compare like with like
		return nil, err
	}
	d := snapshot.Diff(a, b)
	return &spec.Result{Data: map[string]any{"before": name, "after": orStr(in.Against, "current"), "diff": d},
		Summary: fmt.Sprintf("+%d -%d moved %d retagged %d changed %d unknown %d", len(d.Added), len(d.Removed), len(d.Moved), len(d.Retagged), len(d.Changed), len(d.Unknown))}, nil
}

func snapshotDigest(ctx context.Context, c *spec.Call, in snapshotIn) (*spec.Result, error) {
	scope := orStr(in.Scope, "instances")
	var items []snapshot.Transform
	if scope == "actors" {
		f, err := currentSnapshot(ctx, c, "digest", in.ClassFilter, nil, false)
		if err != nil {
			return nil, err
		}
		for _, a := range f.Actors {
			items = append(items, snapshot.Transform{Mesh: a.Class, Loc: a.Loc, Rot: a.Rot, Scale: a.Scale})
		}
	} else {
		limit := in.Limit
		if limit <= 0 {
			limit = 5000000
		}
		args := map[string]any{"world": "editor", "limit": limit}
		if in.Tag != "" {
			args["tag"] = in.Tag
		}
		if in.Mesh != "" {
			args["mesh"] = in.Mesh
		}
		b, err := v2Bridge(c)
		if err != nil {
			return nil, err
		}
		raw, err := b.Call(ctx, "instances_list", args)
		if err != nil {
			return nil, err
		}
		var r struct {
			Instances []snapshot.Transform `json:"instances"`
			Truncated bool                 `json:"truncated"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if r.Truncated {
			// A content oracle never hashes a partial set.
			return nil, envelope.New(envelope.OperationFailed, "more than %d instances; the digest would cover only part of them", limit).
				WithHint("narrow with tag/mesh or raise limit")
		}
		items = r.Instances
	}
	res := snapshot.Digest(items, snapshot.Quant{PosBucket: in.PosBucket, RotBucket: in.RotBucket})
	return &spec.Result{Data: map[string]any{"scope": scope, "hash": res.Hash, "count": res.Count,
		"worst_pos_margin_uu": res.WorstPosMargin, "worst_rot_margin_deg": res.WorstRotMargin},
		Summary: fmt.Sprintf("%s digest %s over %d items", scope, res.Hash, res.Count)}, nil
}

type snapshotRestoreIn struct {
	Name string `json:"name,omitempty" jsonschema:"the snapshot to restore (default auto)"`
	Save *bool  `json:"save,omitempty" jsonschema:"save the level afterwards (default true)"`
}

func snapshotRestoreSpec() *spec.Spec {
	return &spec.Spec{
		Name: "snapshot_restore", Title: "Restore a snapshot", Toolset: spec.Core, Timeout: sync25, Max: sync28,
		Ops:         []spec.OpSpec{{Tier: spec.Destructive, Idempotent: true, Reaches: []string{"snapshot_restore"}, Needs: []string{"project"}}},
		Description: "Move every actor that still exists back to its transform in snapshot `name` (by object path, parents first), as one undo step, then save. Transforms and the snapshot's properties only: spawned/deleted actors are listed in not_restored {added, removed, unknown (unloaded WP cells)}; for those use scene_clear or git_revert.",
		Schema:      spec.SchemaFor[snapshotRestoreIn](nil),
		Replaces:    []string{"scene_restore"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in snapshotRestoreIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			dir, err := projectDir(c)
			if err != nil {
				return nil, err
			}
			name, err := snapshotName(in.Name)
			if err != nil {
				return nil, err
			}
			f, err := loadSnapshot(dir, name)
			if err != nil {
				return nil, err
			}
			args := map[string]any{"name": name, "actors": f.Actors, "class_filter": f.ClassFilter}
			if in.Save != nil {
				args["save"] = *in.Save
			}
			out, err := v2Op(ctx, c, "snapshot_restore", args)
			if err != nil {
				return nil, err
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("restored %v actors from %s", out["restored"], name)}, nil
		},
	}
}

// --- screenshot ------------------------------------------------------------------

type screenshotIn struct {
	Op         string    `json:"op" jsonschema:"viewport | pie | orbit"`
	Width      int       `json:"width,omitempty" jsonschema:"viewport/pie: pixels (default 1280 / 1920)"`
	Height     int       `json:"height,omitempty" jsonschema:"viewport/pie: pixels (default 720 / 1080)"`
	Location   []float64 `json:"location,omitempty" jsonschema:"viewport: camera [x, y, z] (default: the editor viewport camera)"`
	Rotation   []float64 `json:"rotation,omitempty" jsonschema:"viewport: camera [pitch, yaw, roll]"`
	Actors     []string  `json:"actors,omitempty" jsonschema:"orbit: actor labels or globs to frame (default: the whole level)"`
	NumAngles  int       `json:"num_angles,omitempty" jsonschema:"orbit: angles around the target (default 8)"`
	Elevation  *float64  `json:"elevation,omitempty" jsonschema:"orbit: degrees above the horizon (default 25)"`
	Fov        float64   `json:"fov,omitempty" jsonschema:"orbit: vertical field of view (default 60)"`
	Fill       float64   `json:"fill,omitempty" jsonschema:"orbit: fraction of the frame the target fills (default 0.7)"`
	Cols       int       `json:"cols,omitempty" jsonschema:"orbit: contact-sheet columns (default 4)"`
	CellWidth  int       `json:"cell_width,omitempty" jsonschema:"orbit: per-angle width (default 480)"`
	CellHeight int       `json:"cell_height,omitempty" jsonschema:"orbit: per-angle height (default 270)"`
	UI         bool      `json:"ui,omitempty" jsonschema:"pie: the screen as the player sees it, UMG/Slate UI included, paused or not (plugin; a visible game viewport, at its own size)"`
}

func screenshotSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "viewport", Summary: "render the editor world from the viewport (or a given) camera", Tier: spec.Ephemeral, Idempotent: true, Rejects: []string{"ui"}, Reaches: []string{"take_screenshot"}},
		{Name: "pie", Summary: "the running game's screen (HighResShot; ui=true: with the UI)", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"pie_screenshot", "pie_ui_shot"}, Needs: []string{"pie", "plugin>=7 for ui"}},
		{Name: "orbit", Summary: "N angles around a target as one contact sheet", Tier: spec.Ephemeral, Idempotent: true, Rejects: []string{"ui"}, Reaches: []string{"scene_bounds", "capture_poses"}},
	}
	return &spec.Spec{
		Name: "screenshot", Title: "Screenshot", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Look at the world (PNG).\n- viewport: the editor world via a scene capture (works backgrounded).\n- pie: the running game's screen (needs a visible viewport); HighResShot leaves out UMG/Slate UI — ui=true includes it.\n- orbit: `actors` (or the level) from num_angles angles in one sheet.\nResults list any map the capture actors dirtied.",
		Schema:      spec.SchemaFor[screenshotIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"take_screenshot", "pie_screenshot", "scene_contact_sheet"},
		Handler:     screenshot,
	}
}

// uiShot takes the running game's screen with its UI: HighResShot renders the scene
// only (UMG and Slate HUDs are composited by Slate, not the renderer), so the plugin
// reads the game viewport through Slate — one frame, taken now (no capture timer, so a
// paused game, its pause menu included, can be shot). The size is the viewport's.
func uiShot(ctx context.Context, c *spec.Call, in screenshotIn) (*spec.Result, error) {
	if in.Width > 0 || in.Height > 0 {
		return nil, envelope.New(envelope.InvalidArgument, "ui=true takes the game viewport at its own size: drop width/height")
	}
	out, err := v2Op(ctx, c, "pie_ui_shot", nil)
	if err != nil {
		return nil, err
	}
	p, _ := out["file"].(string)
	data := map[string]any{"file": p, "ui": true, "width": out["width"], "height": out["height"]}
	res := &spec.Result{Data: data, Summary: "the game's screen with its UI"}
	attachPNG(res, data, p)
	return res, nil
}

func screenshot(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in screenshotIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if c.Op.Name == "orbit" {
		return orbitShot(ctx, c, in)
	}
	if c.Op.Name == "pie" && in.UI {
		return uiShot(ctx, c, in)
	}
	fname := fmt.Sprintf("mcp_%d_%d.png", os.Getpid(), time.Now().UnixNano())
	args := map[string]any{"filename": fname}
	if in.Width > 0 {
		args["width"] = in.Width
	}
	if in.Height > 0 {
		args["height"] = in.Height
	}
	py, wait := "take_screenshot", 10*time.Second
	if c.Op.Name == "pie" {
		py, wait = "pie_screenshot", 20*time.Second // HighResShot writes asynchronously
	} else {
		if len(in.Location) > 0 {
			args["camera_location"] = in.Location
		}
		if len(in.Rotation) > 0 {
			args["camera_rotation_pyr"] = in.Rotation
		}
	}
	out, err := v2Op(ctx, c, py, args)
	if err != nil {
		return nil, err
	}
	file, _ := out["file"].(string)
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
		wait = time.Until(dl) - 500*time.Millisecond
	}
	if c.Op.Name == "pie" {
		file = awaitShot(file, wait)
	}
	img, err := readImageFile(file, wait)
	if err != nil {
		return nil, withLog(envelope.New(envelope.OperationFailed, "%v", err), out)
	}
	delete(out, "file")
	delete(out, "async")
	return &spec.Result{Data: out, Content: []mcp.Content{&mcp.ImageContent{Data: img, MIMEType: "image/png"}},
		Summary: c.Op.Name + " screenshot"}, nil
}

func orbitShot(ctx context.Context, c *spec.Call, in screenshotIn) (*spec.Result, error) {
	bargs := map[string]any{}
	if len(in.Actors) > 0 {
		bargs["globs"] = in.Actors
	}
	bounds, err := v2Op(ctx, c, "scene_bounds", bargs)
	if err != nil {
		return nil, err
	}
	if n, _ := bounds["count"].(float64); n == 0 {
		return nil, envelope.New(envelope.NotFound, "nothing to frame: no actor matched %v", in.Actors)
	}
	comb, _ := bounds["combined"].(map[string]any)
	box := visual.Bounds{Origin: anyVec3(comb["origin"]), Extent: anyVec3(comb["extent"])}
	num := orDefaultInt(in.NumAngles, 8)
	elevation := 25.0
	if in.Elevation != nil {
		elevation = *in.Elevation
	}
	fov, fill := orDefault(in.Fov, 60), orDefault(in.Fill, 0.7)
	poses := make([]map[string]any, num)
	for i := range poses {
		p := visual.FrameShot(box, 360*float64(i)/float64(num), elevation, fov, fill)
		poses[i] = map[string]any{"location": p.Location[:], "rotation_pyr": p.RotationPyr[:]}
	}
	b, err := v2Bridge(c)
	if err != nil {
		return nil, err
	}
	raw, err := b.Call(ctx, "capture_poses", map[string]any{"poses": poses, "world": "editor",
		"cell_width": orDefaultInt(in.CellWidth, 480), "cell_height": orDefaultInt(in.CellHeight, 270)})
	if err != nil {
		return nil, err
	}
	var pr struct {
		Dir   string `json:"dir"`
		Cells []struct {
			Index       int       `json:"index"`
			File        string    `json:"file"`
			RotationPyr []float64 `json:"rotation_pyr"`
		} `json:"cells"`
		Dirtied []string `json:"dirtied"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, err
	}
	frames := make([]captureFrame, len(pr.Cells))
	for i, cell := range pr.Cells {
		yaw := 0.0
		if len(cell.RotationPyr) > 1 {
			yaw = cell.RotationPyr[1]
		}
		frames[i] = captureFrame{Index: cell.Index, File: cell.File, State: map[string]any{"yaw": yaw}}
	}
	png, sidecar, err := montage(pr.Dir, frames, orDefaultInt(in.Cols, 4), false, nil)
	if err != nil {
		return nil, err
	}
	sidecar["center"], sidecar["angles"], sidecar["dirtied"] = comb["origin"], num, pr.Dirtied
	return &spec.Result{Data: sidecar, Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}},
		Summary: fmt.Sprintf("%d-angle contact sheet", num)}, nil
}

func anyVec3(v any) [3]float64 {
	var out [3]float64
	if s, ok := v.([]any); ok {
		for i := 0; i < 3 && i < len(s); i++ {
			out[i], _ = s[i].(float64)
		}
	}
	return out
}

// --- capture ---------------------------------------------------------------------

type captureIn struct {
	Op          string    `json:"op" jsonschema:"start | status | stop | read | clear"`
	Session     string    `json:"session,omitempty" jsonschema:"start: id (default generated); else the session"`
	World       string    `json:"world,omitempty" jsonschema:"start: editor (default) | pie"`
	Source      string    `json:"source,omitempty" jsonschema:"start: scene_capture (default; editor) | pie_highres (possessed PIE) | game_scene (PIE, plugin)"`
	IntervalS   float64   `json:"interval_s,omitempty" jsonschema:"start: seconds between frames (default 0.25)"`
	CellWidth   int       `json:"cell_width,omitempty" jsonschema:"start: frame width (default 480)"`
	CellHeight  int       `json:"cell_height,omitempty" jsonschema:"start: frame height (default 270)"`
	CameraMode  string    `json:"camera_mode,omitempty" jsonschema:"start: viewport (default, editor camera) | fixed | actor | player (game_scene POV)"`
	CameraActor string    `json:"camera_actor,omitempty" jsonschema:"start: actor label to ride (camera_mode=actor)"`
	CameraFov   float64   `json:"camera_fov,omitempty" jsonschema:"game_scene: FOV (default 90)"`
	Location    []float64 `json:"location,omitempty" jsonschema:"start camera_mode=fixed: [x, y, z]"`
	Rotation    []float64 `json:"rotation,omitempty" jsonschema:"start camera_mode=fixed: [pitch, yaw, roll]"`
	TrackActors []string  `json:"track_actors,omitempty" jsonschema:"start: actor labels to record per-frame state for"`
	MaxFrames   int       `json:"max_frames,omitempty" jsonschema:"start: stop after N frames (default 240)"`
	MaxSeconds  float64   `json:"max_seconds,omitempty" jsonschema:"start: stop after N seconds (default 60)"`
	Include     []string  `json:"include,omitempty" jsonschema:"start: observed property include globs"`
	Exclude     []string  `json:"exclude,omitempty" jsonschema:"start: observed property exclude globs"`
	Properties  []string  `json:"properties,omitempty" jsonschema:"start: exact observed properties"`
	IncludeUI   bool      `json:"include_ui,omitempty" jsonschema:"game_scene: include the HUD (visible window)"`
	Cols        int       `json:"cols,omitempty" jsonschema:"stop/read: contact-sheet columns (default 8 / 6)"`
	DrawLabels  bool      `json:"draw_labels,omitempty" jsonschema:"stop: stamp each frame's world time"`
	MarkCells   []int     `json:"mark_cells,omitempty" jsonschema:"stop: frame indices to outline in red"`
	Path        string    `json:"path,omitempty" jsonschema:"read: a frames directory instead of a session"`
	Glob        string    `json:"glob,omitempty" jsonschema:"read: file glob (default *)"`
	All         bool      `json:"all,omitempty" jsonschema:"clear: delete EVERY MCP capture"`
}

func captureSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "start", Summary: "start an in-editor frame + state recorder", Tier: spec.Ephemeral, Reaches: []string{"capture_start"},
			Rejects: []string{"cols", "draw_labels", "mark_cells", "path", "glob", "all"}},
		{Name: "status", Summary: "frames so far (does not block)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"session"}, Reaches: []string{"capture_poll"}},
		{Name: "stop", Summary: "stop; contact sheet + timeline", Tier: spec.Ephemeral, Required: []string{"session"}, Reaches: []string{"capture_stop"}},
		{Name: "read", Summary: "contact sheet of frames already on disk", Tier: spec.ReadOnly, Idempotent: true},
		{Name: "clear", Summary: "delete capture files (server-owned dirs only)", Tier: spec.Ephemeral, Idempotent: true},
	}
	return &spec.Spec{
		Name: "capture", Title: "Record frames", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Film the world: an in-editor recorder saves a frame + state every interval_s.\n- start → session.\n- status.\n- stop: ONE contact sheet + a timeline (world time, state, cell).\n- read: a past session or a `path` of frames.\n- clear: a session, or all=true (Saved/MCP/capture only).",
		Schema: spec.SchemaFor[captureIn](map[string][]any{"op": spec.OpEnum(ops...), "world": {"editor", "pie"},
			"source": {"scene_capture", "pie_highres", "game_scene"}, "camera_mode": {"viewport", "fixed", "actor", "player"}}, "op"),
		Replaces: []string{"capture_start", "capture_status", "capture_stop", "capture_clear", "read_capture"},
		Handler:  captureHandler,
	}
}

func captureHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in captureIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Session != "" && !snapshot.ValidName(in.Session) {
		return nil, envelope.New(envelope.InvalidArgument, "session %q: use 1-64 letters, digits, '_' or '-'", in.Session)
	}
	switch c.Op.Name {
	case "start":
		args := pick(c.Args, "session", "source", "interval_s", "cell_width", "cell_height", "max_frames", "max_seconds", "include_ui", "track_actors")
		args["world"] = orStr(in.World, "editor")
		cam := rename(map[string]any{}, c.Args, "camera_mode", "mode", "camera_actor", "actor_label", "location", "location", "rotation", "rotation_pyr", "camera_fov", "fov")
		if len(cam) > 0 {
			args["camera"] = cam
		}
		if obs := pick(c.Args, "include", "exclude", "properties"); len(obs) > 0 {
			args["observe"] = obs
		}
		out, err := v2Op(ctx, c, "capture_start", args)
		return &spec.Result{Data: out, Summary: fmt.Sprintf("recording session %v", out["session"])}, err
	case "status":
		out, err := v2Op(ctx, c, "capture_poll", map[string]any{"session": in.Session})
		return &spec.Result{Data: out, Summary: fmt.Sprintf("%v frames", out["frames_captured"])}, err
	case "stop":
		b, err := v2Bridge(c)
		if err != nil {
			return nil, err
		}
		raw, err := b.Call(ctx, "capture_stop", map[string]any{"session": in.Session})
		if err != nil {
			return nil, err
		}
		var r captureStopResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if len(r.Frames) == 0 {
			return nil, envelope.New(envelope.OperationFailed, "the capture produced no frames").
				WithHint("the recorder saw no ticks — was the editor/PIE ticking (not minimized or paused)?")
		}
		png, sidecar, err := montage(r.Dir, r.Frames, orDefaultInt(in.Cols, 8), in.DrawLabels, in.MarkCells)
		if err != nil {
			return nil, err
		}
		sidecar["session"], sidecar["stop_reason"] = in.Session, r.StopReason
		return &spec.Result{Data: sidecar, Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}},
			Summary: fmt.Sprintf("%d frames (%s)", r.FrameCount, r.StopReason)}, nil
	case "read":
		return captureRead(c, in)
	}
	return captureClear(c, in)
}

func captureRoot(c *spec.Call) (string, error) {
	dir, err := projectDir(c)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Saved", "MCP", "capture"), nil
}

func captureRead(c *spec.Call, in captureIn) (*spec.Result, error) {
	dir := in.Path
	if dir == "" {
		if in.Session == "" {
			return nil, envelope.New(envelope.InvalidArgument, "capture op=read needs session or path")
		}
		root, err := captureRoot(c)
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(root, in.Session)
	}
	matches, err := filepath.Glob(filepath.Join(dir, orStr(in.Glob, "*")))
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "glob: %v", err)
	}
	var frames []captureFrame
	for _, m := range matches {
		if filepath.Ext(m) == ".json" {
			continue // manifest
		}
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() && fi.Size() > 0 {
			frames = append(frames, captureFrame{Index: len(frames), File: filepath.Base(m)})
		}
	}
	if len(frames) == 0 {
		return nil, envelope.New(envelope.NotFound, "no frames in %s", dir)
	}
	png, sidecar, err := montage(dir, frames, orDefaultInt(in.Cols, 6), false, nil)
	if err != nil {
		return nil, err
	}
	return &spec.Result{Data: sidecar, Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}},
		Summary: fmt.Sprintf("%d frames from %s", len(frames), dir)}, nil
}

func captureClear(c *spec.Call, in captureIn) (*spec.Result, error) {
	if (in.Session == "") == !in.All {
		return nil, envelope.New(envelope.InvalidArgument, "capture op=clear needs exactly one of session or all=true")
	}
	root, err := captureRoot(c)
	if err != nil {
		return nil, err
	}
	shots := filepath.Join(filepath.Dir(filepath.Dir(root)), "Screenshots") // Saved/Screenshots: pie_highres frames of builds before P7
	var targets []string
	if in.Session != "" {
		targets = append(targets, filepath.Join(root, in.Session))
		frame := regexp.MustCompile(`^mcp_` + regexp.QuoteMeta(in.Session) + `_f\d+\.png$`) // not session "a_b"'s files
		ms, _ := filepath.Glob(filepath.Join(shots, "mcp_"+in.Session+"_*"))
		for _, m := range ms {
			if frame.MatchString(filepath.Base(m)) {
				targets = append(targets, m)
			}
		}
	} else {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			targets = append(targets, filepath.Join(root, e.Name()))
		}
		ms, _ := filepath.Glob(filepath.Join(shots, "mcp_*"))
		targets = append(targets, ms...)
	}
	removed := 0
	for _, t := range targets {
		if _, err := os.Lstat(t); err == nil && os.RemoveAll(t) == nil {
			removed++
		}
	}
	return &spec.Result{Data: map[string]any{"removed": removed, "capture_dir": root}, Summary: fmt.Sprintf("removed %d capture entries", removed)}, nil
}

// --- scene / scene_clear ---------------------------------------------------------

type sceneIn struct {
	Op        string          `json:"op" jsonschema:"apply | check | preview | env_preset"`
	Path      string          `json:"path,omitempty" jsonschema:"apply/check: an unreal.scene/v1 spec file"`
	JSON      string          `json:"json,omitempty" jsonschema:"apply/check: the spec as inline JSON (instead of path)"`
	DryRun    bool            `json:"dry_run,omitempty" jsonschema:"apply: report add/update/missing assets without changing the level"`
	Save      *bool           `json:"save,omitempty" jsonschema:"apply/env_preset: save afterwards (default true)"`
	Checks    map[string]bool `json:"checks,omitempty" jsonschema:"check: {require_environment_lit, require_no_missing_meshes, require_player_start, require_nav_bounds, spawns_within_bounds: bool}"`
	Layout    map[string]any  `json:"layout,omitempty" jsonschema:"preview: {type: grid|ring|line|scatter, count, spacing, rows, cols, radius, start, end, center, extent, seed}"`
	Preset    string          `json:"preset,omitempty" jsonschema:"env_preset: daytime_clear | overcast | dusk | night | studio"`
	Overrides map[string]any  `json:"overrides,omitempty" jsonschema:"env_preset: e.g. {sun_rotation_pyr: [-45, 30, 0], sun_intensity_lux: 75000, exposure_ev100: 11}"`
	SceneID   string          `json:"scene_id,omitempty" jsonschema:"env_preset: scene id for the environment actors (default env)"`
}

func sceneSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "apply", Summary: "realize a scene spec (additive; one undo step)", Tier: spec.Mutating, Idempotent: true, Rejects: []string{"prune"}, Reaches: []string{"scene_apply", "scene_actors"}},
		{Name: "check", Summary: "lint the level's design invariants", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"design_probe"}},
		{Name: "preview", Summary: "where a layout would place instances (offline)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"layout"}},
		{Name: "env_preset", Summary: "apply a lighting/sky/exposure preset", Tier: spec.Mutating, Idempotent: true, Required: []string{"preset"}, Reaches: []string{"scene_apply"}},
	}
	return &spec.Spec{
		Name: "scene", Title: "Declarative scenes", Toolset: spec.World, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Build levels from an unreal.scene/v1 spec (`path` or `json`).\n- apply: create/update the spec's actors (one undo step) and save; dry_run reports the diff. Additive: matched by the scene tag, never by label alone; stale actors go via scene_clear op=prune.\n- check: lint the level (lit, meshes present, PlayerStart, nav).\n- preview: offline layout placements.\n- env_preset: lighting/sky/exposure preset (sun always points down).",
		Schema: spec.SchemaFor[sceneIn](map[string][]any{"op": spec.OpEnum(ops...),
			"preset": {"daytime_clear", "overcast", "dusk", "night", "studio"}}, "op"),
		Replaces: []string{"scene_apply", "scene_plan", "design_check", "layout_preview", "env_preset_apply"},
		Handler:  sceneHandler,
	}
}

// loadScene parses and compiles a spec from path/json; diagnostics with errors are
// an INVALID_ARGUMENT carrying them.
func loadScene(path, inline string) (*scenespec.Spec, scenespec.Plan, []map[string]any, error) {
	var data []byte
	switch {
	case path != "" && inline != "":
		return nil, scenespec.Plan{}, nil, envelope.New(envelope.InvalidArgument, "give the spec as path or json, not both")
	case path != "":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, scenespec.Plan{}, nil, envelope.New(envelope.NotFound, "spec file: %v", err)
		}
		data = b
	case inline != "":
		data = []byte(inline)
	default:
		return nil, scenespec.Plan{}, nil, envelope.New(envelope.InvalidArgument, "the spec is required: path or json")
	}
	sp, diags, err := scenespec.Parse(data)
	if err != nil {
		return nil, scenespec.Plan{}, nil, envelope.New(envelope.InvalidArgument, "parse spec: %v", err)
	}
	plan, cdiags := scenespec.Compile(sp)
	diags = append(diags, cdiags...)
	if hasErrorDiag(diags) {
		return nil, plan, nil, envelope.New(envelope.InvalidArgument, "the spec has errors; nothing was applied").WithDetail("diagnostics", diagsToJSON(diags))
	}
	return sp, plan, diagsToJSON(diags), nil
}

func sceneHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in sceneIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "preview":
		var layout scenespec.Layout
		raw, _ := json.Marshal(in.Layout)
		if err := strictDecode(raw, &layout); err != nil {
			return nil, err
		}
		pts := scenespec.Expand(layout, scenespec.Transform{Scale: [3]float64{1, 1, 1}})
		out := make([]map[string]any, len(pts))
		for i, p := range pts {
			out[i] = map[string]any{"location": p.Location, "rotation": p.RotationPyr}
		}
		return &spec.Result{Data: map[string]any{"count": len(out), "instances": out}, Summary: fmt.Sprintf("%d placements", len(out))}, nil
	case "check":
		checks := scenespec.Checks{RequireEnvironmentLit: true, RequireNoMissingMeshes: true, RequirePlayerStart: true}
		if in.Checks != nil {
			raw, _ := json.Marshal(in.Checks)
			checks = scenespec.Checks{}
			if err := strictDecode(raw, &checks); err != nil {
				return nil, err
			}
		} else if in.Path != "" || in.JSON != "" {
			sp, _, _, err := loadScene(in.Path, in.JSON)
			if err != nil {
				return nil, err
			}
			checks = sp.Checks
		}
		b, err := v2Bridge(c)
		if err != nil {
			return nil, err
		}
		raw, err := b.Call(ctx, "design_probe", map[string]any{})
		if err != nil {
			return nil, err
		}
		var facts scenespec.ProbeFacts
		if err := json.Unmarshal(raw, &facts); err != nil {
			return nil, err
		}
		rep := scenespec.EvaluateChecks(checks, facts)
		return &spec.Result{Data: map[string]any{"ok": rep.OK, "results": rep.Results}, Summary: map[bool]string{true: "all checks pass", false: "checks failed"}[rep.OK]}, nil
	case "env_preset":
		sp := &scenespec.Spec{Schema: "unreal.scene/v1", SceneID: orStr(in.SceneID, "env"),
			Environment: &scenespec.Environment{Preset: in.Preset, Overrides: in.Overrides}}
		plan, diags := scenespec.Compile(sp)
		if hasErrorDiag(diags) {
			return nil, envelope.New(envelope.InvalidArgument, "the preset produced errors; nothing was applied").WithDetail("diagnostics", diagsToJSON(diags))
		}
		return sceneApplyPlan(ctx, c, plan, in.Save, diagsToJSON(diags))
	}
	sp, plan, diags, err := loadScene(in.Path, in.JSON)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		before, err := sceneActors(ctx, c, sp.SceneID)
		if err != nil {
			return nil, err
		}
		d := scenespec.Diff(sp.SceneID, plan, before)
		return &spec.Result{Data: map[string]any{"dry_run": true, "scene_id": sp.SceneID, "diff": d, "diagnostics": diags, "placements": len(plan.Placements)},
			Summary: fmt.Sprintf("would add %d, update %d", len(d.Add), len(d.Update))}, nil
	}
	return sceneApplyPlan(ctx, c, plan, in.Save, diags)
}

// sceneActors is the scene's tagged actors by label (what apply would update).
func sceneActors(ctx context.Context, c *spec.Call, sceneID string) (map[string]scenespec.SnapActor, error) {
	out, err := v2Op(ctx, c, "scene_actors", map[string]any{"scene_id": sceneID})
	if err != nil {
		return nil, err
	}
	res := map[string]scenespec.SnapActor{}
	list, _ := out["actors"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		label, _ := m["label"].(string)
		class, _ := m["class"].(string)
		res[label] = scenespec.SnapActor{Location: anyVec3(m["location"]), Class: class}
	}
	return res, nil
}

func sceneApplyPlan(ctx context.Context, c *spec.Call, plan scenespec.Plan, save *bool, diags []map[string]any) (*spec.Result, error) {
	args := map[string]any{"scene_id": plan.SceneID, "placements": plan.Placements}
	if save != nil {
		args["save"] = *save
	}
	out, err := v2Op(ctx, c, "scene_apply", args)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		out["diagnostics"] = diags
	}
	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		// Hard placement failures: nothing was saved. Partial content may exist (one undo step).
		return nil, withLog(envelope.New(envelope.OperationFailed, "%d placement(s) failed; the level was not saved", len(errs)).
			WithDetail("result", out), out)
	}
	return &spec.Result{Data: out, Summary: fmt.Sprintf("scene %s: %v spawned, %v updated", plan.SceneID, out["spawned"], out["updated"])}, nil
}

type sceneClearIn struct {
	Op      string `json:"op" jsonschema:"all | prune"`
	SceneID string `json:"scene_id,omitempty" jsonschema:"all: the scene whose actors to delete"`
	Path    string `json:"path,omitempty" jsonschema:"prune: the scene spec file (its scene_id and labels are kept)"`
	JSON    string `json:"json,omitempty" jsonschema:"prune: the spec as inline JSON"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"list what would be deleted without deleting"`
	Save    *bool  `json:"save,omitempty" jsonschema:"save afterwards (default true)"`
}

func sceneClearSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "all", Summary: "delete every actor of a scene", Tier: spec.Destructive, Required: []string{"scene_id"}, Rejects: []string{"path", "json"}, Reaches: []string{"scene_clear", "scene_actors"}},
		{Name: "prune", Summary: "delete the scene's actors that are not in the spec", Tier: spec.Destructive, Rejects: []string{"scene_id"}, Reaches: []string{"scene_prune", "scene_actors"}},
	}
	return &spec.Spec{
		Name: "scene_clear", Title: "Remove scene actors", Toolset: spec.World, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Delete actors a scene created (tagged mcp_scene:<id> only; hand-placed actors never), one undo step, then save.\n- all: every actor of `scene_id`.\n- prune: the scene's actors not in the spec (`path`/`json`) — after scene apply; not atomic with it.\ndry_run lists them.",
		Schema:      spec.SchemaFor[sceneClearIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"scene_clear"},
		Handler:     sceneClear,
	}
}

func sceneClear(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in sceneClearIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	sceneID, keep := in.SceneID, map[string]bool{}
	if c.Op.Name == "prune" {
		sp, plan, _, err := loadScene(in.Path, in.JSON)
		if err != nil {
			return nil, err
		}
		sceneID = sp.SceneID
		for _, p := range plan.Placements {
			keep[p.Label] = true
		}
	}
	if in.DryRun {
		cur, err := sceneActors(ctx, c, sceneID)
		if err != nil {
			return nil, err
		}
		would := []string{}
		for label := range cur {
			if !keep[label] {
				would = append(would, label)
			}
		}
		sort.Strings(would)
		return &spec.Result{Data: map[string]any{"dry_run": true, "scene_id": sceneID, "would_delete": would},
			Summary: fmt.Sprintf("would delete %d actors", len(would))}, nil
	}
	args := map[string]any{"scene_id": sceneID}
	if in.Save != nil {
		args["save"] = *in.Save
	}
	if c.Op.Name == "all" {
		out, err := v2Op(ctx, c, "scene_clear", args)
		return &spec.Result{Data: out, Summary: fmt.Sprintf("removed %v actors of %s", out["removed"], sceneID)}, err
	}
	labels := make([]string, 0, len(keep))
	for l := range keep {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	args["keep"] = labels
	out, err := v2Op(ctx, c, "scene_prune", args)
	if err != nil {
		return nil, err
	}
	pruned, _ := out["pruned"].([]any)
	return &spec.Result{Data: out, Summary: fmt.Sprintf("pruned %d actors of %s", len(pruned), sceneID)}, nil
}

// --- audio -----------------------------------------------------------------------

type audioIn struct {
	Op      string  `json:"op" jsonschema:"capture_start | capture_stop | play"`
	Session string  `json:"session,omitempty" jsonschema:"capture_start: a session name"`
	Sound   string  `json:"sound,omitempty" jsonschema:"play: a sound asset path"`
	Volume  float64 `json:"volume,omitempty" jsonschema:"play: volume multiplier (default 1)"`
}

func audioSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "capture_start", Summary: "tap the main submix (RMS/peak envelope)", Tier: spec.Ephemeral, Reaches: []string{"audio_capture_start"}, Needs: []string{"pie", "plugin"}},
		{Name: "capture_stop", Summary: "stop; write the envelope (Saved/MCP/audio)", Tier: spec.Ephemeral, Reaches: []string{"audio_capture_stop"}, Needs: []string{"pie", "plugin"}},
		{Name: "play", Summary: "play a sound into the game (a test signal)", Tier: spec.Ephemeral, Required: []string{"sound"}, Reaches: []string{"play_test_sound"}, Needs: []string{"pie"}},
	}
	return &spec.Spec{
		Name: "audio", Title: "Game audio", Toolset: spec.Core, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Listen to the running game (PIE only, UnrealMCP plugin).\n- capture_start: record the main submix envelope.\n- capture_stop → {path, points, max_rms, duration} for design_audit kind=audio.\n- play `sound` as a test signal.",
		Schema:      spec.SchemaFor[audioIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"audio_capture_start", "audio_capture_stop", "play_test_sound"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			py := map[string]string{"capture_start": "audio_capture_start", "capture_stop": "audio_capture_stop", "play": "play_test_sound"}[c.Op.Name]
			out, err := v2Op(ctx, c, py, pick(c.Args, "session", "sound", "volume"))
			return &spec.Result{Data: out, Summary: "audio " + c.Op.Name}, err
		},
	}
}

// awaitShot finds the file HighResShot actually wrote for `want` (an absolute path from
// the companion): it may add a suffix, so look for <stem>*.png in the directory and
// its subdirectories.
func awaitShot(want string, wait time.Duration) string {
	dir, stem := filepath.Dir(want), strings.TrimSuffix(filepath.Base(want), ".png")
	deadline := time.Now().Add(wait)
	for {
		for _, pat := range []string{filepath.Join(dir, stem+"*.png"), filepath.Join(dir, "*", stem+"*.png")} {
			if ms, _ := filepath.Glob(pat); len(ms) > 0 {
				return ms[0]
			}
		}
		if time.Now().After(deadline) {
			return want
		}
		time.Sleep(200 * time.Millisecond)
	}
}
