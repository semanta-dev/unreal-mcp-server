package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// scriptedRunner is a bridge.Runner that fakes the editor: it services the
// version sentinel + module install, returns scripted dispatch results, and
// returns a scripted raw result for non-dispatch (PROTO) snippets.
type scriptedRunner struct {
	loaded  bool
	version int
	// dispatch returns the op's result value (any JSON-serializable).
	dispatch func(op string, args map[string]any) any
	// dispatchExtra optionally adds editor output entries (e.g. a Warning the
	// editor logged during the op) alongside the __MCP_JSON__ envelope line.
	dispatchExtra []uexec.OutputEntry
	// raw handles PROTO snippets (execute_python / execute_console_command).
	raw func(code string) uexec.CommandResult
	// fail, when it returns a code, turns the op into an ok:false envelope.
	fail func(op string) (code, msg string, details map[string]any)
}

func (scriptedRunner) Generation() uint64 { return 1 }

func (r *scriptedRunner) RunCommand(_ context.Context, code string, mode uexec.ExecMode) (uexec.CommandResult, error) {
	if mode == uexec.ModeEval && strings.Contains(code, "_MCP2_BRIDGE_VERSION") {
		return uexec.CommandResult{Success: true, Result: strconv.Itoa(r.version)}, nil
	}
	body := strings.TrimPrefix(code, "# mcp\n")
	if mode == uexec.ModeExecFile && strings.Contains(code, "exec(compile(base64.b64decode") {
		r.loaded = true
		r.version = bridge.CompanionVersion()
		return uexec.CommandResult{Success: true}, nil
	}
	if mode == uexec.ModeExecFile && strings.HasPrefix(body, "_mcp2_dispatch(") {
		op, args := splitDispatch(body)
		result := r.dispatch(op, args)
		env, _ := json.Marshal(map[string]any{"ok": true, "result": result})
		if r.fail != nil {
			if code, msg, details := r.fail(op); code != "" {
				env, _ = json.Marshal(map[string]any{"ok": false, "error": msg, "code": code, "details": details})
			}
		}
		out := append([]uexec.OutputEntry{}, r.dispatchExtra...)
		out = append(out, uexec.OutputEntry{Type: "Info", Output: "__MCP_JSON__" + string(env)})
		return uexec.CommandResult{Success: true, Output: out}, nil
	}
	// PROTO raw path.
	if r.raw != nil {
		return r.raw(body), nil
	}
	return uexec.CommandResult{Success: true}, nil
}

func splitDispatch(code string) (string, map[string]any) {
	inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(code), "_mcp2_dispatch("), ")")
	parts := strings.SplitN(inner, ", ", 2)
	op, _ := strconv.Unquote(parts[0])
	var args map[string]any
	if len(parts) == 2 {
		b64, _ := strconv.Unquote(parts[1])
		raw, _ := base64.StdEncoding.DecodeString(b64)
		_ = json.Unmarshal(raw, &args)
	}
	return op, args
}

// callTool spins up the server over an in-memory transport and calls one tool.
func callTool(t *testing.T, r bridge.Runner, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	b := bridge.New(r, bridge.Options{})
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "test"}, nil)
	RegisterAll(srv, Deps{Bridge: b})
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

// TestConsoleSurfacesEditorLog: warnings the editor logs while running a console
// command reach the agent as editor_log (v1 parity: format_output).
func TestConsoleSurfacesEditorLog(t *testing.T) {
	r := &scriptedRunner{
		dispatch: func(op string, args map[string]any) any {
			return map[string]any{"ran": args["command"], "world": args["world"]}
		},
		dispatchExtra: []uexec.OutputEntry{{Type: "Warning", Output: "Command not recognized: badcvar"}},
	}
	res := callTool(t, r, "console", map[string]any{"command": "badcvar"})
	sc := structuredMap(t, res)
	if sc["world"] != "editor" {
		t.Fatalf("console must default to the editor world: %v", sc)
	}
	if log, _ := sc["editor_log"].([]any); len(log) != 1 || log[0] != "[Warning] Command not recognized: badcvar" {
		t.Fatalf("captured warning dropped: %v", sc["editor_log"])
	}
}

// TestConsoleReturnsInfoOutput: a CVar query's answer is an Info/Display line; v1
// returned every captured line, so console's `output` must too.
func TestConsoleReturnsInfoOutput(t *testing.T) {
	r := &scriptedRunner{
		dispatch: func(op string, args map[string]any) any { return map[string]any{"ran": args["command"]} },
		dispatchExtra: []uexec.OutputEntry{{Type: "Info", Output: "r.ScreenPercentage = \"100\"\n"},
			{Type: "Warning", Output: "deprecated cvar"}},
	}
	res := callTool(t, r, "console", map[string]any{"command": "r.ScreenPercentage"})
	sc := structuredMap(t, res)
	out, _ := sc["output"].([]any)
	if len(out) != 2 || out[0] != `r.ScreenPercentage = "100"` {
		t.Fatalf("console output = %v", sc["output"])
	}
	if log, _ := sc["editor_log"].([]any); len(log) != 1 {
		t.Fatalf("editor_log keeps only warnings/errors: %v", sc["editor_log"])
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, `r.ScreenPercentage = "100"`) {
		t.Fatalf("summary should carry the output for text-only clients: %q", txt)
	}
}

// TestActorQueryReturnsAnObject: results are objects (plan R4), never a bare array.
func TestActorQueryReturnsAnObject(t *testing.T) {
	r := &scriptedRunner{
		dispatch: func(op string, args map[string]any) any {
			return map[string]any{"world": "editor", "count": 1, "actors": []any{map[string]any{"label": "Cube"}}}
		},
	}
	sc := structuredMap(t, callTool(t, r, "actor_query", map[string]any{"op": "list"}))
	if sc["count"] != float64(1) || sc["world"] != "editor" {
		t.Fatalf("unexpected result: %v", sc)
	}
}

// TestSaveAllFailureKeepsEditorLog: a failed save is an OPERATION_FAILED error that
// still carries the editor's warnings.
func TestSaveAllFailureKeepsEditorLog(t *testing.T) {
	r := &scriptedRunner{
		dispatch: func(op string, args map[string]any) any {
			return map[string]any{"saved": false, "message": "Save reported failures"}
		},
		dispatchExtra: []uexec.OutputEntry{{Type: "Warning", Output: "Package /Game/Broken failed to save"}},
	}
	res := callTool(t, r, "level", map[string]any{"op": "save_all"})
	if !res.IsError {
		t.Fatal("a failed save must be an error")
	}
	e := structuredMap(t, res)["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	if e["code"] != "OPERATION_FAILED" || d == nil || !strings.Contains(fmt.Sprint(d["editor_log"]), "Package /Game/Broken failed to save") {
		t.Fatalf("error lost the editor log: %v", e)
	}
}

// TestPythonEvaluate covers python op=run evaluate=true.
func TestPythonEvaluate(t *testing.T) {
	r := &scriptedRunner{
		raw: func(code string) uexec.CommandResult { return uexec.CommandResult{Success: true, Result: "42"} },
	}
	if sc := structuredMap(t, callTool(t, r, "python", map[string]any{"op": "run", "code": "21*2", "evaluate": true})); sc["value"] != "42" {
		t.Fatalf("python evaluate = %v, want 42", sc["value"])
	}
}

func structuredMap(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	b, _ := json.Marshal(res.StructuredContent)
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("structured content is not an object: %s", b)
	}
	return out
}
