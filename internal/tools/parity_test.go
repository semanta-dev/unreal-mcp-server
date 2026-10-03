package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
}

func (scriptedRunner) Generation() uint64 { return 1 }

func (r *scriptedRunner) RunCommand(_ context.Context, code string, mode uexec.ExecMode) (uexec.CommandResult, error) {
	if mode == uexec.ModeEval && strings.Contains(code, "_MCP_BRIDGE_VERSION") {
		return uexec.CommandResult{Success: true, Result: strconv.Itoa(r.version)}, nil
	}
	body := strings.TrimPrefix(code, "# mcp\n")
	if mode == uexec.ModeExecFile && strings.Contains(code, "exec(compile(base64.b64decode") {
		r.loaded = true
		r.version = bridge.CompanionVersion()
		return uexec.CommandResult{Success: true}, nil
	}
	if mode == uexec.ModeExecFile && strings.HasPrefix(body, "_mcp_dispatch(") {
		op, args := splitDispatch(body)
		result := r.dispatch(op, args)
		env, _ := json.Marshal(map[string]any{"ok": true, "result": result})
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
	inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(code), "_mcp_dispatch("), ")")
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

// TestExecuteConsoleCommandSurfacesOutput is the P3-gate fix: the tool must
// surface captured editor Warning/Error output, not just a canned message
// (parity with the Python server's format_output).
func TestExecuteConsoleCommandSurfacesOutput(t *testing.T) {
	r := &scriptedRunner{
		raw: func(code string) uexec.CommandResult {
			// The editor logs a warning for the command in addition to the print.
			return uexec.CommandResult{Success: true, Output: []uexec.OutputEntry{
				{Type: "Info", Output: "Executed console command: badcvar"},
				{Type: "Warning", Output: "Command not recognized: badcvar"},
			}}
		},
	}
	res := callTool(t, r, "execute_console_command", map[string]any{"command": "badcvar"})
	got := firstText(t, res)
	if !strings.Contains(got, "Executed console command: badcvar") {
		t.Fatalf("missing echo line: %q", got)
	}
	if !strings.Contains(got, "[Warning] Command not recognized: badcvar") {
		t.Fatalf("captured warning was dropped (parity regression): %q", got)
	}
}

// TestListActorsReturnsArray is the P3-gate fix: list_actors must return a bare
// JSON array (Python contract), not an object.
func TestListActorsReturnsArray(t *testing.T) {
	r := &scriptedRunner{
		dispatch: func(op string, args map[string]any) any {
			return []map[string]any{
				{"label": "Cube", "class": "StaticMeshActor", "location": []float64{0, 0, 100}},
			}
		},
	}
	res := callTool(t, r, "list_actors", map[string]any{})
	got := strings.TrimSpace(firstText(t, res))
	if !strings.HasPrefix(got, "[") {
		t.Fatalf("list_actors must return a top-level JSON array, got: %q", got)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("not a JSON array: %v (%q)", err, got)
	}
	if len(arr) != 1 || arr[0]["label"] != "Cube" {
		t.Fatalf("unexpected array content: %v", arr)
	}
}

// TestTextToolSurfacesCapturedWarnings is the P3-re-review fix: RPC text tools
// must surface editor Warning/Error output captured during the op (like Python's
// format_output), not just the canned message.
func TestTextToolSurfacesCapturedWarnings(t *testing.T) {
	r := &scriptedRunner{
		dispatch: func(op string, args map[string]any) any {
			return map[string]any{"saved": false, "message": "Save reported failures"}
		},
		dispatchExtra: []uexec.OutputEntry{
			{Type: "Warning", Output: "Package /Game/Broken failed to save"},
		},
	}
	res := callTool(t, r, "save_all", map[string]any{})
	got := firstText(t, res)
	if !strings.Contains(got, "Save reported failures") {
		t.Fatalf("missing message: %q", got)
	}
	if !strings.Contains(got, "[Warning] Package /Game/Broken failed to save") {
		t.Fatalf("captured warning dropped (parity regression): %q", got)
	}
}

// TestExecutePythonEvaluate covers the other PROTO tool's evaluate path.
func TestExecutePythonEvaluate(t *testing.T) {
	r := &scriptedRunner{
		raw: func(code string) uexec.CommandResult {
			return uexec.CommandResult{Success: true, Result: "42"}
		},
	}
	res := callTool(t, r, "execute_python", map[string]any{"code": "21*2", "evaluate": true})
	if got := firstText(t, res); got != "42" {
		t.Fatalf("execute_python evaluate = %q, want 42", got)
	}
}
