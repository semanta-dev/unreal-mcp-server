// Command tooleval is the plan §3.5 tool-selection eval: real agent loops choosing tools
// on Unreal tasks. It compares the v1 and v2 tool surfaces on the FIRST call (right tool,
// schema-valid arguments) and runs v2 end to end against the stateful op emulator.
//
//	tooleval -mode dry                       # no API: replay each task's reference calls
//	tooleval -mode pilot -key-file <file>    # 5 tasks x 1 run x each model: token/cost estimate
//	tooleval -mode full  -key-file <file>    # every task x -runs x each model
//	tooleval -mode live-lint                 # no API: check the game-making task file
//	tooleval -mode live -projects aesir=<dir>,polyworld=<dir> -server <unreal-mcp> -server-args "<flags>"
//	                                         # the live game-making eval (REMEDIATION_PLAN.md §2)
//
// The v1 surface cannot run end to end (its adapter and companion ops are gone), so its
// tools are the recorded v1 tools/list (testdata/v1_tools_list.json) and only the first
// call is scored for both surfaces; end-to-end success is v2 only.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/session"
)

const systemPrompt = `You are a game-development agent working in a live Unreal Editor 5.7 project through the MCP tools ` +
	`provided. Complete the user's task with the tools; do not ask questions. When the task is done, reply with a ` +
	`short final answer that states any value the task asked for.`

type runResult struct {
	Task, Model, Surface string
	Run                  int
	FirstTool            string     `json:"first_tool"`
	FirstOK              bool       `json:"first_ok"`
	ArgsValid            bool       `json:"args_valid"`
	ArgsError            string     `json:"args_error,omitempty"`
	Success              *bool      `json:"success,omitempty"` // v2 only
	Failed               []string   `json:"failed,omitempty"`
	Calls                []toolCall `json:"calls,omitempty"`
	PythonCalls          int        `json:"python_calls"`
	Turns                int        `json:"turns"`
	Answer               string     `json:"answer,omitempty"`
	Usage                usage      `json:"usage"`
	Served               []string   `json:"served"` // model that served each turn
	Err                  string     `json:"error,omitempty"`
}

func main() {
	mode := flag.String("mode", "dry", "dry | pilot | full")
	tasksPath := flag.String("tasks", "cmd/tooleval/tasks.json", "task file")
	v1Path := flag.String("v1-tools", "cmd/tooleval/testdata/v1_tools_list.json", "recorded v1 tools/list")
	modelsFlag := flag.String("models", "claude-opus-5-5,claude-sonnet-5-5", "comma-separated model ids")
	runs := flag.Int("runs", 3, "runs per task and model (full)")
	pilotN := flag.Int("pilot-tasks", 5, "tasks in the pilot")
	maxTurns := flag.Int("max-turns", 12, "agent turns per v2 run")
	conc := flag.Int("concurrency", 4, "parallel runs")
	keyFile := flag.String("key-file", "", "file holding the API key (else ANTHROPIC_API_KEY)")
	outDir := flag.String("out", "docs/validation/tooleval", "results directory")
	prices := flag.String("prices", "claude-opus-5-5=15/75,claude-sonnet-5-5=3/15", "USD per million input/output tokens per model (cache read 10%, cache write 125% of input)")
	seed := flag.Int64("seed", 7, "task-order seed")
	rerun := flag.String("rerun", "", "full: re-run only the runs that errored in this results .jsonl")
	only := flag.String("only", "", "comma-separated task ids to run (pilot/full)")
	merge := flag.String("merge", "", "report only: comma-separated results .jsonl files (a later file's run replaces an earlier one)")
	gameTasks := flag.String("game-tasks", "docs/validation/gameeval/tasks.json", "live: the game-making task file")
	server := flag.String("server", "dist/unreal-mcp.exe", "live: the server binary under test")
	serverArgs := flag.String("server-args", "-engine D:/Unreal/Engine/UE_5.7 -group 239.0.0.42:6799 -log-format text -log-level warn", "live: extra server flags")
	projectsFlag := flag.String("projects", "aesir=../_p7scratch/aesir,polyworld=../_p7scratch/PolyWorld", "live: name=scratch project dir,…")
	portsFlag := flag.String("ports", "aesir=127.0.0.1:6791,polyworld=127.0.0.1:6792", "live: name=command addr,…")
	checkpoints := flag.String("checkpoints", "", "live: name=baseline checkpoint,… (default: make one per project at start)")
	liveTurns := flag.Int("live-turns", 40, "live: agent turns per run (a run that needs more fails)")
	costCap := flag.Float64("cost-cap", 1.50, "live: USD per run (a run that costs more is aborted and fails)")
	evalCap := flag.Float64("eval-cap", 150, "live: USD for the whole eval (later runs are aborted)")
	label := flag.String("label", "live", "live: report label (e.g. baseline v2.0.2)")
	flag.Parse()

	if *mode == "live-lint" || *mode == "live" {
		gts, err := loadGameTasks(*gameTasks)
		must(err)
		projects := kv(*projectsFlag)
		var names []string
		for n := range projects {
			names = append(names, n)
		}
		// Structure errors are always fatal; the coverage rules apply to the whole file,
		// so a -only subset is linted for structure alone.
		minGoal, minMulti := 3, 8
		if *only != "" {
			minGoal, minMulti = 0, 0
		}
		gts, err = selectGameTasks(gts, *only)
		must(err)
		if errs := lintGameTasks(gts, names, minGoal, minMulti); len(errs) > 0 {
			for _, e := range errs {
				fmt.Println(e)
			}
			os.Exit(1)
		}
		if *mode == "live-lint" {
			fmt.Printf("%d game tasks: OK\n", len(gts))
			return
		}
		cl := apiClient(*keyFile)
		o := liveOpts{server: *server, serverArgs: strings.Fields(*serverArgs), projects: projects, ports: kv(*portsFlag),
			checkpoint: kv(*checkpoints), maxTurns: *liveTurns, costCap: *costCap, prices: parsePrices(*prices),
			model: strings.Split(*modelsFlag, ",")[0]}
		liveMain(context.Background(), cl, o, gts, *runs, *evalCap, *outDir, *label)
		return
	}

	tasks, err := loadTasks(*tasksPath)
	must(err)
	v1Tools, v1Schemas, err := loadV1(*v1Path)
	must(err)
	ctx := context.Background()

	if *merge != "" {
		var rs []runResult
		idx := map[string]int{}
		for _, f := range strings.Split(*merge, ",") {
			for _, r := range readResults(f) {
				k := fmt.Sprintf("%s|%s|%s|%d", r.Task, r.Model, r.Surface, r.Run)
				if i, ok := idx[k]; ok {
					rs[i] = r
				} else {
					idx[k] = len(rs)
					rs = append(rs, r)
				}
			}
		}
		// First-tool hits are re-scored against the current task file (the calls are recorded).
		byID := map[string]*task{}
		for _, t := range tasks {
			byID[t.ID] = t
		}
		for i := range rs {
			if t := byID[rs[i].Task]; t != nil && rs[i].FirstTool != "" {
				want := t.FirstV2
				if rs[i].Surface == "v1" {
					want = t.FirstV1
				}
				rs[i].FirstOK = contains(want, rs[i].FirstTool)
			}
		}
		models := strings.Split(*modelsFlag, ",")
		report := summarize(rs, models, parsePrices(*prices), "full (merged)", len(tasks), *runs, len(tasks), *runs)
		must(os.WriteFile(filepath.Join(*outDir, "full-merged.md"), []byte(report), 0o644))
		fmt.Println(report)
		return
	}
	tasks, pending := activeTasks(tasks, allV2Tools())
	if len(pending) > 0 {
		fmt.Fprintf(os.Stderr, "%d task(s) wait for tools not built yet: %v\n", len(pending), pending)
	}
	if *mode == "dry" {
		code := dryRun(ctx, tasks)
		for _, t := range tasks {
			for _, n := range t.FirstV1 {
				if _, ok := v1Schemas[n]; !ok {
					fmt.Printf("%s: first_v1 %q is not a v1 tool\n", t.ID, n)
					code = 1
				}
			}
		}
		_ = v1Tools
		os.Exit(code)
	}
	cl := apiClient(*keyFile)
	models := strings.Split(*modelsFlag, ",")
	order := append([]*task(nil), tasks...)
	rand.New(rand.NewSource(*seed)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	nRuns := *runs
	if *only != "" {
		keep := map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			keep[id] = true
		}
		var sel []*task
		for _, t := range order {
			if keep[t.ID] {
				sel = append(sel, t)
			}
		}
		order = sel
	}
	if *mode == "pilot" {
		order = order[:min(*pilotN, len(order))]
		nRuns = 1
	}

	type job struct {
		t       *task
		model   string
		run     int
		surface string
	}
	var jobs []job
	if *rerun != "" {
		byID := map[string]*task{}
		for _, t := range tasks {
			byID[t.ID] = t
		}
		for _, r := range readResults(*rerun) {
			if r.Err != "" && byID[r.Task] != nil {
				jobs = append(jobs, job{byID[r.Task], r.Model, r.Run, r.Surface})
			}
		}
		order = nil
	}
	for _, t := range order {
		for _, m := range models {
			for r := 1; r <= nRuns; r++ {
				jobs = append(jobs, job{t, m, r, "v1"}, job{t, m, r, "v2"})
			}
		}
	}
	must(os.MkdirAll(*outDir, 0o755))
	stamp := time.Now().UTC().Format("20060102T150405Z")
	rf, err := os.Create(filepath.Join(*outDir, fmt.Sprintf("%s-%s.jsonl", *mode, stamp)))
	must(err)
	defer rf.Close()

	var mu sync.Mutex
	var results []runResult
	sem := make(chan struct{}, *conc)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			var r runResult
			if j.surface == "v1" {
				r = runV1(ctx, cl, j.model, j.t, v1Tools, v1Schemas)
			} else {
				r = runV2(ctx, cl, j.model, j.t, *maxTurns)
			}
			r.Run = j.run
			mu.Lock()
			defer mu.Unlock()
			results = append(results, r)
			b, _ := json.Marshal(r)
			fmt.Fprintln(rf, string(b))
			status := "ok"
			if r.Err != "" {
				status = "ERR " + truncate(r.Err, 120)
			}
			fmt.Fprintf(os.Stderr, "[%d/%d] %s %s %s run%d first=%s ok=%v valid=%v success=%v %s\n", len(results), len(jobs),
				j.t.ID, j.model, j.surface, j.run, r.FirstTool, r.FirstOK, r.ArgsValid, deref(r.Success), status)
		}(i, j)
	}
	wg.Wait()
	report := summarize(results, models, parsePrices(*prices), *mode, len(order), nRuns, len(tasks), *runs)
	must(os.WriteFile(filepath.Join(*outDir, fmt.Sprintf("%s-%s.md", *mode, stamp)), []byte(report), 0o644))
	fmt.Println(report)
}

// apiClient builds the Messages API client from ANTHROPIC_API_KEY / -key-file or
// ANTHROPIC_AUTH_TOKEN (bearer, e.g. a local router) and ANTHROPIC_BASE_URL.
func apiClient(keyFile string) *client {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		must(err)
		key = strings.TrimSpace(string(b))
	}
	token := os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if key == "" && token == "" {
		fmt.Fprintln(os.Stderr, "no credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, or pass -key-file")
		os.Exit(2)
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &client{key: key, token: token, baseURL: base, http: &http.Client{Timeout: 5 * time.Minute}}
}

// kv parses "a=x,b=y".
func kv(s string) map[string]string {
	m := map[string]string{}
	for _, p := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok {
			m[k] = v
		}
	}
	return m
}

func deref(b *bool) string {
	if b == nil {
		return "-"
	}
	return fmt.Sprint(*b)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "tooleval:", err)
		os.Exit(1)
	}
}

// --- surfaces ---------------------------------------------------------------------

func loadV1(path string) ([]apiTool, map[string]*jsonschema.Resolved, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var ts []*mcp.Tool
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, nil, err
	}
	return toAPITools(ts)
}

// toAPITools converts an MCP tool list to API tools (the last one marks the cache
// breakpoint, so the tool list is cached across turns and runs) plus resolved schemas.
func toAPITools(ts []*mcp.Tool) ([]apiTool, map[string]*jsonschema.Resolved, error) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	var out []apiTool
	schemas := map[string]*jsonschema.Resolved{}
	for _, t := range ts {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, apiTool{Name: t.Name, Description: t.Description, InputSchema: raw})
		var s jsonschema.Schema
		if err := json.Unmarshal(raw, &s); err == nil {
			if r, err := s.Resolve(nil); err == nil {
				schemas[t.Name] = r
			}
		}
	}
	if len(out) > 0 {
		out[len(out)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	return out, schemas, nil
}

func validArgs(schemas map[string]*jsonschema.Resolved, tool string, input json.RawMessage) (bool, string) {
	r, ok := schemas[tool]
	if !ok {
		return false, "unknown tool"
	}
	var v map[string]any
	if err := json.Unmarshal(input, &v); err != nil {
		return false, err.Error()
	}
	if err := r.Validate(&v); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func userTurn(t *task) message {
	return message{Role: "user", Content: raws([]block{{Type: "text", Text: t.Prompt}})}
}

// runV1 scores the first call on the v1 surface (one model turn).
func runV1(ctx context.Context, cl *client, model string, t *task, tools []apiTool, schemas map[string]*jsonschema.Resolved) runResult {
	r := runResult{Task: t.ID, Model: model, Surface: "v1"}
	resp, err := cl.create(ctx, request{Model: model, MaxTokens: 2048, System: systemPrompt, Tools: tools,
		Messages: []message{userTurn(t)}})
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Usage.add(resp.Usage)
	r.Served = append(r.Served, resp.Model)
	r.Turns = 1
	for _, b := range parseBlocks(resp.Content) {
		if b.Type == "tool_use" {
			r.FirstTool = b.Name
			r.FirstOK = contains(t.FirstV1, b.Name)
			r.ArgsValid, r.ArgsError = validArgs(schemas, b.Name, b.Input)
			if b.Name == "execute_python" {
				r.PythonCalls = 1
			}
			break
		}
	}
	return r
}

// runV2 runs the agent loop on the v2 surface against the emulator and scores the
// first call and the end state.
func runV2(ctx context.Context, cl *client, model string, t *task, maxTurns int) runResult {
	r := runResult{Task: t.ID, Model: model, Surface: "v2"}
	env, err := newV2Env(ctx, t)
	if err != nil {
		r.Err = "env: " + err.Error()
		return r
	}
	defer env.Close()
	listTools := func() ([]apiTool, map[string]*jsonschema.Resolved, error) {
		tl, err := env.cs.ListTools(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		return toAPITools(tl.Tools)
	}
	tools, schemas, err := listTools()
	if err != nil {
		r.Err = "tools/list: " + err.Error()
		return r
	}
	msgs := []message{userTurn(t)}
	first := true
	for turn := 0; turn < maxTurns; turn++ {
		resp, err := cl.create(ctx, request{Model: model, MaxTokens: 4096, System: systemPrompt, Tools: tools, Messages: msgs})
		if err != nil {
			r.Err = err.Error()
			break
		}
		r.Usage.add(resp.Usage)
		r.Served = append(r.Served, resp.Model)
		r.Turns++
		msgs = append(msgs, message{Role: "assistant", Content: resp.Content})
		var results []block
		for _, b := range parseBlocks(resp.Content) {
			switch b.Type {
			case "text":
				r.Answer = b.Text
			case "tool_use":
				var args map[string]any
				_ = json.Unmarshal(b.Input, &args)
				if first {
					first = false
					r.FirstTool = b.Name
					r.FirstOK = contains(t.FirstV2, b.Name)
					r.ArgsValid, r.ArgsError = validArgs(schemas, b.Name, b.Input)
				}
				if b.Name == "python" {
					r.PythonCalls++
				}
				res, code := callV2(ctx, env, b.Name, args)
				r.Calls = append(r.Calls, toolCall{Tool: b.Name, Args: args, Error: code})
				results = append(results, block{Type: "tool_result", ToolUseID: b.ID, Content: res, IsError: code != ""})
			}
		}
		if len(results) == 0 || resp.StopReason != "tool_use" {
			break
		}
		msgs = append(msgs, message{Role: "user", Content: raws(results)})
		if env.toolsChanged() {
			if nt, ns, err := listTools(); err == nil {
				tools, schemas = nt, ns
			}
		}
	}
	failed := evaluate(t, env.world.Snapshot(), r.Calls, r.Answer)
	ok := len(failed) == 0 && r.Err == ""
	r.Success, r.Failed = &ok, failed
	return r
}

// callV2 calls a v2 tool and renders its result as API content; code is the error
// envelope code ("" on success).
func callV2(ctx context.Context, env *v2Env, name string, args map[string]any) ([]block, string) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := env.cs.CallTool(cctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return []block{{Type: "text", Text: "error: " + err.Error()}}, "PROTOCOL"
	}
	var out []block
	code := ""
	if res.IsError {
		code = "ERROR"
		if m, ok := res.StructuredContent.(map[string]any); ok {
			if e, ok := m["error"].(map[string]any); ok {
				code = fmt.Sprint(e["code"])
			}
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
	return out, code
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// --- dry run ---------------------------------------------------------------------

// allV2Tools is every v2 tool name (all toolsets) for first_v2 validation.
func allV2Tools() map[string]bool {
	m := map[string]bool{}
	for _, sp := range evalCatalog(session.Deps{Projects: nil}) {
		m[sp.Name] = true
	}
	return m
}

// dryRun replays each task's reference v2 calls and checks its success predicate holds,
// proving every task is solvable on v2 before any API spend. Returns the exit code.
func dryRun(ctx context.Context, tasks []*task) int {
	bad := 0
	v2 := allV2Tools()
	for _, t := range tasks {
		for _, n := range t.FirstV2 {
			if !v2[n] {
				fmt.Printf("%-28s first_v2 %q is not a v2 tool\n", t.ID, n)
				bad++
			}
		}
		env, err := newV2Env(ctx, t)
		if err != nil {
			fmt.Printf("%-28s ENV %v\n", t.ID, err)
			bad++
			continue
		}
		var calls []toolCall
		for _, c := range t.Reference {
			_, code := callV2(ctx, env, c.Tool, c.Args)
			calls = append(calls, toolCall{Tool: c.Tool, Args: c.Args, Error: code})
			if code != "" {
				fmt.Printf("%-28s reference call %s %v failed: %s\n", t.ID, c.Tool, c.Args, code)
			}
		}
		failed := evaluate(t, env.world.Snapshot(), calls, t.ReferenceAnswer)
		env.Close()
		if len(failed) > 0 || len(t.Reference) == 0 {
			fmt.Printf("%-28s FAIL %v\n", t.ID, failed)
			bad++
		} else {
			fmt.Printf("%-28s ok (%d calls)\n", t.ID, len(t.Reference))
		}
	}
	fmt.Printf("\n%d/%d tasks solvable by their reference solution\n", len(tasks)-bad, len(tasks))
	if bad > 0 {
		return 1
	}
	return 0
}

// --- report ----------------------------------------------------------------------

type price struct{ in, out float64 }

func parsePrices(s string) map[string]price {
	m := map[string]price{}
	for _, kv := range strings.Split(s, ",") {
		name, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		var p price
		if _, err := fmt.Sscanf(v, "%f/%f", &p.in, &p.out); err == nil {
			m[name] = p
		}
	}
	return m
}

func cost(u usage, p price) float64 {
	return (float64(u.InputTokens)*p.in + float64(u.CacheCreationInputTokens)*p.in*1.25 +
		float64(u.CacheReadInputTokens)*p.in*0.1 + float64(u.OutputTokens)*p.out) / 1e6
}

// wilson returns the 95% Wilson score interval for k successes in n.
func wilson(k, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 0
	}
	z := 1.96
	p := float64(k) / float64(n)
	d := 1 + z*z/float64(n)
	c := p + z*z/(2*float64(n))
	m := z * math.Sqrt(p*(1-p)/float64(n)+z*z/(4*float64(n)*float64(n)))
	return (c - m) / d, (c + m) / d
}

func summarize(rs []runResult, models []string, prices map[string]price, mode string, nTasks, nRuns, allTasks, fullRuns int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Tool-selection eval — %s (%s)\n\n", mode, time.Now().UTC().Format("2006-01-02 15:04Z"))
	fmt.Fprintf(&b, "%d tasks × %d run(s) × %d model(s) × 2 surfaces. First call scored on both surfaces; end-to-end "+
		"success on v2 only (stateful emulator).\n\n", nTasks, nRuns, len(models))
	b.WriteString("| model | surface | runs | correct first tool | valid first args | end-to-end success (95% CI) | python calls/run | errors | cost USD |\n|---|---|---|---|---|---|---|---|---|\n")
	total := 0.0
	byModelCost := map[string]float64{}
	for _, m := range models {
		for _, s := range []string{"v1", "v2"} {
			n, first, valid, succ, succN, py, errs := 0, 0, 0, 0, 0, 0, 0
			var u usage
			for _, r := range rs {
				if r.Model != m || r.Surface != s {
					continue
				}
				n++
				u.add(r.Usage)
				if r.Err != "" {
					errs++
				}
				if r.FirstOK {
					first++
				}
				if r.ArgsValid {
					valid++
				}
				py += r.PythonCalls
				if r.Success != nil {
					succN++
					if *r.Success {
						succ++
					}
				}
			}
			c := cost(u, prices[m])
			total += c
			byModelCost[m] += c
			e2e := "—"
			if succN > 0 {
				lo, hi := wilson(succ, succN)
				e2e = fmt.Sprintf("%.0f%% (%.0f–%.0f)", 100*float64(succ)/float64(succN), 100*lo, 100*hi)
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %.2f | %d | %.2f |\n", m, s, n, pct(first, n), pct(valid, n), e2e,
				float64(py)/math.Max(1, float64(n)), errs, c)
		}
	}
	served := map[string]map[string]int{}
	for _, r := range rs {
		if served[r.Model] == nil {
			served[r.Model] = map[string]int{}
		}
		for _, s := range r.Served {
			served[r.Model][s]++
		}
	}
	b.WriteString("\n**Models that actually served the turns** (a router may substitute):\n\n")
	for _, m := range models {
		fmt.Fprintf(&b, "- requested `%s` → %v\n", m, served[m])
	}
	fmt.Fprintf(&b, "\nTotal cost: **$%.2f** (prices: input/output per MTok as passed in -prices; cache reads 10%%, writes 125%%).\n", total)
	if mode == "pilot" && nTasks > 0 {
		scale := float64(allTasks*fullRuns) / float64(nTasks*nRuns)
		fmt.Fprintf(&b, "\n**Full-run projection** (%d tasks × %d runs): about **$%.0f** (pilot × %.1f).\n", allTasks, fullRuns, total*scale, scale)
	}
	// Tools implicated in failures (plan: ≥ 3 → redesign).
	impl := map[string]int{}
	for _, r := range rs {
		if r.Surface != "v2" || r.Success == nil || *r.Success {
			continue
		}
		seen := map[string]bool{}
		for _, c := range r.Calls {
			if c.Error != "" && !seen[c.Tool] {
				impl[c.Tool]++
				seen[c.Tool] = true
			}
		}
		if r.FirstTool != "" && !r.FirstOK && !seen[r.FirstTool] {
			impl[r.FirstTool]++
		}
	}
	if len(impl) > 0 {
		b.WriteString("\n## v2 tools implicated in failed runs\n\n| tool | failed runs |\n|---|---|\n")
		var ks []string
		for k := range impl {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool { return impl[ks[i]] > impl[ks[j]] })
		for _, k := range ks {
			flag := ""
			if impl[k] >= 3 {
				flag = " ⚠"
			}
			fmt.Fprintf(&b, "| `%s` | %d%s |\n", k, impl[k], flag)
		}
	}
	// Per-task v2 success.
	b.WriteString("\n## Per task (v2 end-to-end successes / runs; first-tool hits v1 vs v2)\n\n| task | v2 success | v1 first | v2 first |\n|---|---|---|---|\n")
	type agg struct{ s, n, f1, n1, f2, n2 int }
	per := map[string]*agg{}
	for _, r := range rs {
		a := per[r.Task]
		if a == nil {
			a = &agg{}
			per[r.Task] = a
		}
		if r.Surface == "v1" {
			a.n1++
			if r.FirstOK {
				a.f1++
			}
		} else {
			a.n2++
			if r.FirstOK {
				a.f2++
			}
			if r.Success != nil {
				a.n++
				if *r.Success {
					a.s++
				}
			}
		}
	}
	var ids []string
	for k := range per {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := per[id]
		fmt.Fprintf(&b, "| %s | %d/%d | %d/%d | %d/%d |\n", id, a.s, a.n, a.f1, a.n1, a.f2, a.n2)
	}
	return b.String()
}

func pct(k, n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(k)/float64(n))
}

func readResults(path string) []runResult {
	b, err := os.ReadFile(path)
	must(err)
	var out []runResult
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r runResult
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}
