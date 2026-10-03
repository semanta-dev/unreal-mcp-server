package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeEditorRunner simulates the editor as seen through the protocol layer: it
// tracks whether the companion module is resident and at what version, and lets
// tests script per-op responses. Counts commands for assertions.
type fakeEditorRunner struct {
	mu           sync.Mutex
	gen          uint64
	moduleVer    int  // editor-side _MCP_BRIDGE_VERSION (0 = absent)
	moduleLoaded bool // whether _mcp_dispatch is defined
	dispatch     func(op string, args map[string]any) (ok bool, result any, errMsg string)

	versionChecks int
	installs      int
}

func newFakeEditor() *fakeEditorRunner {
	return &fakeEditorRunner{
		gen: 1,
		dispatch: func(op string, args map[string]any) (bool, any, string) {
			return true, map[string]any{"echo": op}, ""
		},
	}
}

func (f *fakeEditorRunner) Generation() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func (f *fakeEditorRunner) bumpGen() {
	f.mu.Lock()
	f.gen++
	f.mu.Unlock()
}

func (f *fakeEditorRunner) RunCommand(_ context.Context, code string, mode uexec.ExecMode) (uexec.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Version sentinel check.
	if mode == uexec.ModeEval && strings.Contains(code, "_MCP_BRIDGE_VERSION") {
		f.versionChecks++
		return okResult(strconv.Itoa(f.moduleVer)), nil
	}
	// Module install: the base64 bootstrap that decodes + execs the source.
	if mode == uexec.ModeExecFile && strings.Contains(code, "exec(compile(base64.b64decode") {
		f.installs++
		f.moduleLoaded = true
		f.moduleVer = CompanionVersion()
		return uexec.CommandResult{Success: true}, nil
	}
	// A dispatch call.
	if mode == uexec.ModeExecFile && strings.HasPrefix(strings.TrimPrefix(code, "# mcp\n"), "_mcp_dispatch(") {
		if !f.moduleLoaded {
			return uexec.CommandResult{
				Success: false,
				Result:  "NameError: name '_mcp_dispatch' is not defined",
				Output:  []uexec.OutputEntry{{Type: "Error", Output: "NameError: name '_mcp_dispatch' is not defined"}},
			}, nil
		}
		op, args := parseDispatch(code)
		ok, result, errMsg := f.dispatch(op, args)
		var env map[string]any
		if ok {
			env = map[string]any{"ok": true, "result": result}
		} else {
			env = map[string]any{"ok": false, "error": errMsg, "traceback": "Traceback ..."}
		}
		b, _ := json.Marshal(env)
		return uexec.CommandResult{Success: true, Output: []uexec.OutputEntry{{Type: "Info", Output: "__MCP_JSON__" + string(b)}}}, nil
	}
	return uexec.CommandResult{Success: true}, nil
}

func okResult(result string) uexec.CommandResult {
	return uexec.CommandResult{Success: true, Result: result}
}

// parseDispatch extracts op + decoded args from a `_mcp_dispatch("op", "b64")` call.
func parseDispatch(code string) (string, map[string]any) {
	code = strings.TrimPrefix(code, "# mcp\n")
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

func TestInstallOnceThenSkip(t *testing.T) {
	f := newFakeEditor()
	b := New(f, Options{})
	ctx := context.Background()

	if _, err := b.Call(ctx, "editor_status", map[string]any{}); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	checksAfterFirst := f.versionChecks
	if _, err := b.Call(ctx, "list_actors", map[string]any{}); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if f.installs != 1 {
		t.Fatalf("expected exactly 1 install, got %d", f.installs)
	}
	// The invariant that matters: a cached, same-generation call does NOT touch
	// the editor to re-verify the version.
	if f.versionChecks != checksAfterFirst {
		t.Fatalf("second (cached) call re-checked version: %d -> %d", checksAfterFirst, f.versionChecks)
	}
}

func TestReinstallOnGenerationBump(t *testing.T) {
	f := newFakeEditor()
	b := New(f, Options{})
	ctx := context.Background()

	if _, err := b.Call(ctx, "editor_status", nil); err != nil {
		t.Fatal(err)
	}
	// Simulate editor restart: new connection generation + module gone.
	f.mu.Lock()
	f.moduleLoaded = false
	f.moduleVer = 0
	f.mu.Unlock()
	f.bumpGen()

	checksBefore := f.versionChecks
	if _, err := b.Call(ctx, "editor_status", nil); err != nil {
		t.Fatalf("post-restart call: %v", err)
	}
	if f.installs != 2 {
		t.Fatalf("expected reinstall after generation bump, installs=%d", f.installs)
	}
	if f.versionChecks <= checksBefore {
		t.Fatalf("expected a fresh version check after gen bump (was %d, now %d)", checksBefore, f.versionChecks)
	}
}

func TestReinstallOnNameError(t *testing.T) {
	f := newFakeEditor()
	b := New(f, Options{})
	ctx := context.Background()

	// First call installs and succeeds.
	if _, err := b.Call(ctx, "editor_status", nil); err != nil {
		t.Fatal(err)
	}
	// Editor silently drops the module WITHOUT a generation change (worst case).
	f.mu.Lock()
	f.moduleLoaded = false
	f.moduleVer = 0
	f.mu.Unlock()

	// Bridge still thinks it's installed (same gen), so the dispatch NameErrors;
	// the backstop must reinstall and retry, yielding a correct result.
	res, err := b.Call(ctx, "editor_status", nil)
	if err != nil {
		t.Fatalf("expected recovery via reinstall, got %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res, &got); err != nil || got["echo"] != "editor_status" {
		t.Fatalf("unexpected result after reinstall: %s (%v)", res, err)
	}
	if f.installs != 2 {
		t.Fatalf("expected a reinstall on NameError, installs=%d", f.installs)
	}
}

func TestCallOpError(t *testing.T) {
	f := newFakeEditor()
	f.dispatch = func(op string, args map[string]any) (bool, any, string) {
		return false, nil, "No actor labeled Foo"
	}
	b := New(f, Options{})
	_, err := b.Call(context.Background(), "get_actor", map[string]any{"actor_label": "Foo"})
	var oe *OpError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrOp) || oe.Message != "No actor labeled Foo" {
		t.Fatalf("unexpected op error: %v", err)
	}
}

func TestCallArgsRoundTripBase64(t *testing.T) {
	var seen map[string]any
	f := newFakeEditor()
	f.dispatch = func(op string, args map[string]any) (bool, any, string) {
		seen = args
		return true, map[string]any{"ok": 1}, ""
	}
	b := New(f, Options{})
	// Args containing quotes/backslashes/unicode must survive base64 intact.
	in := map[string]any{"label": `a"b\c`, "path": "/Game/Über", "n": 42.0}
	if _, err := b.Call(context.Background(), "spawn_actor", in); err != nil {
		t.Fatal(err)
	}
	if seen["label"] != `a"b\c` || seen["path"] != "/Game/Über" || seen["n"] != 42.0 {
		t.Fatalf("args corrupted through base64: %#v", seen)
	}
}

func TestCallIntoTyped(t *testing.T) {
	f := newFakeEditor()
	f.dispatch = func(op string, args map[string]any) (bool, any, string) {
		return true, map[string]any{"total": 3, "assets": []string{"/Game/A", "/Game/B"}}, ""
	}
	b := New(f, Options{})
	var out struct {
		Total  int      `json:"total"`
		Assets []string `json:"assets"`
	}
	if err := b.CallInto(context.Background(), "list_assets", map[string]any{}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 3 || len(out.Assets) != 2 {
		t.Fatalf("CallInto typed decode wrong: %+v", out)
	}
}

func TestEval(t *testing.T) {
	f := newFakeEditor()
	b := New(f, Options{})
	got, err := b.Eval(context.Background(), "unreal.SystemLibrary.get_engine_version()")
	if err != nil {
		t.Fatal(err)
	}
	// Our fake returns the version-check pathway only for _MCP_BRIDGE_VERSION;
	// a generic eval returns Success with empty Result here.
	_ = got

	// Failure path.
	f2 := newFakeEditor()
	f2.dispatch = nil
	b2 := New(&evalFailRunner{}, Options{})
	if _, err := b2.Eval(context.Background(), "boom"); err == nil {
		t.Fatal("expected eval failure to error")
	}
}

type evalFailRunner struct{}

func (evalFailRunner) Generation() uint64 { return 1 }
func (evalFailRunner) RunCommand(_ context.Context, _ string, _ uexec.ExecMode) (uexec.CommandResult, error) {
	return uexec.CommandResult{Success: false, Result: "SyntaxError",
		Output: []uexec.OutputEntry{{Type: "Error", Output: "SyntaxError: bad"}}}, nil
}

func TestFormatOutputGolden(t *testing.T) {
	cases := []struct {
		name string
		in   uexec.CommandResult
		want string
	}{
		{"empty", uexec.CommandResult{Success: true}, "(no output)"},
		{"info+result", uexec.CommandResult{Success: true, Result: "42",
			Output: []uexec.OutputEntry{{Type: "Info", Output: "hello\n"}}}, "hello\n42"},
		{"result none suppressed", uexec.CommandResult{Success: true, Result: "None",
			Output: []uexec.OutputEntry{{Type: "Info", Output: "x"}}}, "x"},
		{"error prefix + failed banner", uexec.CommandResult{Success: false,
			Output: []uexec.OutputEntry{{Type: "Error", Output: "boom"}}}, "[Execution failed]\n[Error] boom"},
		{"warning prefix", uexec.CommandResult{Success: true,
			Output: []uexec.OutputEntry{{Type: "Warning", Output: "careful"}}}, "[Warning] careful"},
	}
	for _, c := range cases {
		if got := FormatOutput(c.in); got != c.want {
			t.Errorf("%s: FormatOutput = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractMarkerMidLineAndMissing(t *testing.T) {
	res := uexec.CommandResult{Success: true, Output: []uexec.OutputEntry{
		{Type: "Info", Output: "noise"},
		{Type: "Info", Output: `prefix __MCP_JSON__{"ok":true,"result":7} trailing`},
	}}
	raw, ok := extractMarker(res)
	if !ok {
		t.Fatal("expected marker extraction")
	}
	var env dispatchEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("bad envelope: %s (%v)", raw, err)
	}
	if _, ok := extractMarker(uexec.CommandResult{Success: true,
		Output: []uexec.OutputEntry{{Type: "Info", Output: "no marker here"}}}); ok {
		t.Fatal("expected no marker")
	}
}

func TestSnippetVersionParsed(t *testing.T) {
	if CompanionVersion() < 1 {
		t.Fatalf("snippet version should be >= 1, got %d", CompanionVersion())
	}
	if !strings.Contains(CompanionSource(), "def _mcp_dispatch") {
		t.Fatal("embedded module missing _mcp_dispatch")
	}
}

// sanity: ensure a real *uexec.Session satisfies Runner (compile-time check).
var _ Runner = (*uexec.Session)(nil)

func TestParseDispatchHelper(t *testing.T) {
	// guards the test helper itself against silent breakage.
	op, args := parseDispatch(fmt.Sprintf("# mcp\n_mcp_dispatch(%q, %q)", "spawn_actor",
		base64.StdEncoding.EncodeToString([]byte(`{"x":1}`))))
	if op != "spawn_actor" || args["x"] != 1.0 {
		t.Fatalf("parseDispatch broken: op=%q args=%v", op, args)
	}
}

// fakeNative is a stand-in NativeDispatcher (the framed cockpit backend).
type fakeNative struct {
	mu     sync.Mutex
	calls  int
	lastOp string
	result NativeResult
	err    error
}

func (f *fakeNative) RPCNative(_ context.Context, op string, _ json.RawMessage, _, _ string) (NativeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastOp = op
	return f.result, f.err
}

func TestBridgeNativeBackendRouting(t *testing.T) {
	fake := newFakeEditor()
	opDispatches := 0
	fake.dispatch = func(op string, args map[string]any) (bool, any, string) {
		opDispatches++ // guarded by fake.mu (dispatch is called under it); must stay 0 while native
		return true, map[string]any{"echo": op}, ""
	}
	b := New(fake, Options{})
	native := &fakeNative{result: NativeResult{OK: true, Result: json.RawMessage(`{"native":true}`)}}
	b.SetNative(native)

	// A successful op routes through native, NOT the uexec op-dispatch path.
	res, err := b.Call(context.Background(), "spawn_actor", map[string]any{"class": "StaticMeshActor"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res) != `{"native":true}` {
		t.Fatalf("result = %s, want native", res)
	}
	if native.calls != 1 || native.lastOp != "spawn_actor" {
		t.Fatalf("native calls=%d op=%q", native.calls, native.lastOp)
	}
	if opDispatches != 0 {
		t.Fatalf("op dispatched over uexec %d times; native must be exclusive", opDispatches)
	}

	// A native op-level failure surfaces as an *OpError (not a transport error).
	native.result = NativeResult{OK: false, Error: "no such class", Code: "CLASS_UNRESOLVED"}
	_, err = b.Call(context.Background(), "spawn_actor", map[string]any{})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != "CLASS_UNRESOLVED" {
		t.Fatalf("expected OpError CLASS_UNRESOLVED, got %v", err)
	}

	// Clearing native reverts to the uexec path (one op-dispatch now).
	b.SetNative(nil)
	if _, err := b.Call(context.Background(), "spawn_actor", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if opDispatches != 1 {
		t.Fatalf("after clearing native, expected 1 uexec dispatch, got %d", opDispatches)
	}
}
