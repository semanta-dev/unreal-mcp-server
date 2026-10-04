package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
		Description: "Follow async work (build, playtest, editor_lifecycle, git_revert, headless).\n- status: state, last progress, result or error.\n- wait: up to wait_s (default 25), streaming progress.\n- cancel.\n- list: this project's jobs (any of its sessions can poll them).",
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
		Description: "Read the editor log files (works while the editor is busy or gone).\n- mark → marker; later since `marker` → those lines + error/warning/ensure counts.\n- tail: last `lines` at min_severity.\n- events: the log's structured events from `marker` (gameplay events: toolset game).",
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
	// Agents asking for gameplay events (kills, hits) land here: say whose events these are.
	return &spec.Result{Data: map[string]any{"events": out, "marker": strconv.FormatInt(next, 10), "count": len(out),
		"note": "the editor log's events (issues, PIE transitions), not the game's: gameplay events are game op=events (toolset game)"},
		Summary: fmt.Sprintf("%d editor log events", len(out))}, nil
}

// --- analyze ---------------------------------------------------------------------

type analyzeIn struct {
	Op       string             `json:"op" jsonschema:"rubric | perf | image_diff | scenarios | events"`
	Timeline []timelineFrame    `json:"timeline,omitempty" jsonschema:"rubric: a playtest result's timeline"`
	Logs     *logCounts         `json:"logs,omitempty" jsonschema:"rubric: {errors, warnings, ensures} for log checks"`
	Rubric   []eval.RubricCheck `json:"rubric,omitempty" jsonschema:"rubric: [{id, kind, path, params?, severity?, allow_perturbed?}]"`
	Path     string             `json:"path,omitempty" jsonschema:"perf: a CsvProfiler .csv or a .memreport; image_diff: an image"`
	Baseline string             `json:"baseline,omitempty" jsonschema:"image_diff: the image to compare against"`
	HitchMs  float64            `json:"hitch_ms,omitempty" jsonschema:"perf: hitch threshold in ms (default 33.3)"`
	MaxDHash *int               `json:"max_dhash,omitempty" jsonschema:"image_diff: max dHash distance (default 8; 0 = exact)"`
	MaxLuma  *float64           `json:"max_luma_delta,omitempty" jsonschema:"image_diff: max mean-luma delta (default 0.15)"`
	Dir      string             `json:"dir,omitempty" jsonschema:"scenarios: scenario/v1 folder (default .mcp/scenarios)"`
	Kinds    []string           `json:"kinds,omitempty" jsonschema:"events: only these kinds"`
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
		{Name: "events", Summary: "a playtest's recorded events, filtered by kind", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"path"}},
	}
	return &spec.Spec{
		Name: "analyze", Title: "Analyze results offline", Toolset: spec.Core, Offline: true, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Score evidence offline.\n- rubric: re-score a playtest `timeline` → a verdict with frame evidence.\n- perf: CsvProfiler CSV → frame-time percentiles + hitches; .memreport → memory buckets.\n- image_diff: `path` vs `baseline` → hash distance, luma delta, pass.\n- scenarios: the saved playtest suite.\n- events: a playtest_path's recorded events (`kinds`).",
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
	case "events":
		return playtestEvents(projectPath(c, in.Path), in.Kinds, 200)
	case "scenarios":
		dir := projectPath(c, in.Dir)
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
			item := map[string]any{"file": filepath.ToSlash(p), "valid": false}
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
		return &spec.Result{Data: map[string]any{"dir": filepath.ToSlash(dir), "scenarios": out}, Summary: fmt.Sprintf("%d scenarios", len(out))}, nil
	case "rubric":
		var ls eval.LogSummary
		if in.Logs != nil {
			ls = eval.LogSummary{Errors: in.Logs.Errors, Warnings: in.Logs.Warnings, Ensures: in.Logs.Ensures}
		}
		samples := make([]eval.Sample, len(in.Timeline))
		for i, f := range in.Timeline {
			samples[i] = eval.Sample{Index: f.Index, TWorld: f.TWorld, State: f.State}
		}
		if diags := eval.LintRubric(in.Rubric); eval.HasErrors(diags) {
			return nil, envelope.New(envelope.InvalidArgument, "the rubric has errors").WithDetail("diagnostics", diags)
		}
		rep := eval.Evaluate(samples, ls, scenarioRubric(in.Rubric))
		return &spec.Result{Data: reportToJSON(rep), Summary: "verdict " + rep.Verdict}, nil
	case "perf":
		data, err := os.ReadFile(projectPath(c, in.Path))
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
	a, err := visual.Load(projectPath(c, in.Path))
	if err != nil {
		return nil, envelope.New(envelope.NotFound, "path: %v", err)
	}
	b, err := visual.Load(projectPath(c, in.Baseline))
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
		{Name: "checkpoint", Summary: "commit + tag umcp/cp/N", Tier: spec.Mutating, Required: []string{"message"}, Needs: []string{"project"}},
	}
	return &spec.Spec{
		Name: "git", Title: "Project git", Toolset: spec.Core, Offline: true, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "The project's git repo (no editor).\n- status / diff / log (+ checkpoints).\n- checkpoint: stage (default all but Saved/Intermediate/DerivedDataCache), commit `message` (hooks run) and tag umcp/cp/N; nothing to commit tags HEAD. git_revert only goes back to these.",
		Schema:      spec.SchemaFor[gitIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"git_status", "git_diff", "git_log", "git_checkpoint"},
		Handler:     gitHandler,
	}
}

var cpTag = regexp.MustCompile(`^umcp/cp/(\d+)$`)

// checkpoints lists the umcp/cp/N tags, highest n first.
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
		add = append(append(add, "."), stageExcludes(ctx, dir)...)
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
	if err := notWhileRestarting(c); err != nil {
		return nil, err
	}
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
	BeatErrors string   `json:"beat_errors,omitempty" jsonschema:"run: fail (default: a failed setup step or beat fails the run) | warn"`
	Perf       bool     `json:"perf,omitempty" jsonschema:"replay under CsvProfiler (no capture/recorder) → perf_csv.*"`
	Seeds      []int    `json:"seeds,omitempty" jsonschema:"batch: one run per seed (≤ 20)"`
}

var playtestReaches = []string{"open_level", "pie_start", "pie_stop", "console", "actor_set_properties",
	"capture_start", "capture_stop", "actor_call", "pie_observe", "observe_paths", "editor_ping",
	"pie_input", "pie_cursor", "pie_ui_click", "pie_aim_state", "pie_axis_stats", "pie_time", "game_read", "game_command", "events_start", "events_stop", "seed_random"}

func playtestSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "run", Summary: "play a scenario: frames + state + beats + rubric verdict", Tier: spec.Exec, Async: true,
			Reaches: slices.Clone(playtestReaches[:len(playtestReaches)-1]),
			Rejects: []string{"seeds"}, Needs: []string{"plugin>=3 for game_command beats", "plugin>=5 for cursor/ui_click/axis beats", "plugin>=8 for engine events"}},
		{Name: "batch", Summary: "a run per seed (engine RNG seeded) + the spread", Tier: spec.Exec, Async: true,
			Required: []string{"seeds"}, Reaches: playtestReaches, Needs: []string{"plugin>=8"}},
	}
	return &spec.Spec{
		Name: "playtest", Title: "Automated playtest", Toolset: spec.Core, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Validate the game (async job). op=run plays a scenario/v1 (`path` or `json`): open the level, play (pie|simulate|editor), record frames + state, run timed beats at at_s or game-time at_world_s (exec: a UFUNCTION; console; wait_until; input: like pie; game_command), stop, score the rubric → {verdict (or INSUFFICIENT_EVIDENCE), rubric, logs, timeline, playtest_path, …} + a contact sheet (wait_s / job). record_events: engine + game event timeline. op=batch seeds=[…]: a run per seed + the spread. A crash or a failed setup step/beat fails the run (beat_errors=warn: WARN). Saved suite: analyze op=scenarios.",
		Schema:      spec.SchemaFor[playtestIn](map[string][]any{"op": spec.OpEnum(ops...), "beat_errors": {"fail", "warn"}}, "op"),
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
		b, err := os.ReadFile(projectPath(c, in.Path))
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
	if c.Op.Name == "batch" && (len(in.Seeds) == 0 || len(in.Seeds) > maxSeeds) {
		return nil, envelope.New(envelope.InvalidArgument, "batch takes 1 to %d seeds", maxSeeds)
	}
	for _, s := range in.Seeds {
		if s < math.MinInt32 || s > math.MaxInt32 {
			return nil, envelope.New(envelope.InvalidArgument, "seed %d is not a 32-bit integer", s)
		}
	}
	if c.Op.Name == "batch" && sc.Mode == "editor" {
		return nil, envelope.New(envelope.InvalidArgument, "a seeded batch plays the game: mode pie or simulate")
	}
	if in.Perf && sc.Mode == "editor" {
		return nil, envelope.New(envelope.InvalidArgument, "perf profiles the game: mode pie or simulate")
	}
	if _, err := v2Bridge(c); err != nil {
		return nil, err
	}
	reg, err := jobsOf(c)
	if err != nil {
		return nil, err
	}
	j := reg.Start(context.Background(), func(jctx context.Context, progress func(string)) (any, error) {
		if c.Op.Name == "batch" {
			return runBatch(jctx, c, sc, in, progress)
		}
		return runPlaytest(jctx, c, sc, in, progress, nil)
	})
	return &spec.Result{Job: j}, nil
}

// runPlaytest is the one playtest orchestration (v1 had it twice: playtest_capture
// and scenario_run).
func runPlaytest(ctx context.Context, c *spec.Call, sc *eval.Scenario, in playtestIn, progress func(string), seed *int) (map[string]any, error) {
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
	var setupErrs, seedErrs []string
	crashed := func(err error) (map[string]any, error) {
		// A PIE that dies on start (bad Blueprint, null access) is the common case:
		// diagnose it instead of returning a bare transport error.
		if rep := detectCrash(pd, marker, runStart); rep != nil {
			return map[string]any{"scenario": sc.Name, "verdict": "FAIL", "crash": rep, "error": err.Error()}, nil
		}
		return nil, err
	}
	stop := func() error {
		if playing {
			return stopPIEAndWait(ctx, c)
		}
		return nil
	}
	if playing {
		progress("starting " + mode)
		forgetGameWorld(c)
		if _, err := v2Op(ctx, c, "pie_start", map[string]any{"simulate": mode == "simulate"}); err != nil {
			return crashed(err)
		}
		if err := waitPIE(ctx, c, true, 20*time.Second); err != nil {
			stop()
			return crashed(err)
		}
		if seed != nil {
			seedErrs = applySeed(ctx, c, sc, *seed)
			setupErrs = append(setupErrs, seedErrs...)
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
	var timeline *eventTimeline
	if sc.RecordEvents && playing {
		if timeline, err = startEventTimeline(ctx, c, session); err != nil {
			setupErrs = append(setupErrs, "record_events: "+err.Error())
			progress(setupErrs[len(setupErrs)-1])
		}
	}
	progress(fmt.Sprintf("recording %s for %.0fs", session, duration))
	beatErrs := append(setupErrs, runBeatsV2(ctx, c, sc.Beats, duration, progress, timeline)...)

	// Tear down on detached contexts (a cancelled job still stops the recorder and PIE),
	// each call with its own budget: a long timeline must not starve the capture stop.
	var events *eval.EventLog
	var engineReport map[string]any
	if timeline != nil {
		ectx, ecancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		if events, engineReport, err = timeline.stop(ectx, c); err != nil {
			beatErrs = append(beatErrs, "record_events: the event session did not stop cleanly: "+err.Error())
		}
		ecancel()
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	b, _ := v2Bridge(c)
	raw, stopErr := b.Call(cctx, "capture_stop", map[string]any{"session": session})
	teardownErr := stop()
	if teardownErr != nil {
		beatErrs = append(beatErrs, "teardown: "+teardownErr.Error())
	}
	result := map[string]any{"scenario": sc.Name, "session": session}
	if seed != nil {
		result["seed"] = *seed
	}
	if teardownErr != nil {
		result["teardown_error"] = teardownErr.Error() // the editor may still be playing: nothing more should run
	}
	if events != nil {
		result["events"] = eventSummary(events, engineReport)
	}
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
	crash := detectCrash(pd, marker, runStart)
	if crash != nil {
		result["crash"] = crash
	}
	if len(cr.Frames) == 0 {
		result["verdict"], result["error"] = "FAIL", "no frames were captured (was the world ticking? possessed play needs mode=pie)"
		notePlaytestJSON(cr.Dir, result, events)
		return result, nil
	}
	enrichVisual(cr.Dir, cr.Frames)
	var perfCSV map[string]float64
	if in.Perf && playing && teardownErr == nil { // never profile in a PIE that would not stop
		values, csvPath, perrs, perr := perfPass(ctx, c, sc, duration, progress, false, seed)
		beatErrs = append(beatErrs, perrs...)
		if errors.Is(perr, errStuckPIE) {
			beatErrs = append(beatErrs, "teardown: "+perr.Error())
			result["teardown_error"] = perr.Error()
		}
		if errors.Is(perr, errUnseeded) {
			seedErrs = append(seedErrs, perr.Error())
		}
		if perr != nil {
			result["perf_error"] = perr.Error()
		} else {
			perfCSV = values
			pc := map[string]any{"values": values, "csv": csvPath}
			if sc.RecordEvents {
				// The event recorder's cost: the same run profiled with it on (R5.4).
				with, withCSV, werrs, werr := perfPass(ctx, c, sc, duration, progress, true, seed)
				beatErrs = append(beatErrs, werrs...)
				if errors.Is(werr, errStuckPIE) {
					beatErrs = append(beatErrs, "teardown: "+werr.Error())
					result["teardown_error"] = werr.Error()
				}
				if errors.Is(werr, errUnseeded) {
					seedErrs = append(seedErrs, werr.Error())
				}
				if werr != nil {
					pc["with_events_error"] = werr.Error()
				} else {
					pc["with_events"] = map[string]any{"values": with, "csv": withCSV}
					pc["recorder_overhead_ms"] = map[string]float64{"mean": with["mean_frame_ms"] - values["mean_frame_ms"],
						"p50": with["p50_frame_ms"] - values["p50_frame_ms"], "p95": with["p95_frame_ms"] - values["p95_frame_ms"]}
				}
			}
			result["perf_csv"] = pc
		}
		if len(beatErrs) > 0 {
			result["beat_errors"] = beatErrs
		}
	}
	rep := eval.EvaluateInputs(eval.Inputs{Timeline: framesToSamples(cr.Frames), Logs: logSum, Events: events, PerfCSV: perfCSV}, scenarioRubric(sc.Rubric))
	png, sidecar, err := montage(cr.Dir, cr.Frames, orDefaultInt(in.Cols, 8), true, failedFrameIndices(rep))
	if err != nil {
		return nil, err
	}
	imgPath := filepath.Join(cr.Dir, "montage.png")
	if werr := os.WriteFile(imgPath, png, 0o644); werr == nil {
		result["image_path"] = filepath.ToSlash(imgPath) // job status/wait attach it as image content
	}
	for k, v := range sidecar {
		result[k] = v
	}
	verdict, reasons := finalVerdict(rep.Verdict, beatErrs, crash != nil, in.BeatErrors)
	if len(seedErrs) > 0 && verdict != "FAIL" {
		// An unseeded run in a seeded batch is not the run asked for, beat_errors=warn or not.
		verdict, reasons = "FAIL", append(reasons, "the run was not seeded")
	}
	if result["teardown_error"] != nil && verdict != "FAIL" {
		// PIE would not stop: whatever plays next would play in it — never a warning.
		verdict, reasons = "FAIL", append(reasons, "PIE would not stop")
	}
	result["verdict"], result["rubric"] = verdict, reportToJSON(rep)
	if len(reasons) > 0 {
		result["verdict_reasons"] = reasons
	}
	notePlaytestJSON(cr.Dir, result, events)
	return result, nil
}

// notePlaytestJSON writes playtest.json beside the frames and names it in the result.
func notePlaytestJSON(dir string, result map[string]any, events *eval.EventLog) {
	if dir == "" {
		return
	}
	if p, err := writePlaytestJSON(dir, result, events); err == nil {
		result["playtest_path"] = p
	} else {
		result["playtest_path_error"] = err.Error()
	}
}

// stopPIEAndWait stops PIE on a detached context and waits until it has stopped: the next
// pie_start (a perf pass, the next seed) must not see the dying session as running.
func stopPIEAndWait(ctx context.Context, c *spec.Call) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, _ = v2Op(cctx, c, "pie_stop", nil)
	err := waitPIE(cctx, c, false, 15*time.Second)
	forgetGameWorld(c)
	if err != nil {
		return fmt.Errorf("PIE did not stop (the next play would run in it): %w", err)
	}
	return nil
}

// finalVerdict folds what the rubric cannot see into the verdict: a crash always fails
// the run, and so does a failed setup step or beat — the scenario did not play as
// written — unless the caller opted into beat_errors=warn. reasons says why the verdict
// is worse than the rubric's.
func finalVerdict(rubric string, beatErrs []string, crashed bool, beatMode string) (string, []string) {
	verdict := rubric
	var reasons []string
	worsen := func(to, why string) {
		if rank(to) > rank(verdict) {
			verdict = to
		}
		reasons = append(reasons, why)
	}
	if crashed {
		worsen("FAIL", "the editor crashed during the run")
	}
	if len(beatErrs) > 0 {
		to := "FAIL"
		if beatMode == "warn" {
			to = "WARN"
		}
		worsen(to, fmt.Sprintf("%d setup step(s)/beat(s) failed (beat_errors)", len(beatErrs)))
	}
	return verdict, reasons
}

func rank(v string) int {
	switch v {
	case "FAIL":
		return 3
	case eval.VerdictInsufficient:
		return 2
	case "WARN":
		return 1
	}
	return 0
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

// runBeatsV2 runs the beats in schedule order over the window via the v2 ops,
// collecting (not swallowing) beat failures. Beats are scheduled on the wall clock
// (at_s) or on the game's clock (at_world_s, read with pie_time: paused time does not
// count) — one clock per scenario (ParseScenario).
func runBeatsV2(ctx context.Context, c *spec.Call, beats []eval.Beat, duration float64, progress func(string), timeline *eventTimeline) []string {
	var errs []string
	start := time.Now()
	end := start.Add(secs(duration))
	ordered := append([]eval.Beat(nil), beats...)
	worldClock := false
	for _, b := range ordered {
		worldClock = worldClock || b.AtWorldS > 0
	}
	at := func(b eval.Beat) float64 {
		if worldClock {
			return b.AtWorldS
		}
		return b.AtS
	}
	sort.SliceStable(ordered, func(i, j int) bool { return at(ordered[i]) < at(ordered[j]) })
	fail := func(i int, what string, err error) {
		msg := fmt.Sprintf("beat %d (%s): %v", i, what, err)
		errs = append(errs, msg)
		progress(msg)
	}
	var world0 float64
	var worldName string
	if worldClock {
		t, w, err := gameTime(ctx, c)
		if err != nil {
			fail(0, "at_world_s", fmt.Errorf("the game clock is unreadable: %w", err))
			return errs
		}
		world0, worldName = t, w
	}
	runID := strconv.FormatInt(start.UnixNano(), 36) // game_command request_ids: unique per run
	for i, bt := range ordered {
		if worldClock {
			if bt.AtWorldS > 0 {
				if err := waitGameTime(ctx, c, worldName, world0, world0+bt.AtWorldS, end); err != nil {
					if ctx.Err() != nil {
						return errs
					}
					fail(i, "at_world_s", err)
					continue
				}
			}
		} else if bt.AtS > 0 && sleepUntil(ctx, start.Add(secs(bt.AtS))) != nil {
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
		if bt.Input != nil {
			err := runInputBeat(ctx, c, bt.Input, end)
			if err != nil {
				fail(i, "input "+bt.Input.Kind(), err)
			}
			timeline.note(ctx, c, "input", true, beatData(map[string]any{"step": bt.Input.Kind(), "key": bt.Input.Key,
				"action": bt.Input.Action, "widget": bt.Input.Widget, "beat": i}, err))
		}
		if g := bt.GameCommand; g != nil {
			id := g.RequestID
			if id == "" {
				id = fmt.Sprintf("playtest-%s-beat%d", runID, i)
			}
			_, err := runGameCommand(ctx, c, gameCommandIn{Name: g.Name, Args: g.Args, RequestID: id})
			if err != nil {
				fail(i, "game_command "+g.Name, err)
			}
			timeline.note(ctx, c, "game_command", false, beatData(map[string]any{"name": g.Name, "request_id": id, "beat": i}, err))
		}
		if bt.WaitUntil != "" {
			if ok, err := waitUntil(ctx, c, bt.WaitUntil, orDefault(bt.TimeoutS, 10)); err != nil {
				fail(i, "wait_until", err)
			} else if !ok {
				fail(i, "wait_until", fmt.Errorf("%q not met within %gs", bt.WaitUntil, orDefault(bt.TimeoutS, 10)))
			}
		}
	}
	_ = sleepUntil(ctx, end)
	return errs
}

// beatData drops empty fields and adds the beat's error, if any.
func beatData(d map[string]any, err error) map[string]any {
	for k, v := range d {
		if v == "" {
			delete(d, k)
		}
	}
	if err != nil {
		d["error"] = err.Error()
	}
	return d
}

// runInputBeat plays one input step through the same companion ops as pie op=input /
// cursor / ui_click / aim; an aim step with duration_s tracks until then (or the run's end).
func runInputBeat(ctx context.Context, c *spec.Call, in *eval.InputStep, end time.Time) error {
	if in.Kind() == "aim" {
		target := map[string]any{"class": in.Class}
		if in.Actor != "" {
			target = map[string]any{"actor": in.Actor}
		}
		return trackAim(ctx, c, target, in.DurationS, end)
	}
	args := map[string]any{}
	set := func(k string, v any, ok bool) {
		if ok {
			args[k] = v
		}
	}
	set("action", in.Action, in.Action != "")
	set("duration_s", in.DurationS, in.DurationS > 0)
	set("button", in.Button, in.Button != "")
	var op string
	switch in.Kind() {
	case "input":
		op = "pie_input"
		args["key"] = in.Key
		if in.Value != nil {
			args["value"] = *in.Value
		}
		if (in.Action == "axis") != (in.Value != nil) {
			return envelope.New(envelope.InvalidArgument, "value goes with action=axis, and action=axis needs value")
		}
	case "cursor":
		op = "pie_cursor"
		args["position"] = in.Position
		set("to", in.To, in.To != nil)
	case "ui_click":
		op = "pie_ui_click"
		args["widget"] = in.Widget
	default:
		return envelope.New(envelope.InvalidArgument, "input needs exactly one of key, position, widget, actor/class")
	}
	_, err := v2Op(ctx, c, op, args)
	return err
}

// gameTime reads the running game's clock (world seconds) and which world it is.
func gameTime(ctx context.Context, c *spec.Call) (float64, string, error) {
	out, err := v2Op(ctx, c, "pie_time", nil)
	if err != nil {
		return 0, "", err
	}
	t, ok := out["world_time_s"].(float64)
	if !ok {
		return 0, "", envelope.New(envelope.OperationFailed, "pie_time returned no world_time_s")
	}
	w, _ := out["world"].(string)
	return t, w, nil
}

// waitGameTime polls the game clock until it reaches target, or fails when the window
// ends first (a paused or slowed game) — never runs a beat early. The clock belongs to
// one world: after a map travel it restarts, so a changed world fails the beat.
func waitGameTime(ctx context.Context, c *spec.Call, world string, start, target float64, end time.Time) error {
	last := start
	for {
		t, w, err := gameTime(ctx, c)
		if err == nil {
			if world != "" && w != world {
				return fmt.Errorf("the game world changed (%s → %s, a map travel?): at_world_s counts game time in one world", world, w)
			}
			if t < last {
				return fmt.Errorf("the game clock went back (%.2fs → %.2fs: the level restarted?): at_world_s counts game time in one world", last, t)
			}
			if t >= target {
				return nil
			}
			last = t
		} else if transient, _ := transientWaitError(err); !transient {
			return err
		}
		if time.Now().After(end) {
			return fmt.Errorf("the game clock was at %.2fs, not %.2fs, when the window ended (paused or slowed?)", last, target)
		}
		if err := sleepCtx(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitUntil(ctx context.Context, c *spec.Call, expr string, timeoutS float64) (bool, error) {
	pred, err := eval.ParsePredicate(expr)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(secs(timeoutS))
	for time.Now().Before(deadline) {
		st, err := observeState(ctx, c, pred, nil)
		if err == nil {
			if ok, _ := pred.Eval(st); ok {
				return true, nil
			}
		} else if transient, _ := transientWaitError(err); !transient {
			return false, err // a malformed or non-pure path will not fix itself
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
