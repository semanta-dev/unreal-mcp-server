package main

// The live game-making eval (docs/plans/REMEDIATION_PLAN.md §2): agents do multi-step
// game tasks through a REAL server against a scratch copy of a game project in a live
// editor. Every run starts from the same files (git_revert to a baseline checkpoint), and
// is judged only by mechanical checks — harness-side Python probes of the world/assets,
// the recorded tool calls, and the final answer matched against a probed value. No LLM
// judge. A run that exceeds its cost cap or turn limit fails.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gameTask is one live task (docs/validation/gameeval/tasks.json).
type gameTask struct {
	ID        string      `json:"id"`
	Goal      string      `json:"goal"`    // G1..G6 (plan §2)
	Project   string      `json:"project"` // a key of -projects
	Prompt    string      `json:"prompt"`
	Requires  []string    `json:"requires,omitempty"` // plan phases that make it possible (informational)
	MultiStep bool        `json:"multi_step,omitempty"`
	NoPython  bool        `json:"no_python,omitempty"` // any python call fails the run (G2)
	HeldOut   bool        `json:"held_out,omitempty"`
	Setup     []liveCall  `json:"setup,omitempty"` // harness calls after the reset, before the agent
	Checks    []liveCheck `json:"checks"`
}

type liveCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// liveCheck is one mechanical pass condition; exactly one of Probe, Called, Answer.
type liveCheck struct {
	Name string `json:"name"`
	// Probe is Python the harness runs after the agent stops; its last line printed as
	// "RESULT <json object>" is matched against Expect.
	Probe  string        `json:"probe,omitempty"`
	Expect []expectation `json:"expect,omitempty"`
	Called *calledCheck  `json:"called,omitempty"`
	Answer *answerCheck  `json:"answer,omitempty"`
}

type expectation struct {
	Path  string `json:"path"` // dot path into the probe result
	Op    string `json:"op"`   // eq ne gt gte lt lte contains exists
	Value any    `json:"value,omitempty"`
}

// calledCheck: the agent called Tool (with op Op, if set) at least Min times (default
// 1) without an error, after a successful call to After (if set).
type calledCheck struct {
	Tool  string `json:"tool"`
	Op    string `json:"op,omitempty"`
	After string `json:"after,omitempty"` // "tool" or "tool/op"
	Min   int    `json:"min,omitempty"`
}

// answerCheck: the final answer contains a number within Tolerance of the probed value
// at From ("<check name>.<path>"), or matches Regex.
type answerCheck struct {
	From      string  `json:"from,omitempty"`
	Tolerance float64 `json:"tolerance,omitempty"`
	Regex     string  `json:"regex,omitempty"`
}

// gameTaskFile is the task file: Python helpers shared by every probe and python
// setup call (prepended to their code), and the tasks.
type gameTaskFile struct {
	Prelude string      `json:"prelude"`
	Tasks   []*gameTask `json:"tasks"`
}

// loadGameTasks reads a task file and prepends the prelude to every probe and to the
// code of every python setup call.
func loadGameTasks(path string) ([]*gameTask, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f gameTaskFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, t := range f.Tasks {
		for i := range t.Checks {
			if t.Checks[i].Probe != "" {
				t.Checks[i].Probe = f.Prelude + "\n" + t.Checks[i].Probe
			}
		}
		for i := range t.Setup {
			if code, ok := t.Setup[i].Args["code"].(string); ok && t.Setup[i].Tool == "python" {
				t.Setup[i].Args["code"] = f.Prelude + "\n" + code
			}
		}
	}
	return f.Tasks, nil
}

// lintGameTasks checks the task file's structure and the plan's coverage rules: unique
// ids, a known goal and project, ≥ 1 check each, every check well-formed, every Answer.From
// naming an earlier probe; ≥ minPerGoal tasks per goal G2–G6 and ≥ minMulti multi-step.
func lintGameTasks(ts []*gameTask, projects []string, minPerGoal, minMulti int) []string {
	var errs []string
	seen := map[string]bool{}
	perGoal := map[string]int{}
	multi := 0
	for _, t := range ts {
		where := "task " + t.ID
		if t.ID == "" || seen[t.ID] {
			errs = append(errs, where+": missing or duplicate id")
		}
		seen[t.ID] = true
		if !regexp.MustCompile(`^G[1-6]$`).MatchString(t.Goal) {
			errs = append(errs, where+": goal must be G1..G6")
		}
		perGoal[t.Goal]++
		if t.MultiStep {
			multi++
		}
		if !contains(projects, t.Project) {
			errs = append(errs, fmt.Sprintf("%s: unknown project %q", where, t.Project))
		}
		if strings.TrimSpace(t.Prompt) == "" || len(t.Checks) == 0 {
			errs = append(errs, where+": needs a prompt and at least one check")
		}
		probes := map[string]bool{}
		for i, c := range t.Checks {
			cw := fmt.Sprintf("%s check %d (%s)", where, i, c.Name)
			n := 0
			if c.Probe != "" {
				n++
				probes[c.Name] = true
				if c.Name == "" {
					errs = append(errs, cw+": a probe needs a name")
				}
				for _, e := range c.Expect {
					if !contains([]string{"eq", "ne", "gt", "gte", "lt", "lte", "contains", "exists"}, e.Op) || e.Path == "" {
						errs = append(errs, fmt.Sprintf("%s: bad expectation %+v", cw, e))
					}
				}
			}
			if c.Called != nil {
				n++
				if c.Called.Tool == "" {
					errs = append(errs, cw+": called needs a tool")
				}
			}
			if c.Answer != nil {
				n++
				if c.Answer.From == "" && c.Answer.Regex == "" {
					errs = append(errs, cw+": answer needs from or regex")
				}
				if c.Answer.From != "" && !probes[strings.SplitN(c.Answer.From, ".", 2)[0]] {
					errs = append(errs, cw+": answer.from must name an earlier probe")
				}
				if c.Answer.Regex != "" {
					if _, err := regexp.Compile(c.Answer.Regex); err != nil {
						errs = append(errs, cw+": "+err.Error())
					}
				}
			}
			if n != 1 {
				errs = append(errs, cw+": exactly one of probe, called, answer")
			}
		}
	}
	for _, g := range []string{"G2", "G3", "G4", "G5", "G6"} {
		if perGoal[g] < minPerGoal {
			errs = append(errs, fmt.Sprintf("goal %s has %d tasks, want ≥ %d", g, perGoal[g], minPerGoal))
		}
	}
	if multi < minMulti {
		errs = append(errs, fmt.Sprintf("%d multi-step tasks, want ≥ %d", multi, minMulti))
	}
	return errs
}

// --- judging --------------------------------------------------------------------

// judgeGame applies the checks; probes are the parsed probe results by check name.
func judgeGame(t *gameTask, probes map[string]map[string]any, probeErr map[string]string, calls []toolCall, answer string, pythonCalls int) []string {
	var failed []string
	if t.NoPython && pythonCalls > 0 {
		failed = append(failed, fmt.Sprintf("no_python: %d python call(s)", pythonCalls))
	}
	for _, c := range t.Checks {
		switch {
		case c.Probe != "":
			if e := probeErr[c.Name]; e != "" {
				failed = append(failed, c.Name+": probe failed: "+e)
				continue
			}
			for _, e := range c.Expect {
				if !expectHolds(probes[c.Name], e) {
					failed = append(failed, fmt.Sprintf("%s: %s %s %v (got %v)", c.Name, e.Path, e.Op, e.Value, lookup(probes[c.Name], e.Path)))
				}
			}
		case c.Called != nil:
			if !calledHolds(*c.Called, calls) {
				failed = append(failed, c.Name+": no matching call "+describeCalled(*c.Called))
			}
		case c.Answer != nil:
			if !answerHolds(*c.Answer, probes, answer) {
				failed = append(failed, c.Name+": answer does not match")
			}
		}
	}
	return failed
}

func lookup(m map[string]any, path string) any {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(v) {
				return nil
			}
			cur = v[i]
		default:
			return nil
		}
	}
	return cur
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func expectHolds(m map[string]any, e expectation) bool {
	got := lookup(m, e.Path)
	switch e.Op {
	case "exists":
		return got != nil
	case "eq", "ne":
		eq := fmt.Sprint(got) == fmt.Sprint(e.Value)
		if a, ok := num(got); ok {
			if b, ok := num(e.Value); ok {
				eq = math.Abs(a-b) < 1e-9
			}
		}
		return eq == (e.Op == "eq")
	case "contains":
		switch g := got.(type) {
		case string:
			return strings.Contains(g, fmt.Sprint(e.Value))
		case []any:
			for _, x := range g {
				if fmt.Sprint(x) == fmt.Sprint(e.Value) {
					return true
				}
			}
		}
		return false
	}
	a, ok1 := num(got)
	b, ok2 := num(e.Value)
	if !ok1 || !ok2 {
		return false
	}
	switch e.Op {
	case "gt":
		return a > b
	case "gte":
		return a >= b
	case "lt":
		return a < b
	case "lte":
		return a <= b
	}
	return false
}

func callMatches(c toolCall, spec string) bool {
	tool, op, _ := strings.Cut(spec, "/")
	if c.Tool != tool || c.Error != "" {
		return false
	}
	return op == "" || fmt.Sprint(c.Args["op"]) == op
}

func calledHolds(cc calledCheck, calls []toolCall) bool {
	want := cc.Tool
	if cc.Op != "" {
		want += "/" + cc.Op
	}
	min := cc.Min
	if min == 0 {
		min = 1
	}
	armed := cc.After == ""
	n := 0
	for _, c := range calls {
		if !armed && callMatches(c, cc.After) {
			armed = true
			continue
		}
		if armed && callMatches(c, want) {
			n++
		}
	}
	return n >= min
}

func describeCalled(cc calledCheck) string {
	s := cc.Tool
	if cc.Op != "" {
		s += " op=" + cc.Op
	}
	if cc.After != "" {
		s += " after " + cc.After
	}
	return s
}

var numberRE = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

func answerHolds(a answerCheck, probes map[string]map[string]any, answer string) bool {
	if a.Regex != "" {
		return regexp.MustCompile(a.Regex).MatchString(answer)
	}
	name, path, _ := strings.Cut(a.From, ".")
	want, ok := num(lookup(probes[name], path))
	if !ok {
		return false
	}
	for _, s := range numberRE.FindAllString(answer, -1) {
		if f, err := strconv.ParseFloat(s, 64); err == nil && math.Abs(f-want) <= a.Tolerance {
			return true
		}
	}
	return false
}

// parseProbe finds the last "RESULT {...}" line of a python tool result's output.
func parseProbe(output string) (map[string]any, error) {
	lines := strings.Split(strings.ReplaceAll(output, "\r", ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "RESULT "); ok {
			var m map[string]any
			if err := json.Unmarshal([]byte(rest), &m); err != nil {
				return nil, fmt.Errorf("RESULT line: %w", err)
			}
			return m, nil
		}
	}
	return nil, fmt.Errorf("the probe printed no RESULT line (output %q)", truncate(output, 300))
}

// --- the live runner -------------------------------------------------------------

type liveOpts struct {
	server     string            // the server binary under test
	serverArgs []string          // common args (engine, group)
	projects   map[string]string // name -> scratch project dir
	ports      map[string]string // name -> -command-addr
	checkpoint map[string]string // name -> baseline checkpoint tag (made at start when empty)
	maxTurns   int
	costCap    float64 // USD per run
	prices     map[string]price
	model      string
	logDir     string
}

type liveResult struct {
	Task        string     `json:"task"`
	Goal        string     `json:"goal"`
	HeldOut     bool       `json:"held_out,omitempty"`
	Run         int        `json:"run"`
	Model       string     `json:"model"`
	Pass        bool       `json:"pass"`
	Failed      []string   `json:"failed,omitempty"`
	Aborted     string     `json:"aborted,omitempty"` // cost_cap | turn_limit | eval_cap | error
	Calls       []toolCall `json:"calls,omitempty"`
	PythonCalls int        `json:"python_calls"`
	Turns       int        `json:"turns"`
	Answer      string     `json:"answer,omitempty"`
	Usage       usage      `json:"usage"`
	CostUSD     float64    `json:"cost_usd"`
	Served      []string   `json:"served"`
	Err         string     `json:"error,omitempty"`
	Seconds     float64    `json:"seconds"`
}

// liveSession is one server process (stdio) bound to a scratch project.
type liveSession struct {
	cs *mcp.ClientSession
}

func startLiveSession(ctx context.Context, o liveOpts, project string, logf *os.File) (*liveSession, error) {
	args := append([]string{"-project", o.projects[project], "-command-addr", o.ports[project]}, o.serverArgs...)
	cmd := exec.Command(o.server, args...)
	if logf != nil {
		cmd.Stderr = logf
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "tooleval-live", Version: "1"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, err
	}
	return &liveSession{cs: cs}, nil
}

// call runs one harness-side tool call and returns its structured result.
func (s *liveSession) call(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, err
	}
	m, _ := res.StructuredContent.(map[string]any)
	if res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		return m, fmt.Errorf("%s: %s", tool, truncate(string(b), 600))
	}
	return m, nil
}

// waitJob follows an async result to completion (a finished job answers at once).
func (s *liveSession) waitJob(ctx context.Context, out map[string]any, limit time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(limit)
	for {
		state, _ := out["state"].(string)
		id, _ := out["job_id"].(string)
		if id == "" || (state != "running" && state != "queued") {
			if state == "failed" || state == "cancelled" {
				return out, fmt.Errorf("job %s %s: %v", id, state, out["error"])
			}
			return out, nil
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("job %s still %s after %s", id, state, limit)
		}
		next, err := s.call(ctx, "job", map[string]any{"op": "wait", "job_id": id, "wait_s": 25})
		if err != nil {
			return out, err
		}
		out = next
	}
}

// probe runs Python in the editor and parses its RESULT line.
func (s *liveSession) probe(ctx context.Context, code string) (map[string]any, error) {
	out, err := s.call(ctx, "python", map[string]any{"op": "run", "code": code})
	if err != nil {
		return nil, err
	}
	return parseProbe(fmt.Sprint(out["output"]))
}

// baseline makes (or reuses) the project's baseline checkpoint.
func baseline(ctx context.Context, s *liveSession) (string, error) {
	out, err := s.call(ctx, "git", map[string]any{"op": "checkpoint", "message": "gameeval baseline " + time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return "", err
	}
	for _, k := range []string{"tag", "checkpoint"} {
		if v, ok := out[k].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("checkpoint returned no tag: %v", out)
}

// reset reverts the project to its baseline (closing and reopening the editor when
// files changed) and waits until the editor answers.
func reset(ctx context.Context, s *liveSession, tag string) error {
	out, err := s.call(ctx, "git_revert", map[string]any{"to": tag, "discard_dirty": true, "wait_s": 25})
	if err != nil {
		return err
	}
	if _, err := s.waitJob(ctx, out, 15*time.Minute); err != nil {
		return err
	}
	if _, err := s.call(ctx, "editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25}); err != nil {
		return err
	}
	return nil
}

// runLive runs one task once: reset, setup, the agent loop, then the probes.
func runLive(ctx context.Context, cl *client, o liveOpts, t *gameTask, run int, logf *os.File) (r liveResult) {
	start := time.Now()
	r = liveResult{Task: t.ID, Goal: t.Goal, HeldOut: t.HeldOut, Run: run, Model: o.model}
	defer func() { r.Seconds = time.Since(start).Seconds() }() // named result: the deferred write lands
	s, err := startLiveSession(ctx, o, t.Project, logf)
	if err != nil {
		r.Err, r.Aborted = "server: "+err.Error(), "error"
		return r
	}
	defer s.cs.Close()
	if err := reset(ctx, s, o.checkpoint[t.Project]); err != nil {
		r.Err, r.Aborted = "reset: "+err.Error(), "error"
		return r
	}
	for _, c := range t.Setup {
		out, err := s.call(ctx, c.Tool, c.Args)
		if err == nil {
			_, err = s.waitJob(ctx, out, 10*time.Minute)
		}
		if err != nil {
			r.Err, r.Aborted = "setup: "+err.Error(), "error"
			return r
		}
	}
	tl, err := s.cs.ListTools(ctx, nil)
	if err != nil {
		r.Err, r.Aborted = "tools/list: "+err.Error(), "error"
		return r
	}
	tools, _, err := toAPITools(tl.Tools)
	if err != nil {
		r.Err, r.Aborted = "tools: "+err.Error(), "error"
		return r
	}
	msgs := []message{{Role: "user", Content: raws([]block{{Type: "text", Text: t.Prompt}})}}
	for {
		if r.Turns >= o.maxTurns {
			r.Aborted = "turn_limit"
			break
		}
		resp, err := cl.create(ctx, request{Model: o.model, MaxTokens: 8192, System: systemPrompt, Tools: tools, Messages: msgs})
		if err != nil {
			r.Err, r.Aborted = err.Error(), "error"
			break
		}
		r.Usage.add(resp.Usage)
		r.Served = append(r.Served, resp.Model)
		r.Turns++
		r.CostUSD = runCost(r.Usage, r.Served, o)
		msgs = append(msgs, message{Role: "assistant", Content: resp.Content})
		var results []block
		relist := false
		for _, b := range parseBlocks(resp.Content) {
			switch b.Type {
			case "text":
				r.Answer = b.Text
			case "tool_use":
				var args map[string]any
				_ = json.Unmarshal(b.Input, &args)
				if b.Name == "python" {
					r.PythonCalls++
				}
				if b.Name == "toolsets" || b.Name == "project" {
					relist = true
				}
				res, code := callLive(ctx, s, b.Name, args)
				r.Calls = append(r.Calls, toolCall{Tool: b.Name, Args: args, Error: code})
				results = append(results, block{Type: "tool_result", ToolUseID: b.ID, Content: res, IsError: code != ""})
			}
		}
		if o.costCap > 0 && r.CostUSD > o.costCap {
			r.Aborted = "cost_cap"
			break
		}
		if len(results) == 0 || resp.StopReason != "tool_use" {
			break
		}
		msgs = append(msgs, message{Role: "user", Content: raws(results)})
		if relist {
			if tl, err := s.cs.ListTools(ctx, nil); err == nil {
				if nt, _, err := toAPITools(tl.Tools); err == nil {
					tools = nt
				}
			}
		}
	}
	probes := map[string]map[string]any{}
	probeErr := map[string]string{}
	for _, c := range t.Checks {
		if c.Probe == "" {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		m, err := s.probe(pctx, c.Probe)
		cancel()
		if err != nil {
			probeErr[c.Name] = err.Error()
		}
		probes[c.Name] = m
	}
	r.Failed = judgeGame(t, probes, probeErr, r.Calls, r.Answer, r.PythonCalls)
	if r.Aborted != "" {
		r.Failed = append([]string{"aborted: " + r.Aborted}, r.Failed...)
	}
	r.Pass = len(r.Failed) == 0 && r.Err == ""
	return r
}

// callLive is callV2 against a live session (long timeout: builds and playtests).
func callLive(ctx context.Context, s *liveSession, name string, args map[string]any) ([]block, string) {
	return callV2(ctx, &v2Env{cs: s.cs}, name, args)
}

// runCost prices a run by the model that served each turn (a router may substitute);
// turns are priced at the most expensive served model when they differ.
func runCost(u usage, served []string, o liveOpts) float64 {
	p, ok := o.prices[o.model]
	for _, m := range served {
		if q, ok2 := o.prices[m]; ok2 && (!ok || q.in > p.in) {
			p, ok = q, true
		}
	}
	return cost(u, p)
}

// --- report ----------------------------------------------------------------------

type taskVerdict struct {
	Task, Goal string
	HeldOut    bool
	Passed     int
	Runs       int
	Pass       bool
}

// verdicts folds runs into per-task verdicts: a task passes when ≥ 2/3 of its runs
// pass (k = 3 → 2 runs).
func verdicts(rs []liveResult) []taskVerdict {
	by := map[string]*taskVerdict{}
	var order []string
	for _, r := range rs {
		v := by[r.Task]
		if v == nil {
			v = &taskVerdict{Task: r.Task, Goal: r.Goal, HeldOut: r.HeldOut}
			by[r.Task] = v
			order = append(order, r.Task)
		}
		v.Runs++
		if r.Pass {
			v.Passed++
		}
	}
	sort.Strings(order)
	out := make([]taskVerdict, 0, len(order))
	for _, id := range order {
		v := by[id]
		v.Pass = v.Runs > 0 && 3*v.Passed >= 2*v.Runs
		out = append(out, *v)
	}
	return out
}

func liveReport(rs []liveResult, label string, passBar, perGoal int) string {
	var b strings.Builder
	vs := verdicts(rs)
	fmt.Fprintf(&b, "# Game-making eval — %s (%s)\n\n", label, time.Now().UTC().Format("2006-01-02 15:04Z"))
	passed, py, total := 0, 0, 0.0
	goalPass := map[string]int{}
	goals := map[string]int{}
	for _, v := range vs {
		goals[v.Goal]++
		if v.Pass {
			passed++
			goalPass[v.Goal]++
		}
	}
	served := map[string]int{}
	for _, r := range rs {
		py += r.PythonCalls
		total += r.CostUSD
		for _, m := range r.Served {
			served[m]++
		}
	}
	pyRate := 0.0
	if len(rs) > 0 {
		pyRate = float64(py) / float64(len(rs))
	}
	fmt.Fprintf(&b, "%d tasks, %d runs. **%d/%d tasks pass** (a task passes when ≥ 2/3 of its runs pass). Python calls/run: "+
		"%.2f. Cost: $%.2f. Served: %v.\n\n", len(vs), len(rs), passed, len(vs), pyRate, total, served)
	bar := passed >= passBar && pyRate <= 0.1
	b.WriteString("| goal | tasks | pass |\n|---|---|---|\n")
	var gs []string
	for g := range goals {
		gs = append(gs, g)
	}
	sort.Strings(gs)
	for _, g := range gs {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", g, goals[g], goalPass[g])
		if g >= "G2" && g <= "G6" && goalPass[g] < perGoal {
			bar = false
		}
	}
	fmt.Fprintf(&b, "\nPass bar (≥ %d tasks, each of G2–G6 ≥ %d, python ≤ 0.1/run): **%v**\n\n", passBar, perGoal, map[bool]string{true: "MET", false: "NOT MET"}[bar])
	b.WriteString("| task | goal | held out | runs passed | pass |\n|---|---|---|---|---|\n")
	for _, v := range vs {
		fmt.Fprintf(&b, "| %s | %s | %v | %d/%d | %v |\n", v.Task, v.Goal, v.HeldOut, v.Passed, v.Runs, v.Pass)
	}
	b.WriteString("\n## Failures\n\n")
	for _, r := range rs {
		if !r.Pass {
			fmt.Fprintf(&b, "- %s run %d: %s\n", r.Task, r.Run, truncate(strings.Join(append(r.Failed, r.Err), "; "), 400))
		}
	}
	return b.String()
}

// liveMain is -mode live.
func liveMain(ctx context.Context, cl *client, o liveOpts, tasks []*gameTask, runs int, evalCap float64, outDir, label string) {
	must(os.MkdirAll(outDir, 0o755))
	stamp := time.Now().UTC().Format("20060102T150405Z")
	rf, err := os.Create(filepath.Join(outDir, fmt.Sprintf("live-%s.jsonl", stamp)))
	must(err)
	defer rf.Close()
	logf, err := os.Create(filepath.Join(outDir, fmt.Sprintf("live-%s.server.log", stamp)))
	must(err)
	defer logf.Close()

	// Baseline checkpoints, made once per project in its own session.
	for name := range o.projects {
		if o.checkpoint[name] != "" {
			continue
		}
		s, err := startLiveSession(ctx, o, name, logf)
		must(err)
		_, _ = s.call(ctx, "editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25})
		tag, err := baseline(ctx, s)
		s.cs.Close()
		must(err)
		o.checkpoint[name] = tag
		fmt.Fprintf(os.Stderr, "[live] %s baseline %s\n", name, tag)
	}
	var results []liveResult
	spent := 0.0
	for _, t := range tasks {
		for run := 1; run <= runs; run++ {
			var r liveResult
			if evalCap > 0 && spent >= evalCap {
				r = liveResult{Task: t.ID, Goal: t.Goal, HeldOut: t.HeldOut, Run: run, Model: o.model, Aborted: "eval_cap",
					Failed: []string{"aborted: eval_cap"}}
			} else {
				r = runLive(ctx, cl, o, t, run, logf)
			}
			spent += r.CostUSD
			results = append(results, r)
			line, _ := json.Marshal(r)
			fmt.Fprintln(rf, string(line))
			fmt.Fprintf(os.Stderr, "[live %d] %s run%d pass=%v turns=%d python=%d $%.3f %s %s\n", len(results), t.ID, run, r.Pass,
				r.Turns, r.PythonCalls, r.CostUSD, r.Aborted, truncate(strings.Join(r.Failed, "; "), 200))
		}
	}
	report := liveReport(results, label, 20, 2)
	must(os.WriteFile(filepath.Join(outDir, fmt.Sprintf("live-%s.md", stamp)), []byte(report), 0o644))
	fmt.Println(report)
}

// readLive loads a live results file (for re-reporting).
func readLive(path string) []liveResult {
	b, err := os.ReadFile(path)
	must(err)
	var out []liveResult
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r liveResult
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}
