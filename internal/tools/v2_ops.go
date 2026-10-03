package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/build"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/headless"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
	"github.com/jdziat/unreal-mcp-server/internal/perf"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

// v2 job / logs / analyze / git / headless / playtest tools (OVERHAUL_PLAN.md §2.3
// rows 29–33, 38).

func opsSpecs() []*spec.Spec {
	return []*spec.Spec{jobSpec(), logsSpec(), analyzeSpec(), gitSpec(), headlessSpec(), playtestSpec()}
}

func jobsOf(c *spec.Call) (*jobs.Registry, error) {
	if c.Deps.Jobs == nil {
		return nil, envelope.New(envelope.Precondition, "no job registry for this session")
	}
	return c.Deps.Jobs, nil
}

// --- job -------------------------------------------------------------------------

type jobIn struct {
	Op    string  `json:"op" jsonschema:"status | wait | cancel | list"`
	JobID string  `json:"job_id,omitempty" jsonschema:"status/wait/cancel: the job"`
	WaitS float64 `json:"wait_s,omitempty" jsonschema:"wait: seconds (default and max 25)"`
}

func jobSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "status", Summary: "state, last progress line, result or error", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"job_id"}, Rejects: []string{"wait_s"}},
		{Name: "wait", Summary: "wait for completion (streams progress)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"job_id"}},
		{Name: "cancel", Summary: "request cancellation", Tier: spec.Ephemeral, Idempotent: true, Required: []string{"job_id"}},
		{Name: "list", Summary: "this project's jobs", Tier: spec.ReadOnly, Idempotent: true},
	}
	return &spec.Spec{
		Name: "job", Title: "Background jobs", Toolset: spec.Core, Offline: true, Timeout: sync28, Max: sync28, Ops: ops,
		Description: "Follow async work (build, playtest run, editor_lifecycle, git_revert, headless), which returns {job_id, state} unless called with wait_s.\n- status: state, last progress, result or error.\n- wait: up to wait_s (default 25), streaming progress.\n- cancel.\n- list: this project's jobs (any session of the project can poll them).",
		Schema:      spec.SchemaFor[jobIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"job_status", "job_cancel"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			reg, err := jobsOf(c)
			if err != nil {
				return nil, err
			}
			if c.Op.Name == "list" {
				var out []map[string]any
				for _, sn := range reg.List() {
					out = append(out, spec.JobView(sn))
				}
				return &spec.Result{Data: map[string]any{"jobs": out}, Summary: fmt.Sprintf("%d jobs", len(out))}, nil
			}
			id, _ := c.Args["job_id"].(string)
			j, ok := reg.Get(id)
			if !ok {
				return nil, envelope.New(envelope.NotFound, "no job %q in this project", id).WithHint("job op=list shows them")
			}
			switch c.Op.Name {
			case "cancel":
				j.Cancel()
			case "wait":
				if _, ok := c.Args["wait_s"]; !ok {
					c.Args["wait_s"] = spec.MaxWait.Seconds()
				}
			}
			return &spec.Result{Job: j}, nil
		},
	}
}

// --- logs ------------------------------------------------------------------------

type logsIn struct {
	Op          string   `json:"op" jsonschema:"mark | tail | since | events"`
	Marker      string   `json:"marker,omitempty" jsonschema:"since: a marker from op=mark; events: an offset from a previous events call"`
	Lines       int      `json:"lines,omitempty" jsonschema:"tail: how many lines (default 200)"`
	MinSeverity string   `json:"min_severity,omitempty" jsonschema:"tail/since: minimum severity (default Display / Warning)"`
	Categories  []string `json:"categories,omitempty" jsonschema:"tail: only these log categories, e.g. LogLiveCoding"`
	Type        string   `json:"type,omitempty" jsonschema:"events: only this event type (e.g. issue)"`
	Limit       int      `json:"limit,omitempty" jsonschema:"events: most recent N (default 200)"`
}

func logsSpec() *spec.Spec {
	ro := func(name, summary string, req ...string) spec.OpSpec {
		return spec.OpSpec{Name: name, Summary: summary, Tier: spec.ReadOnly, Idempotent: true, Required: req, Needs: []string{"project"}}
	}
	ops := []spec.OpSpec{
		ro("mark", "a marker into the editor log, for op=since"),
		ro("tail", "recent log lines by severity/category"),
		ro("since", "lines since a marker + error/warning/ensure counts", "marker"),
		ro("events", "the editor's structured event stream (issues, PIE transitions)"),
	}
	return &spec.Spec{
		Name: "logs", Title: "Editor logs", Toolset: spec.Core, Offline: true, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Read the editor log files (works while the editor is busy or gone).\n- mark → marker; later since `marker` → those lines + error/warning/ensure counts.\n- tail: last `lines` at min_severity.\n- events: structured events from offset `marker`.",
		Schema:      spec.SchemaFor[logsIn](map[string][]any{"op": spec.OpEnum(ops...), "min_severity": {"Verbose", "Log", "Display", "Warning", "Error"}}, "op"),
		Replaces:    []string{"logs_mark", "logs_tail", "logs_since", "editor_events"},
		Handler:     logsHandler,
	}
}

func logsHandler(_ context.Context, c *spec.Call) (*spec.Result, error) {
	var in logsIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	dir, err := projectDir(c)
	if err != nil {
		return nil, err
	}
	path := logs.LogPath(dir)
	switch c.Op.Name {
	case "mark":
		m := strconv.FormatInt(logs.LogSize(path), 10)
		return &spec.Result{Data: map[string]any{"marker": m}, Summary: "marker " + m}, nil
	case "tail":
		text, _, err := logs.ReadFrom(path, 0)
		if err != nil {
			return nil, envelope.New(envelope.NotFound, "editor log: %v", err)
		}
		lines := logs.FilterLines(text, orStr(in.MinSeverity, "Display"), in.Categories)
		if n := orDefaultInt(in.Lines, 200); len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return &spec.Result{Data: map[string]any{"lines": lines}, Summary: fmt.Sprintf("%d lines", len(lines))}, nil
	case "since":
		off, perr := strconv.ParseInt(in.Marker, 10, 64)
		if perr != nil || off < 0 {
			return nil, envelope.New(envelope.InvalidArgument, "marker %q is not a value from logs op=mark", in.Marker)
		}
		text, _, err := logs.ReadFrom(path, off)
		if err != nil {
			return nil, envelope.New(envelope.NotFound, "editor log: %v", err)
		}
		lines := logs.FilterLines(text, orStr(in.MinSeverity, "Warning"), nil)
		e, w, en := logs.CountBySeverity(lines)
		return &spec.Result{Data: map[string]any{"lines": lines, "errors": e, "warnings": w, "ensures": en},
			Summary: fmt.Sprintf("%d errors, %d warnings, %d ensures", e, w, en)}, nil
	}
	var off int64
	if in.Marker != "" {
		if off, err = strconv.ParseInt(in.Marker, 10, 64); err != nil {
			return nil, envelope.New(envelope.InvalidArgument, "marker %q is not an events offset", in.Marker)
		}
	}
	evs, next, err := logs.Tail(logs.Path(dir), off)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(evs))
	for _, e := range evs {
		if in.Type == "" || e.Type == in.Type {
			out = append(out, e.Raw)
		}
	}
	if n := orDefaultInt(in.Limit, 200); len(out) > n {
		out = out[len(out)-n:]
	}
	return &spec.Result{Data: map[string]any{"events": out, "marker": strconv.FormatInt(next, 10), "count": len(out)},
		Summary: fmt.Sprintf("%d events", len(out))}, nil
}

// --- analyze ---------------------------------------------------------------------

type analyzeIn struct {
	Op       string             `json:"op" jsonschema:"rubric | perf | image_diff | scenarios"`
	Timeline []timelineFrame    `json:"timeline,omitempty" jsonschema:"rubric: recorded frames [{index, t_world, state}] (a playtest result's timeline)"`
	Logs     *logCounts         `json:"logs,omitempty" jsonschema:"rubric: {errors, warnings, ensures} for log checks"`
	Rubric   []eval.RubricCheck `json:"rubric,omitempty" jsonschema:"rubric: [{id, kind, path, params?, severity?}]"`
	Path     string             `json:"path,omitempty" jsonschema:"perf: a CsvProfiler .csv or a .memreport; image_diff: an image"`
	Baseline string             `json:"baseline,omitempty" jsonschema:"image_diff: the image to compare against"`
	HitchMs  float64            `json:"hitch_ms,omitempty" jsonschema:"perf: frames slower than this are hitches (default 33.3)"`
	MaxDHash *int               `json:"max_dhash,omitempty" jsonschema:"image_diff: pass threshold on dHash distance (default 8; 0 = exact)"`
	MaxLuma  *float64           `json:"max_luma_delta,omitempty" jsonschema:"image_diff: pass threshold on mean-luma delta (default 0.15)"`
	Dir      string             `json:"dir,omitempty" jsonschema:"scenarios: directory of scenario/v1 files (default <project>/.mcp/scenarios)"`
}

// timelineFrame / logCounts are the JSON shapes of a playtest timeline and its log
// summary (the eval types carry no json tags).
type timelineFrame struct {
	Index  int            `json:"index"`
	TWorld float64        `json:"t_world"`
	State  map[string]any `json:"state"`
}

type logCounts struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Ensures  int `json:"ensures"`
}

func analyzeSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "rubric", Summary: "score a recorded timeline against checks", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"timeline", "rubric"}},
		{Name: "perf", Summary: "frame-time percentiles / memory buckets", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"path"}},
		{Name: "image_diff", Summary: "perceptual compare with a pass verdict", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"path", "baseline"}},
		{Name: "scenarios", Summary: "the saved playtest suite: name + valid per file", Tier: spec.ReadOnly, Idempotent: true},
	}
	return &spec.Spec{
		Name: "analyze", Title: "Analyze results offline", Toolset: spec.Core, Offline: true, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Score evidence offline.\n- rubric: re-score a playtest `timeline` → PASS|WARN|FAIL with frame evidence.\n- perf: CsvProfiler CSV → frame-time percentiles + hitches; .memreport → memory buckets.\n- image_diff: `path` vs `baseline` → hash distance, luma delta, pass.\n- scenarios: the saved playtest suite.",
		Schema:      spec.SchemaFor[analyzeIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"playtest_evaluate", "perf_parse", "image_compare", "scenario_list"},
		Handler:     analyze,
	}
}

func analyze(_ context.Context, c *spec.Call) (*spec.Result, error) {
	var in analyzeIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "scenarios":
		dir := in.Dir
		if dir == "" {
			pd, err := projectDir(c)
			if err != nil {
				return nil, err
			}
			dir = filepath.Join(pd, ".mcp", "scenarios")
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		out := []map[string]any{}
		for _, p := range matches {
			item := map[string]any{"file": p, "valid": false}
			if data, err := os.ReadFile(p); err == nil {
				if sc, diags, err := eval.ParseScenario(data); err == nil {
					item["name"], item["valid"] = sc.Name, !eval.HasErrors(diags)
					if eval.HasErrors(diags) {
						item["diagnostics"] = diags
					}
				}
			}
			out = append(out, item)
		}
		return &spec.Result{Data: map[string]any{"dir": dir, "scenarios": out}, Summary: fmt.Sprintf("%d scenarios", len(out))}, nil
	case "rubric":
		var ls eval.LogSummary
		if in.Logs != nil {
			ls = eval.LogSummary{Errors: in.Logs.Errors, Warnings: in.Logs.Warnings, Ensures: in.Logs.Ensures}
		}
		samples := make([]eval.Sample, len(in.Timeline))
		for i, f := range in.Timeline {
			samples[i] = eval.Sample{Index: f.Index, TWorld: f.TWorld, State: f.State}
		}
		rep := eval.Evaluate(samples, ls, scenarioRubric(in.Rubric))
		return &spec.Result{Data: reportToJSON(rep), Summary: "verdict " + rep.Verdict}, nil
	case "perf":
		data, err := os.ReadFile(in.Path)
		if err != nil {
			return nil, envelope.New(envelope.NotFound, "%v", err)
		}
		if strings.Contains(strings.ToLower(filepath.Base(in.Path)), "memreport") {
			return &spec.Result{Data: map[string]any{"memory": perf.ParseMemReport(string(data))}, Summary: "memory report"}, nil
		}
		fs, err := perf.ParseCSV(string(data), in.HitchMs)
		if err != nil {
			return nil, envelope.New(envelope.InvalidArgument, "not a CsvProfiler CSV: %v", err)
		}
		return &spec.Result{Data: map[string]any{"frames": fs}, Summary: "frame stats"}, nil
	}
	a, err := visual.Load(in.Path)
	if err != nil {
		return nil, envelope.New(envelope.NotFound, "path: %v", err)
	}
	b, err := visual.Load(in.Baseline)
	if err != nil {
		return nil, envelope.New(envelope.NotFound, "baseline: %v", err)
	}
	res := visual.Compare(a, b)
	maxD, maxL := 8, 0.15
	if in.MaxDHash != nil {
		maxD = *in.MaxDHash
	}
	if in.MaxLuma != nil {
		maxL = *in.MaxLuma
	}
	pass := res.DHashDist <= maxD && res.LumaDelta <= maxL
	return &spec.Result{Data: map[string]any{"pass": pass, "dhash_dist": res.DHashDist, "ahash_dist": res.AHashDist,
		"luma_delta": res.LumaDelta, "luma_a": res.LumaA, "luma_b": res.LumaB},
		Summary: map[bool]string{true: "images match", false: "images differ"}[pass]}, nil
}

// --- git -------------------------------------------------------------------------

type gitIn struct {
	Op      string   `json:"op" jsonschema:"status | diff | log | checkpoint"`
	Paths   []string `json:"paths,omitempty" jsonschema:"diff: limit to these; checkpoint: stage only these"`
	Limit   int      `json:"limit,omitempty" jsonschema:"log: commits (default 20)"`
	Message string   `json:"message,omitempty" jsonschema:"checkpoint: commit message"`
}

func gitSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "status", Summary: "branch, staged, unstaged, untracked", Tier: spec.ReadOnly, Idempotent: true, Needs: []string{"project"}},
		{Name: "diff", Summary: "working-tree diff", Tier: spec.ReadOnly, Idempotent: true, Needs: []string{"project"}},
		{Name: "log", Summary: "recent commits + checkpoint tags", Tier: spec.ReadOnly, Idempotent: true, Needs: []string{"project"}},
		{Name: "checkpoint", Summary: "commit + tag umcp/cp/<n>", Tier: spec.Mutating, Required: []string{"message"}, Needs: []string{"project"}},
	}
	return &spec.Spec{
		Name: "git", Title: "Project git", Toolset: spec.Core, Offline: true, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "The project's git repo (no editor).\n- status / diff / log (+ checkpoints).\n- checkpoint: stage (default all but Saved/Intermediate/DerivedDataCache), commit `message` (hooks run) and tag umcp/cp/<n>; nothing to commit tags HEAD. git_revert only goes back to these.",
		Schema:      spec.SchemaFor[gitIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"git_status", "git_diff", "git_log", "git_checkpoint"},
		Handler:     gitHandler,
	}
}

var cpTag = regexp.MustCompile(`^umcp/cp/(\d+)$`)

// checkpoints lists the umcp/cp/<n> tags, highest n first.
func checkpoints(ctx context.Context, dir string) ([]string, error) {
	out, err := build.Run(ctx, dir, "tag", "--list", "umcp/cp/*")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, t := range strings.Fields(out) {
		if cpTag.MatchString(t) {
			tags = append(tags, t)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return cpNum(tags[i]) > cpNum(tags[j]) })
	return tags, nil
}

func cpNum(tag string) int {
	m := cpTag.FindStringSubmatch(tag)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func gitDir(ctx context.Context, c *spec.Call) (string, error) {
	dir, err := projectDir(c)
	if err != nil {
		return "", err
	}
	if !build.IsRepo(ctx, dir) {
		return "", envelope.New(envelope.Precondition, "%s is not a git repository", dir)
	}
	return dir, nil
}

func gitFail(op string, err error) *envelope.Error {
	return envelope.New(envelope.OperationFailed, "git %s: %v", op, err)
}

func gitHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in gitIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	dir, err := gitDir(ctx, c)
	if err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "status":
		out, err := build.Run(ctx, dir, "status", "--porcelain=v1", "--branch")
		if err != nil {
			return nil, gitFail("status", err)
		}
		st := parseStatus(out)
		return &spec.Result{Data: st, Summary: fmt.Sprintf("%s: %d staged, %d unstaged, %d untracked", st.Branch, len(st.Staged), len(st.Unstaged), len(st.Untracked))}, nil
	case "diff":
		args := []string{"diff"}
		if len(in.Paths) > 0 {
			args = append(append(args, "--"), in.Paths...)
		}
		out, err := build.Run(ctx, dir, args...)
		if err != nil {
			return nil, gitFail("diff", err)
		}
		return &spec.Result{Data: map[string]any{"diff": out}, Summary: fmt.Sprintf("%d diff lines", strings.Count(out, "\n"))}, nil
	case "log":
		out, err := build.Run(ctx, dir, "log", "-n", strconv.Itoa(orDefaultInt(in.Limit, 20)), "--pretty=format:%H\x1f%s\x1f%cI")
		if err != nil {
			return nil, gitFail("log", err)
		}
		cps, _ := checkpoints(ctx, dir)
		return &spec.Result{Data: map[string]any{"commits": parseLog(out), "checkpoints": cps}, Summary: fmt.Sprintf("%d checkpoints", len(cps))}, nil
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, envelope.New(envelope.InvalidArgument, "git op=checkpoint needs a message")
	}
	add := []string{"add", "--"}
	if len(in.Paths) > 0 {
		add = append(add, in.Paths...)
	} else {
		add = append(append(add, "."), gitExcludes...)
	}
	if _, err := build.Run(ctx, dir, add...); err != nil {
		return nil, gitFail("add", err)
	}
	committed := true
	// Commit only what this checkpoint staged (the given paths, else the project tree
	// minus generated dirs) — never other changes already staged elsewhere in the repo.
	commit := append([]string{"commit", "-m", in.Message, "--"}, add[2:]...)
	if staged, _ := build.Run(ctx, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) == "" {
		committed = false // nothing new: checkpoint the current HEAD
	} else if _, err := build.Run(ctx, dir, commit...); err != nil {
		return nil, gitFail("commit", err)
	}
	head, err := build.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, gitFail("rev-parse", err).WithHint("a checkpoint needs at least one commit")
	}
	cps, _ := checkpoints(ctx, dir)
	n := 1
	if len(cps) > 0 {
		n = cpNum(cps[0]) + 1
	}
	// A concurrent checkpoint may take n first: on a failed tag, move on only if the
	// name is now taken (checked with rev-parse, independent of git's message language).
	var tag string
	for attempt := 0; ; attempt++ {
		tag = "umcp/cp/" + strconv.Itoa(n+attempt)
		_, terr := build.Run(ctx, dir, "tag", "-a", tag, "-m", in.Message)
		if terr == nil {
			break
		}
		if _, exists := build.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag); attempt == 4 || exists != nil {
			return nil, gitFail("tag", terr)
		}
	}
	return &spec.Result{Data: map[string]any{"commit": strings.TrimSpace(head), "tag": tag, "committed": committed, "message": in.Message},
		Summary: "checkpoint " + tag}, nil
}

// --- headless --------------------------------------------------------------------

type headlessIn struct {
	Op         string   `json:"op" jsonschema:"commandlet | exec | tests"`
	Commandlet string   `json:"commandlet,omitempty" jsonschema:"commandlet: the commandlet to -run, e.g. DataValidation, ResavePackages"`
	Args       []string `json:"args,omitempty" jsonschema:"commandlet: extra arguments"`
	ExecCmds   []string `json:"exec_cmds,omitempty" jsonschema:"exec: console commands to batch (Quit is appended)"`
	Filter     string   `json:"filter,omitempty" jsonschema:"tests: automation test filter, e.g. Project.Functional"`
	TimeoutS   float64  `json:"timeout_s,omitempty" jsonschema:"abort after this many seconds (default 180, max 600)"`
	WithGPU    bool     `json:"with_gpu,omitempty" jsonschema:"render with a GPU (default -nullrhi)"`
	WaitS      float64  `json:"wait_s,omitempty" jsonschema:"wait up to this many seconds (max 25) before returning the job"`
}

func headlessSpec() *spec.Spec {
	op := func(name, summary string, req ...string) spec.OpSpec {
		return spec.OpSpec{Name: name, Summary: summary, Tier: spec.Exec, Async: true, Required: req, Needs: []string{"project", "engine"}}
	}
	ops := []spec.OpSpec{
		op("commandlet", "run a commandlet", "commandlet"),
		op("exec", "run a batch of console commands", "exec_cmds"),
		op("tests", "run automation tests", "filter"),
	}
	return &spec.Spec{
		Name: "headless", Title: "Headless editor runs", Toolset: spec.Headless, Timeout: sync8, Ops: ops,
		Description: "Run work in a SEPARATE UnrealEditor-Cmd process (async job), off the interactive editor's command " +
			"channel: a commandlet, an -ExecCmds batch, or automation tests → exit code, log tail, test pass/fail counts. " +
			"A tests run that matched no test is ok:false.",
		Schema:   spec.SchemaFor[headlessIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"headless_run"},
		Handler:  headlessRun,
	}
}

func headlessRun(_ context.Context, c *spec.Call) (*spec.Result, error) {
	var in headlessIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	dir, err := projectDir(c)
	if err != nil {
		return nil, err
	}
	if c.Deps.EngineDir == "" {
		return nil, envelope.New(envelope.Precondition, "headless needs the engine directory (-engine / UMCP_ENGINE_DIR)")
	}
	reg, err := jobsOf(c)
	if err != nil {
		return nil, err
	}
	uproject, err := uprojectPath(dir)
	if err != nil {
		return nil, envelope.New(envelope.Precondition, "%v", err)
	}
	timeout := min(secs(orDefault(in.TimeoutS, 180)), 600*time.Second)
	cmd := headless.Cmd{Editor: headless.EditorCmdFromEngineDir(c.Deps.EngineDir), Project: uproject,
		NullRHI: !in.WithGPU, Unattended: true}
	switch in.Op {
	case "commandlet":
		cmd.Commandlet = in.Commandlet
		cmd.ExecCmds = nil
		if len(in.Args) > 0 {
			cmd.Commandlet = strings.Join(append([]string{in.Commandlet}, in.Args...), " ")
		}
	case "exec":
		cmd.ExecCmds = in.ExecCmds
	default:
		cmd.RunTests = in.Filter
	}
	j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
		progress("running " + filepath.Base(cmd.Editor) + " " + in.Op)
		rctx, cancel := context.WithTimeout(jctx, timeout)
		defer cancel()
		res, runErr := headless.Run(rctx, cmd)
		out := map[string]any{"exit_code": res.ExitCode, "timed_out": res.TimedOut,
			"ok": res.ExitCode == 0 && !res.TimedOut, "log_tail": lastLines(res.Stdout, 40), "cmdline": res.Cmdline}
		if runErr != nil {
			out["ok"], out["error"] = false, runErr.Error()
		}
		if st := strings.TrimSpace(res.Stderr); st != "" {
			out["stderr_tail"] = lastLines(res.Stderr, 20)
		}
		if in.Op == "tests" {
			tests := summarizeAutomation(res.Stdout)
			out["tests"] = tests
			if tests["passed"] == 0 && tests["failed"] == 0 {
				out["ok"], out["warning"] = false, "no automation test results parsed — check the filter matched any test"
			} else if f, _ := tests["failed"].(int); f > 0 {
				out["ok"] = false
			}
		}
		return out, nil
	})
	return &spec.Result{Job: j}, nil
}

// --- playtest --------------------------------------------------------------------

type playtestIn struct {
	Op         string   `json:"op" jsonschema:"run"`
	Path       string   `json:"path,omitempty" jsonschema:"run: a scenario/v1 .json file"`
	JSON       string   `json:"json,omitempty" jsonschema:"run: the scenario as inline JSON (instead of path)"`
	FrameW     int      `json:"frame_w,omitempty" jsonschema:"run: per-frame width (default 256)"`
	FrameH     int      `json:"frame_h,omitempty" jsonschema:"run: per-frame height (default 144)"`
	MaxFrames  int      `json:"max_frames,omitempty" jsonschema:"run: frame cap (default 96)"`
	Include    []string `json:"include,omitempty" jsonschema:"run: observed property include globs"`
	Properties []string `json:"properties,omitempty" jsonschema:"run: exact observed properties"`
	Cols       int      `json:"cols,omitempty" jsonschema:"run: montage columns (default 8)"`
	WaitS      float64  `json:"wait_s,omitempty" jsonschema:"run: wait up to this many seconds (max 25) before returning the job"`
}

func playtestSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "run", Summary: "play a scenario: frames + state + beats + rubric verdict", Tier: spec.Exec, Async: true,
			Reaches: []string{"open_level", "pie_start", "pie_stop", "console", "actor_set_properties",
				"capture_start", "capture_stop", "actor_call", "pie_observe", "editor_ping"}},
	}
	return &spec.Spec{
		Name: "playtest", Title: "Automated playtest", Toolset: spec.Core, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Validate that the game works (async job). op=run plays a scenario/v1 (`path` or `json`): open the level, play (pie|simulate|editor), record frames + state, run timed beats (exec = call a UFUNCTION, arbitrary code; console; wait_until), stop, score the rubric → {verdict, rubric, logs, crash?, beat_errors?, timeline} plus a contact sheet image via wait_s / job. Saved suite: analyze op=scenarios.",
		Schema:      spec.SchemaFor[playtestIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"playtest_capture", "scenario_run"},
		Handler:     playtestHandler,
	}
}

func playtestHandler(_ context.Context, c *spec.Call) (*spec.Result, error) {
	var in playtestIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	var data []byte
	switch {
	case in.Path != "" && in.JSON != "":
		return nil, envelope.New(envelope.InvalidArgument, "give the scenario as path or json, not both")
	case in.Path != "":
		b, err := os.ReadFile(in.Path)
		if err != nil {
			return nil, envelope.New(envelope.NotFound, "scenario: %v", err)
		}
		data = b
	case in.JSON != "":
		data = []byte(in.JSON)
	default:
		return nil, envelope.New(envelope.InvalidArgument, "playtest op=run needs a scenario: path or json")
	}
	sc, diags, err := eval.ParseScenario(data)
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "parse scenario: %v", err)
	}
	if eval.HasErrors(diags) {
		return nil, envelope.New(envelope.InvalidArgument, "the scenario has errors").WithDetail("diagnostics", diags)
	}
	if _, err := v2Bridge(c); err != nil {
		return nil, err
	}
	reg, err := jobsOf(c)
	if err != nil {
		return nil, err
	}
	j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
		return runPlaytest(jctx, c, sc, in, progress)
	})
	return &spec.Result{Job: j}, nil
}

// runPlaytest is the one playtest orchestration (v1 had it twice: playtest_capture
// and scenario_run).
func runPlaytest(ctx context.Context, c *spec.Call, sc *eval.Scenario, in playtestIn, progress func(string)) (map[string]any, error) {
	mode := orStr(sc.Mode, "pie")
	duration := orDefault(sc.DurationS, 20)
	runStart := time.Now()
	var marker int64
	pd := c.Deps.ProjectDir
	if pd != "" {
		marker = logs.LogSize(logs.LogPath(pd))
	}
	if sc.Level != "" {
		progress("opening " + sc.Level)
		if _, err := v2Op(ctx, c, "open_level", map[string]any{"level_path": sc.Level}); err != nil {
			return nil, err
		}
	}
	playing := mode == "pie" || mode == "simulate"
	var setupErrs []string
	crashed := func(err error) (map[string]any, error) {
		// A PIE that dies on start (bad Blueprint, null access) is the common case:
		// diagnose it instead of returning a bare transport error.
		if rep := detectCrash(pd, marker, runStart); rep != nil {
			return map[string]any{"scenario": sc.Name, "verdict": "FAIL", "crash": rep, "error": err.Error()}, nil
		}
		return nil, err
	}
	stop := func() {
		if playing {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_, _ = v2Op(cctx, c, "pie_stop", nil)
		}
	}
	if playing {
		progress("starting " + mode)
		if _, err := v2Op(ctx, c, "pie_start", map[string]any{"simulate": mode == "simulate"}); err != nil {
			return crashed(err)
		}
		if err := waitPIE(ctx, c, true, 20*time.Second); err != nil {
			stop()
			return crashed(err)
		}
		if sc.TimeDilation > 0 && sc.TimeDilation != 1 {
			_, _ = v2Op(ctx, c, "console", map[string]any{"command": fmt.Sprintf("slomo %g", sc.TimeDilation), "world": "pie"})
		}
		for _, sp := range sc.Setup.SetProps {
			if _, err := v2Op(ctx, c, "actor_set_properties", map[string]any{"world": "pie", "actor": beatTarget(sp.Target), "properties": sp.Properties}); err != nil {
				setupErrs = append(setupErrs, "setup "+sp.Target+": "+err.Error())
				progress(setupErrs[len(setupErrs)-1])
			}
		}
	}
	source, world := "scene_capture", "editor"
	if mode == "pie" {
		source, world = "pie_highres", "pie"
	} else if mode == "simulate" {
		world = "pie"
	}
	startArgs := map[string]any{"world": world, "source": source, "interval_s": orDefault(sc.IntervalS, 0.5),
		"cell_width": orDefaultInt(in.FrameW, 256), "cell_height": orDefaultInt(in.FrameH, 144),
		"max_frames": orDefaultInt(in.MaxFrames, 96), "max_seconds": duration + 5, "track_actors": sc.TrackActors}
	if obs := pick(map[string]any{"include": nilIfEmpty(in.Include), "properties": nilIfEmpty(in.Properties)}, "include", "properties"); len(obs) > 0 {
		startArgs["observe"] = obs
	}
	started, err := v2Op(ctx, c, "capture_start", startArgs)
	if err != nil {
		stop()
		return crashed(err)
	}
	session, _ := started["session"].(string)
	progress(fmt.Sprintf("recording %s for %.0fs", session, duration))
	beatErrs := append(setupErrs, runBeatsV2(ctx, c, sc.Beats, duration, progress)...)

	// Tear down on a detached context so a cancelled job still stops the recorder and PIE.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	b, _ := v2Bridge(c)
	raw, stopErr := b.Call(cctx, "capture_stop", map[string]any{"session": session})
	stop()
	result := map[string]any{"scenario": sc.Name, "session": session}
	if len(beatErrs) > 0 {
		result["beat_errors"] = beatErrs
	}
	if stopErr != nil {
		if rep := detectCrash(pd, marker, runStart); rep != nil {
			result["verdict"], result["crash"], result["error"] = "FAIL", rep, stopErr.Error()
			return result, nil
		}
		return nil, stopErr
	}
	var cr captureStopResult
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, err
	}
	logSum := eval.LogSummary{}
	if pd != "" {
		if text, _, rerr := logs.ReadFrom(logs.LogPath(pd), marker); rerr == nil {
			logSum.Errors, logSum.Warnings, logSum.Ensures = logs.CountBySeverity(logs.FilterLines(text, "Warning", nil))
		}
	}
	result["logs"] = logCounts{Errors: logSum.Errors, Warnings: logSum.Warnings, Ensures: logSum.Ensures}
	if rep := detectCrash(pd, marker, runStart); rep != nil {
		result["crash"] = rep
	}
	if len(cr.Frames) == 0 {
		result["verdict"], result["error"] = "FAIL", "no frames were captured (was the world ticking? possessed play needs mode=pie)"
		return result, nil
	}
	enrichVisual(cr.Dir, cr.Frames)
	rep := eval.Evaluate(framesToSamples(cr.Frames), logSum, scenarioRubric(sc.Rubric))
	png, sidecar, err := montage(cr.Dir, cr.Frames, orDefaultInt(in.Cols, 8), true, failedFrameIndices(rep))
	if err != nil {
		return nil, err
	}
	imgPath := filepath.Join(cr.Dir, "montage.png")
	if werr := os.WriteFile(imgPath, png, 0o644); werr == nil {
		result["image_path"] = imgPath // job status/wait attach it as image content
	}
	for k, v := range sidecar {
		result[k] = v
	}
	result["verdict"], result["rubric"] = rep.Verdict, reportToJSON(rep)
	return result, nil
}

func nilIfEmpty(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s
}

// beatTarget maps the scenario vocabulary ("gamestate", a label) onto actor refs.
func beatTarget(t string) string {
	switch t {
	case "gamestate":
		return "@gamestate"
	case "pawn":
		return "@pawn"
	}
	return t
}

// runBeatsV2 runs the beats in at_s order over the window via the v2 ops, collecting
// (not swallowing) beat failures.
func runBeatsV2(ctx context.Context, c *spec.Call, beats []eval.Beat, duration float64, progress func(string)) []string {
	var errs []string
	start := time.Now()
	ordered := append([]eval.Beat(nil), beats...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].AtS < ordered[j].AtS })
	fail := func(i int, what string, err error) {
		msg := fmt.Sprintf("beat %d (%s): %v", i, what, err)
		errs = append(errs, msg)
		progress(msg)
	}
	for i, bt := range ordered {
		if bt.AtS > 0 && sleepUntil(ctx, start.Add(secs(bt.AtS))) != nil {
			return errs
		}
		if bt.Exec != nil {
			if _, err := v2Op(ctx, c, "actor_call", map[string]any{"actor": beatTarget(bt.Exec.Target), "function": bt.Exec.UFunction,
				"args": bt.Exec.Args, "world": "pie"}); err != nil {
				fail(i, "exec "+bt.Exec.UFunction, err)
			}
		}
		if bt.Console != "" {
			if _, err := v2Op(ctx, c, "console", map[string]any{"command": bt.Console, "world": "pie"}); err != nil {
				fail(i, "console", err)
			}
		}
		if bt.WaitUntil != "" {
			if ok, err := waitUntil(ctx, c, bt.WaitUntil, orDefault(bt.TimeoutS, 10)); err != nil {
				fail(i, "wait_until", err)
			} else if !ok {
				fail(i, "wait_until", fmt.Errorf("%q not met within %gs", bt.WaitUntil, orDefault(bt.TimeoutS, 10)))
			}
		}
	}
	_ = sleepUntil(ctx, start.Add(secs(duration)))
	return errs
}

func waitUntil(ctx context.Context, c *spec.Call, expr string, timeoutS float64) (bool, error) {
	pred, err := eval.ParsePredicate(expr)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(secs(timeoutS))
	for time.Now().Before(deadline) {
		if st, err := v2Op(ctx, c, "pie_observe", nil); err == nil {
			if ok, _ := pred.Eval(st); ok {
				return true, nil
			}
		}
		if sleepCtx(ctx, 250*time.Millisecond) != nil {
			return false, ctx.Err()
		}
	}
	return false, nil
}

// waitPIE polls editor_ping until PIE is (or is no longer) running.
func waitPIE(ctx context.Context, c *spec.Call, want bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ping, err := v2Op(ctx, c, "editor_ping", nil); err == nil && ping["pie"] == want {
			return nil
		}
		if time.Now().After(deadline) {
			return envelope.New(envelope.Timeout, "PIE did not %s within %s", map[bool]string{true: "start", false: "stop"}[want], timeout).
				WithHint("check the editor log (logs op=tail): a compile error or modal dialog can block PIE")
		}
		if err := sleepCtx(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}
