package main

// The live game-making eval (docs/plans/REMEDIATION_PLAN.md §2): agents do multi-step
// game tasks through a REAL server against a scratch copy of a game project in a live
// editor. Every run starts from the same files (git_revert to a baseline checkpoint), and
// is judged only by mechanical checks — harness-side probes of the world/assets (Python,
// or a server tool call), the recorded tool calls (an async call counts only when its job
// succeeded), and the final ANSWER line matched against a probed value. No LLM judge. A
// run that exceeds its cost cap or turn limit, or is served by a model without a price,
// fails.

import (
	"context"
	"encoding/base64"
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
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// liveSystemPrompt asks for a machine-checkable final line: the answer check reads only
// it, so listing several candidate values cannot pass.
const liveSystemPrompt = systemPrompt + ` When the task asks for a value or a verdict, end your final reply with ` +
	`exactly one line "ANSWER: <value>" (several values: "ANSWER: name=<value>, name=<value>"; a verdict: ` +
	`"ANSWER: PASS" or "ANSWER: FAIL"). Write numbers in full digits (12500, not 12.5k).`

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

// liveCheck is one mechanical pass condition: exactly one of Probe, Call, Called, Answer.
type liveCheck struct {
	Name string `json:"name"`
	// Probe is Python the harness runs after the agent stops; its last line printed as
	// "RESULT <json object>" is matched against Expect.
	Probe string `json:"probe,omitempty"`
	// Call is a server tool call the harness makes after the agent stops; its structured
	// result is matched against Expect.
	Call   *liveCall     `json:"call,omitempty"`
	Expect []expectation `json:"expect,omitempty"`
	Called *calledCheck  `json:"called,omitempty"`
	Answer *answerCheck  `json:"answer,omitempty"`
}

func (c liveCheck) isProbe() bool { return c.Probe != "" || c.Call != nil }

type expectation struct {
	Path  string `json:"path"` // dot path into the probe result
	Op    string `json:"op"`   // eq ne gt gte lt lte contains exists
	Value any    `json:"value,omitempty"`
	// ValueFrom takes the expected value from an earlier probe ("<check>.<path>").
	ValueFrom string `json:"value_from,omitempty"`
}

// calledCheck: the agent called Tool (with op Op, if set) at least Min times (default
// 1) without an error — and, for a call that started a job, with that job succeeded —
// after such a call to After (if set).
type calledCheck struct {
	Tool  string `json:"tool"`
	Op    string `json:"op,omitempty"`
	After string `json:"after,omitempty"` // "tool" or "tool/op"
	Min   int    `json:"min,omitempty"`
}

// answerCheck reads the final reply's "ANSWER:" line: with From, its one number (or the
// number after "<Key>=") must be within Tolerance of the probed value at From
// ("<check>.<path>"); with Regex (G1 verdicts only), the line must match.
type answerCheck struct {
	From      string  `json:"from,omitempty"`
	Key       string  `json:"key,omitempty"`
	Tolerance float64 `json:"tolerance,omitempty"`
	// RelTolerance widens Tolerance to this fraction of the expected value.
	RelTolerance float64 `json:"rel_tolerance,omitempty"`
	Regex        string  `json:"regex,omitempty"`
}

// gameTaskFile is the task file: Python helpers shared by every probe and python
// setup call (prepended to their code), and the tasks.
type gameTaskFile struct {
	Prelude string      `json:"prelude"`
	Tasks   []*gameTask `json:"tasks"`
}

// loadGameTasks reads a task file and prepends the prelude to every probe and to the
// code of every python setup call.
// loadGameTaskFiles reads a comma-separated list of task files (each with its own
// prelude): the plan's tasks and, for the final run, the sealed held-out file — merged
// without anyone opening the sealed one.
func loadGameTaskFiles(paths string) ([]*gameTask, error) {
	var all []*gameTask
	for _, p := range strings.Split(paths, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		ts, err := loadGameTasks(p)
		if err != nil {
			return nil, err
		}
		all = append(all, ts...)
	}
	return all, nil
}

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

var goalRE = regexp.MustCompile(`^G[1-6]$`)

// lintGameTasks checks the task file's structure and the plan's rules: unique ids, a
// known goal and project, every check well-formed and every From/ValueFrom naming an
// earlier probe; every G2–G6 task grounded in game state (≥ 1 probe, and an answer, if
// any, read from a probe — a regex answer is for G1 verdicts only); ≥ minPerGoal tasks
// per goal G2–G6 and ≥ minMulti multi-step tasks.
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
		if !goalRE.MatchString(t.Goal) {
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
		nProbe := 0
		earlier := func(ref string) bool { return probes[strings.SplitN(ref, ".", 2)[0]] }
		for i, c := range t.Checks {
			cw := fmt.Sprintf("%s check %d (%s)", where, i, c.Name)
			n := 0
			if c.Probe != "" {
				n++
			}
			if c.Call != nil {
				n++
				if c.Call.Tool == "" {
					errs = append(errs, cw+": call needs a tool")
				}
			}
			if c.isProbe() {
				nProbe++
				if c.Name == "" {
					errs = append(errs, cw+": a probe needs a name")
				}
				for _, e := range c.Expect {
					if !contains([]string{"eq", "ne", "gt", "gte", "lt", "lte", "contains", "word", "exists"}, e.Op) || e.Path == "" {
						errs = append(errs, fmt.Sprintf("%s: bad expectation %+v", cw, e))
					}
					if e.ValueFrom != "" && !earlier(e.ValueFrom) {
						errs = append(errs, cw+": value_from must name an earlier probe")
					}
				}
				probes[c.Name] = true
			}
			if c.Called != nil {
				n++
				if c.Called.Tool == "" {
					errs = append(errs, cw+": called needs a tool")
				}
			}
			if c.Answer != nil {
				n++
				switch {
				case c.Answer.From == "" && c.Answer.Regex == "":
					errs = append(errs, cw+": answer needs from or regex")
				case c.Answer.From != "" && !earlier(c.Answer.From):
					errs = append(errs, cw+": answer.from must name an earlier probe")
				case c.Answer.Regex != "" && t.Goal != "G1":
					errs = append(errs, cw+": a regex answer is allowed only for G1 verdicts (others read a probed value)")
				}
				if c.Answer.Regex != "" {
					if _, err := regexp.Compile(c.Answer.Regex); err != nil {
						errs = append(errs, cw+": "+err.Error())
					}
				}
			}
			if n != 1 {
				errs = append(errs, cw+": exactly one of probe, call, called, answer")
			}
		}
		// Answers: one value may be plain; several must each have a key, and the prompt
		// must show the agent every key ("<key>=").
		var answers []*answerCheck
		for i := range t.Checks {
			if t.Checks[i].Answer != nil && t.Checks[i].Answer.From != "" {
				answers = append(answers, t.Checks[i].Answer)
			}
		}
		for _, a := range answers {
			if len(answers) > 1 && a.Key == "" {
				errs = append(errs, where+": several answer values: every answer check needs a key")
			}
			if a.Key != "" && !strings.Contains(t.Prompt, a.Key+"=") {
				errs = append(errs, fmt.Sprintf("%s: the prompt never shows the answer key %q (write %s=<value>)", where, a.Key, a.Key))
			}
		}
		if t.Goal != "G1" && nProbe == 0 {
			errs = append(errs, where+": not grounded: a G2–G6 task needs at least one probe of game state")
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
		case c.isProbe():
			if e := probeErr[c.Name]; e != "" {
				failed = append(failed, c.Name+": probe failed: "+e)
				continue
			}
			for _, e := range c.Expect {
				if e.ValueFrom != "" {
					name, path, _ := strings.Cut(e.ValueFrom, ".")
					e.Value = lookup(probes[name], path)
				}
				if !expectHolds(probes[c.Name], e) {
					failed = append(failed, fmt.Sprintf("%s: %s %s %v (got %v)", c.Name, e.Path, e.Op, e.Value, lookup(probes[c.Name], e.Path)))
				}
			}
		case c.Called != nil:
			if !calledHolds(*c.Called, calls) {
				failed = append(failed, c.Name+": no matching completed call "+describeCalled(*c.Called))
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
		if e.Value == nil {
			return false // an unresolved value_from never matches
		}
		eq := fmt.Sprint(got) == fmt.Sprint(e.Value)
		if a, ok := num(got); ok {
			if b, ok := num(e.Value); ok {
				eq = math.Abs(a-b) < 1e-9
			}
		}
		return eq == (e.Op == "eq")
	case "word":
		// a whole word/number of a string ("WAVE 3" has 3; "100 HP" has no 1)
		s, ok := got.(string)
		if !ok || e.Value == nil {
			return false
		}
		want := fmt.Sprint(e.Value)
		if f, isNum := e.Value.(float64); isNum && f == math.Trunc(f) {
			want = strconv.FormatFloat(f, 'f', -1, 64)
		}
		return regexp.MustCompile(`(^|[^0-9A-Za-z])` + regexp.QuoteMeta(want) + `([^0-9A-Za-z]|$)`).MatchString(s)
	case "contains":
		if e.Value == nil {
			return false
		}
		want := fmt.Sprint(e.Value)
		if f, ok := e.Value.(float64); ok && f == math.Trunc(f) {
			want = strconv.FormatFloat(f, 'f', -1, 64)
		}
		switch g := got.(type) {
		case string:
			return strings.Contains(g, want)
		case []any:
			for _, x := range g {
				if fmt.Sprint(x) == want {
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

// callMatches: the call is tool[/op], did not fail, and — if it started a job — the
// job succeeded.
func callMatches(c toolCall, spec string) bool {
	tool, op, _ := strings.Cut(spec, "/")
	if c.Tool != tool || c.Error != "" {
		return false
	}
	if c.Job != "" && c.JobState != "succeeded" {
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

// A number in an ANSWER line: thousands separators allowed ("12,345"), an optional
// currency sign ("$12,345", "-$5"), a sign only at the start or after a space, "=", ":",
// "(" or a currency sign (so "wave-2" is 2, not -2). U+2212 (−) is a minus.
var numberRE = regexp.MustCompile(`(?:^|[\s=:($€£])(-?[$€£]?-?\d{1,3}(?:,\d{3})+(?:\.\d+)?|-?[$€£]?-?\d+(?:\.\d+)?)|(\d+(?:\.\d+)?)`)

func answerNumbers(s string) []float64 {
	s = strings.ReplaceAll(s, "−", "-")
	var out []float64
	for _, m := range numberRE.FindAllStringSubmatch(s, -1) {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		v = strings.NewReplacer(",", "", "$", "", "€", "", "£", "").Replace(v)
		v = strings.Replace(v, "--", "-", 1)
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out = append(out, f)
		}
	}
	return out
}

var abbreviated = regexp.MustCompile(`\d\s*(?:[kKmMbB]|bn|mn)\b`)

// keyedValue is the text after "<key>=" up to the next ", <name>=" (or the end), so a
// thousands separator inside the value is kept.
func keyedValue(line, key string) (string, bool) {
	m := regexp.MustCompile(`(?i)(?:^|[\s,;])` + regexp.QuoteMeta(key) + `\s*=\s*(.*?)\s*(?:[,;]\s*[A-Za-z_][A-Za-z0-9_]*\s*=|$)`).FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// answerLine is the content of the reply's last "ANSWER:" line ("" when there is none).
func answerLine(reply string) string {
	lines := strings.Split(strings.ReplaceAll(reply, "\r", ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(strings.Trim(strings.TrimSpace(lines[i]), "*`_"))
		if len(l) >= 7 && strings.EqualFold(l[:7], "ANSWER:") {
			return strings.TrimSpace(strings.Trim(strings.TrimSpace(l[7:]), "*`_"))
		}
	}
	return ""
}

func answerHolds(a answerCheck, probes map[string]map[string]any, reply string) bool {
	line := answerLine(reply)
	if line == "" {
		return false
	}
	if a.Regex != "" {
		return regexp.MustCompile(a.Regex).MatchString(line)
	}
	name, path, _ := strings.Cut(a.From, ".")
	want, ok := num(lookup(probes[name], path))
	if !ok {
		return false
	}
	part := line
	if a.Key != "" {
		v, ok := keyedValue(line, a.Key)
		if !ok {
			return false
		}
		part = v
	}
	if abbreviated.MatchString(part) {
		return false // "1.5k" is not 1.5: an abbreviated number is unparseable, not a guess
	}
	ns := answerNumbers(part)
	tol := math.Max(a.Tolerance, a.RelTolerance*math.Abs(want))
	return len(ns) == 1 && math.Abs(ns[0]-want) <= tol
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
}

type liveResult struct {
	Task        string     `json:"task"`
	Goal        string     `json:"goal"`
	HeldOut     bool       `json:"held_out,omitempty"`
	Run         int        `json:"run"`
	Model       string     `json:"model"`
	Pass        bool       `json:"pass"`
	Failed      []string   `json:"failed,omitempty"`
	Aborted     string     `json:"aborted,omitempty"` // cost_cap | turn_limit | unpriced | eval_cap | error
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

// baseline makes the project's baseline checkpoint.
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
	// A play session outlives the server and the revert (which leaves an unchanged
	// project's editor open): stop it, or the run starts in the last run's game world.
	if _, err := s.call(ctx, "pie", map[string]any{"op": "stop"}); err != nil && !strings.Contains(err.Error(), "EDITOR_UNREACHABLE") {
		return fmt.Errorf("stop PIE: %w", err)
	}
	out, err := s.call(ctx, "git_revert", map[string]any{"to": tag, "discard_dirty": true, "wait_s": 25})
	if err != nil {
		return err
	}
	if _, err := s.waitJob(ctx, out, 15*time.Minute); err != nil {
		return err
	}
	out, err = s.call(ctx, "editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25})
	if err == nil {
		_, err = s.waitJob(ctx, out, 10*time.Minute)
	}
	return err
}

// clearEvidence removes the server-written outputs probes read, in a project's Saved/.
func clearEvidence(project string) error {
	saved := filepath.Join(project, "Saved")
	for _, p := range []string{filepath.Join(saved, "MCP", "capture"), filepath.Join(saved, "MCP", "playtest"),
		filepath.Join(saved, "gameeval_baseline.json")} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
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
	// Saved/ is not in git, so the reset keeps it: clear the evidence the probes read
	// (playtest and batch outputs, the setup baseline) so no run is scored on an earlier
	// run's files.
	if err := clearEvidence(o.projects[t.Project]); err != nil {
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
		resp, err := cl.create(ctx, request{Model: o.model, MaxTokens: 8192, System: liveSystemPrompt, Tools: tools, Messages: withHistoryCache(msgs)})
		if err != nil {
			r.Err, r.Aborted = err.Error(), "error"
			break
		}
		r.Usage.add(resp.Usage)
		r.Served = append(r.Served, resp.Model)
		r.Turns++
		cost, priced := turnCost(resp.Usage, resp.Model, o)
		r.CostUSD += cost
		if !priced {
			// An unpriced model would silently disable the caps: fail the run instead.
			r.Aborted = "unpriced"
			r.Err = fmt.Sprintf("served by %q, which has no -prices entry", resp.Model)
			break
		}
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
				res, code, structured := callLive(ctx, s, b.Name, args)
				tc := toolCall{Tool: b.Name, Args: args, Error: code}
				if id, ok := structured["job_id"].(string); ok && id != "" && b.Name != "job" {
					tc.Job = id
				}
				r.Calls = append(r.Calls, tc)
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
	// The final state of every job the agent started (an async call counts only when its
	// job succeeded — submitting a playtest proves nothing).
	for i := range r.Calls {
		if r.Calls[i].Job == "" {
			continue
		}
		jctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		st, err := s.call(jctx, "job", map[string]any{"op": "status", "job_id": r.Calls[i].Job})
		cancel()
		if err == nil {
			r.Calls[i].JobState, _ = st["state"].(string)
		}
	}
	probes := map[string]map[string]any{}
	probeErr := map[string]string{}
	for _, c := range t.Checks {
		if !c.isProbe() {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		var m map[string]any
		var err error
		if c.Probe != "" {
			m, err = s.probe(pctx, c.Probe)
		} else {
			m, err = s.call(pctx, c.Call.Tool, c.Call.Args)
		}
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

// callLive calls a tool for the agent and renders the result as API content; code is
// the error envelope code ("" on success); structured is the structured result.
func callLive(ctx context.Context, s *liveSession, name string, args map[string]any) ([]block, string, map[string]any) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := s.cs.CallTool(cctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return []block{{Type: "text", Text: "error: " + err.Error()}}, "PROTOCOL", nil
	}
	structured, _ := res.StructuredContent.(map[string]any)
	var out []block
	code := ""
	if res.IsError {
		code = "ERROR"
		if e, ok := structured["error"].(map[string]any); ok {
			code = fmt.Sprint(e["code"])
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		out = append(out, block{Type: "text", Text: truncate(string(b), 20000)})
	}
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			if res.StructuredContent == nil {
				out = append(out, block{Type: "text", Text: truncate(v.Text, 20000)})
			}
		case *mcp.ImageContent:
			out = append(out, block{Type: "image", Source: &imageSource{Type: "base64", MediaType: v.MIMEType,
				Data: base64.StdEncoding.EncodeToString(v.Data)}})
		}
	}
	if len(out) == 0 {
		out = append(out, block{Type: "text", Text: "(no output)"})
	}
	return out, code, structured
}

// turnCost prices one turn at the model that served it (the router may serve a
// different model each turn; cache reads and writes as in cost). A served model
// without a price is reported unpriced, so the caps never run blind.
func turnCost(u usage, served string, o liveOpts) (float64, bool) {
	p, ok := o.prices[served]
	if !ok {
		return 0, false
	}
	return cost(u, p), true
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

// passBarMet: ≥ passBar tasks pass, EVERY goal G2–G6 has ≥ perGoal passing tasks (a
// goal with no tasks in the results fails the bar), ≤ 0.1 python calls per run.
func passBarMet(vs []taskVerdict, pyRate float64, passBar, perGoal int) bool {
	passed := 0
	goalPass := map[string]int{}
	for _, v := range vs {
		if v.Pass {
			passed++
			goalPass[v.Goal]++
		}
	}
	if passed < passBar || pyRate > 0.1 {
		return false
	}
	for _, g := range []string{"G2", "G3", "G4", "G5", "G6"} {
		if goalPass[g] < perGoal {
			return false
		}
	}
	return true
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
	b.WriteString("| goal | tasks | pass |\n|---|---|---|\n")
	for _, g := range []string{"G1", "G2", "G3", "G4", "G5", "G6"} {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", g, goals[g], goalPass[g])
	}
	fmt.Fprintf(&b, "\nPass bar (≥ %d tasks, each of G2–G6 ≥ %d, python ≤ 0.1/run): **%v**\n\n", passBar, perGoal,
		map[bool]string{true: "MET", false: "NOT MET"}[passBarMet(vs, pyRate, passBar, perGoal)])
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
	if _, ok := o.prices[o.model]; !ok {
		// Without a price the per-run and per-eval caps cannot work: refuse to spend.
		fmt.Fprintf(os.Stderr, "no -prices entry for model %q: the cost caps would be disabled\n", o.model)
		os.Exit(2)
	}
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
		out, err := s.call(ctx, "editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25})
		if err == nil {
			_, err = s.waitJob(ctx, out, 10*time.Minute)
		}
		must(err)
		tag, err := baseline(ctx, s)
		s.cs.Close()
		must(err)
		o.checkpoint[name] = tag
		fmt.Fprintf(os.Stderr, "[live] %s baseline %s\n", name, tag)
	}
	// One queue per project: each project has its own editor, so the projects run side by
	// side and a project's runs one at a time. The eval cap is shared.
	var (
		mu      sync.Mutex
		results []liveResult
		spent   float64
		wg      sync.WaitGroup
	)
	byProject := map[string][]*gameTask{}
	var order []string
	for _, t := range tasks {
		if _, ok := byProject[t.Project]; !ok {
			order = append(order, t.Project)
		}
		byProject[t.Project] = append(byProject[t.Project], t)
	}
	for _, name := range order {
		wg.Add(1)
		go func(queue []*gameTask) {
			defer wg.Done()
			for _, t := range queue {
				for run := 1; run <= runs; run++ {
					mu.Lock()
					capped := evalCap > 0 && spent >= evalCap
					mu.Unlock()
					var r liveResult
					if capped {
						r = liveResult{Task: t.ID, Goal: t.Goal, HeldOut: t.HeldOut, Run: run, Model: o.model, Aborted: "eval_cap",
							Failed: []string{"aborted: eval_cap"}}
					} else {
						r = runLive(ctx, cl, o, t, run, logf)
					}
					mu.Lock()
					spent += r.CostUSD
					results = append(results, r)
					line, _ := json.Marshal(r)
					fmt.Fprintln(rf, string(line))
					fmt.Fprintf(os.Stderr, "[live %d] %s run%d pass=%v turns=%d python=%d $%.3f %s %s\n", len(results), t.ID, run, r.Pass,
						r.Turns, r.PythonCalls, r.CostUSD, r.Aborted, truncate(strings.Join(r.Failed, "; "), 200))
					mu.Unlock()
				}
			}
		}(byProject[name])
	}
	wg.Wait()
	report := liveReport(results, label, 20, 2)
	must(os.WriteFile(filepath.Join(outDir, fmt.Sprintf("live-%s.md", stamp)), []byte(report), 0o644))
	fmt.Println(report)
}

// selectGameTasks keeps the -only ids; an id that is not in the file is an error.
func selectGameTasks(ts []*gameTask, only string) ([]*gameTask, error) {
	if only == "" {
		return ts, nil
	}
	byID := map[string]*gameTask{}
	for _, t := range ts {
		byID[t.ID] = t
	}
	var sel []*gameTask
	for _, id := range strings.Split(only, ",") {
		t, ok := byID[strings.TrimSpace(id)]
		if !ok {
			return nil, fmt.Errorf("-only: no task %q", id)
		}
		sel = append(sel, t)
	}
	return sel, nil
}

// liveMerge reads live results files in order; a later file's (task, run) replaces an
// earlier one. The report says which files it merged.
func liveMerge(files []string, label string) string {
	byKey := map[string]liveResult{}
	var order []string
	for _, f := range files {
		raw, err := os.ReadFile(strings.TrimSpace(f))
		must(err)
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var r liveResult
			must(json.Unmarshal([]byte(line), &r))
			k := fmt.Sprintf("%s#%d", r.Task, r.Run)
			if _, ok := byKey[k]; !ok {
				order = append(order, k)
			}
			byKey[k] = r
		}
	}
	rs := make([]liveResult, 0, len(order))
	for _, k := range order {
		rs = append(rs, byKey[k])
	}
	return liveReport(rs, label+" (merged: "+strings.Join(files, ", ")+")", 20, 2)
}
