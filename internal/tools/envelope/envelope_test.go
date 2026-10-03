package envelope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// Every companion code in the plan's mapping table (§2.2 R5) maps as specified.
func TestEditorCodeMap(t *testing.T) {
	want := map[string]Code{
		"ASSET_NOT_FOUND": NotFound, "CLASS_UNRESOLVED": NotFound, "TARGET_NOT_FOUND": NotFound, "NOT_FOUND": NotFound,
		"NOT_IN_PIE": PIENotRunning, "BAD_VALUE": InvalidArgument, "PROPERTY_READONLY": InvalidArgument,
		"PLUGIN_MISSING": Precondition, "NOT_IMPLEMENTED": Unsupported,
		"SPAWN_FAILED": OperationFailed, "IMPORT_FAILED": OperationFailed, "INPUT_FAILED": OperationFailed,
		"CAPTURE_START_FAILED": OperationFailed, "EDITOR_ERROR": OperationFailed, "UNKNOWN_OP": UnknownOp,
		"SOMETHING_NEW": OperationFailed,
	}
	for in, out := range want {
		if got := EditorCode(in); got != out {
			t.Errorf("EditorCode(%s) = %s, want %s", in, got, out)
		}
	}
	closed := map[Code]bool{}
	for _, c := range Codes {
		closed[c] = true
	}
	for _, c := range editorCodes {
		if !closed[c] {
			t.Errorf("mapping targets %s, which is not in the closed set", c)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		mutating bool
		code     Code
		outcome  string
		retry    bool
	}{
		{"op error", &bridge.OpError{Op: "spawn_actor", Code: "CLASS_UNRESOLVED", Message: "x"}, true, NotFound, OutcomeNone, false},
		{"python traceback", &bridge.OpError{Op: "x", Message: "boom", Traceback: "Traceback..."}, true, PythonError, OutcomeNone, false},
		{"no editor", fmt.Errorf("wrap: %w", uexec.ErrEditorNotFound), true, EditorUnreachable, OutcomeNone, true},
		{"timeout mutating", fmt.Errorf("x: %w", uexec.ErrTimeout), true, Timeout, OutcomeUnknown, false},
		{"timeout readonly", context.DeadlineExceeded, false, Timeout, OutcomeNone, true},
		{"cancel mutating", context.Canceled, true, Cancelled, OutcomeUnknown, false},
		{"lost before send (never sent)", uexec.ErrConnectionLost, true, EditorUnreachable, OutcomeNone, true},
		{"lost after send mutating", fmt.Errorf("%w: %w", uexec.ErrOutcomeUnknown, uexec.ErrConnectionLost), true, EditorUnreachable, OutcomeUnknown, false},
		{"stolen in flight", fmt.Errorf("%w: %w", uexec.ErrChannelStolen, uexec.ErrOutcomeUnknown), true, EditorBusy, OutcomeUnknown, false},
		{"stolen fail-fast", uexec.ErrChannelStolen, true, EditorBusy, OutcomeNone, false},
		{"install", fmt.Errorf("%w: hotload: x", bridge.ErrInstall), false, EditorUnreachable, OutcomeNone, true},
		{"install timeout keeps cause", fmt.Errorf("%w: version check: %w", bridge.ErrInstall, uexec.ErrTimeout), false, Timeout, OutcomeNone, true},
		{"envelope passthrough", New(Conflict, "two actors"), true, Conflict, OutcomeNone, false},
		{"unknown", errors.New("weird"), false, Internal, OutcomeNone, false},
	}
	for _, c := range cases {
		e := Classify(c.err, c.mutating)
		if e.Code != c.code || e.Outcome != c.outcome || e.Retryable != c.retry {
			t.Errorf("%s: got %s/%s/retry=%v, want %s/%s/retry=%v", c.name, e.Code, e.Outcome, e.Retryable, c.code, c.outcome, c.retry)
		}
	}
	if d := Classify(&bridge.OpError{Op: "a", Code: "NOT_IN_PIE", Message: "m"}, false).Details; d["editor_code"] != "NOT_IN_PIE" || d["op"] != "a" {
		t.Errorf("details lost: %v", d)
	}
}

func TestErrorResultShape(t *testing.T) {
	res := ErrorResult(New(NotFound, "no actor %q", "A").WithHint("check the label"))
	if !res.IsError || !IsEnveloped(res) {
		t.Fatal("error result must be enveloped")
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; txt != `[NOT_FOUND] no actor "A" — check the label` {
		t.Fatalf("text = %q", txt)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"code", "message", "hint", "retryable", "outcome"} {
		if _, ok := m["error"][k]; !ok {
			t.Errorf("structured error lacks %q: %s", k, b)
		}
	}
}

func TestSafetyNet(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "raw_fail", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			r := &mcp.CallToolResult{}
			r.SetError(errors.New("plain failure"))
			return r, nil
		})
	srv.AddReceivingMiddleware(SafetyNet(func(name string) (string, bool) {
		if name == "design_audit" {
			return "design", true
		}
		return "", false
	}))
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, st, nil)
	defer ss.Close()
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "raw_fail"})
	if err != nil || !res.IsError {
		t.Fatalf("raw_fail: %v %+v", err, res)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if string(b) == "null" || !IsEnveloped(&mcp.CallToolResult{IsError: true, StructuredContent: json.RawMessage(b)}) {
		t.Fatalf("unstructured error not rewritten: %s", b)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "design_audit"})
	if err != nil {
		t.Fatalf("disabled tool should be a tool error, not a protocol error: %v", err)
	}
	b, _ = json.Marshal(res.StructuredContent)
	if !res.IsError || !json.Valid(b) || !strings.Contains(string(b), `"PRECONDITION"`) || !strings.Contains(string(b), "toolset=design") {
		t.Fatalf("disabled-tool hint wrong: %s", b)
	}

	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "truly_unknown"}); err == nil {
		t.Fatal("a truly unknown tool must stay a protocol error")
	}
}

func TestIsEnvelopedIsStrict(t *testing.T) {
	cases := map[string]any{
		"string error":   map[string]any{"error": "text"},
		"value contains": json.RawMessage(`{"result":"an error occurred"}`),
		"unknown code":   map[string]any{"error": map[string]any{"code": "WHATEVER"}},
	}
	for name, sc := range cases {
		if IsEnveloped(&mcp.CallToolResult{IsError: true, StructuredContent: sc}) {
			t.Errorf("%s: wrongly treated as enveloped", name)
		}
	}
	if !IsEnveloped(ErrorResult(New(Conflict, "x"))) {
		t.Error("a real envelope must be recognised")
	}
}
