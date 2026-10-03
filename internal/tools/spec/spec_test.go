package spec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callSpec(t *testing.T, s *Spec, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	Register(srv, []*Spec{s}, Options{})
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: s.Name, Arguments: args})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	return res
}

func errCode(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Error.Code
}

type probeIn struct {
	Op        string  `json:"op"`
	Target    string  `json:"target,omitempty"`
	Path      string  `json:"path,omitempty"`
	TimeoutS  float64 `json:"timeout_s,omitempty"`
	ForceFail bool    `json:"force_fail,omitempty"`
}

func probe(h Handler) *Spec {
	schema, _ := jsonschema.For[probeIn](nil)
	return &Spec{Name: "probe", Toolset: Core, Schema: schema, Timeout: 2 * time.Second, Max: 5 * time.Second,
		Ops: []OpSpec{
			{Name: "get", Tier: ReadOnly, Required: []string{"target"}, Rejects: []string{"path"}},
			{Name: "slow", Tier: Mutating, Max: 200 * time.Millisecond},
			{Name: "boom", Tier: Mutating},
		},
		Handler: h}
}

func TestOpDispatchAndParamChecks(t *testing.T) {
	s := probe(func(ctx context.Context, c *Call) (*Result, error) {
		return &Result{Data: map[string]any{"op": c.Op.Name, "target": c.Args["target"]}, Summary: "ok"}, nil
	})
	if res := callSpec(t, s, map[string]any{"op": "get", "target": "A"}); res.IsError {
		t.Fatalf("get failed: %+v", res)
	}
	if c := errCode(t, callSpec(t, s, map[string]any{"op": "get"})); c != "INVALID_ARGUMENT" {
		t.Fatalf("missing required: %s", c)
	}
	if c := errCode(t, callSpec(t, s, map[string]any{"op": "get", "target": "A", "path": "x"})); c != "INVALID_ARGUMENT" {
		t.Fatalf("rejected param: %s", c)
	}
	if c := errCode(t, callSpec(t, s, map[string]any{"op": "nope"})); c != "INVALID_ARGUMENT" {
		t.Fatalf("unknown op: %s", c)
	}
	if c := errCode(t, callSpec(t, s, map[string]any{"op": 7})); c != "INVALID_ARGUMENT" {
		t.Fatalf("schema violation: %s", c)
	}
}

func TestTimeoutCappedAtOpMax(t *testing.T) {
	s := probe(func(ctx context.Context, c *Call) (*Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	res := callSpec(t, s, map[string]any{"op": "slow", "timeout_s": 60})
	if c := errCode(t, res); c != "TIMEOUT" {
		t.Fatalf("code = %s", c)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("timeout_s=60 was not capped at the op Max (took %s)", d)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"outcome":"unknown"`) {
		t.Fatalf("mutating timeout must report outcome unknown: %s", b)
	}
}

func TestPanicIsEnveloped(t *testing.T) {
	s := probe(func(ctx context.Context, c *Call) (*Result, error) { panic("kaboom") })
	if c := errCode(t, callSpec(t, s, map[string]any{"op": "boom"})); c != "INTERNAL" {
		t.Fatalf("code = %s", c)
	}
}

func TestAnnotationsFromWorstOp(t *testing.T) {
	mk := func(tiers ...Tier) *Spec {
		s := &Spec{Name: "x"}
		for _, tr := range tiers {
			s.Ops = append(s.Ops, OpSpec{Tier: tr, Idempotent: true})
		}
		return s
	}
	ro := mk(ReadOnly).Annotations()
	if !ro.ReadOnlyHint || ro.DestructiveHint != nil || ro.OpenWorldHint == nil || !*ro.OpenWorldHint {
		t.Fatalf("readonly annotations wrong: %+v", ro)
	}
	eph := mk(ReadOnly, Ephemeral).Annotations()
	if eph.ReadOnlyHint || eph.DestructiveHint == nil || *eph.DestructiveHint || !eph.IdempotentHint {
		t.Fatalf("ephemeral annotations wrong: %+v", eph)
	}
	mut := mk(Mutating).Annotations()
	if mut.DestructiveHint == nil || *mut.DestructiveHint || mut.IdempotentHint {
		t.Fatalf("mutating annotations wrong: %+v", mut)
	}
	for _, tr := range []Tier{Destructive, Exec} {
		a := mk(Mutating, tr).Annotations()
		if a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Fatalf("%s must be destructiveHint=true: %+v", tr, a)
		}
	}
	off := &Spec{Name: "x", Offline: true, Ops: []OpSpec{{Tier: ReadOnly}}}
	if a := off.Annotations(); a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Fatalf("offline tool must set openWorldHint=false")
	}
}

func TestLintRules(t *testing.T) {
	good := &Spec{Name: "ok_tool", Timeout: time.Second, Max: 10 * time.Second,
		Ops: []OpSpec{{Name: "a", Tier: Mutating, Reaches: []string{"spawn_actor"}}, {Name: "b", Tier: Destructive, Reaches: []string{"delete_actor"}}}}
	if v := Lint([]*Spec{good}); len(v) != 0 {
		t.Fatalf("unexpected violations: %v", v)
	}
	bad := []*Spec{
		{Name: "BadName", Max: time.Second, Ops: []OpSpec{{Tier: ReadOnly}}},
		{Name: "slow", Ops: []OpSpec{{Tier: ReadOnly}}}, // sync with no Max
		{Name: "mixed", Max: time.Second, Ops: []OpSpec{{Name: "r", Tier: ReadOnly}, {Name: "d", Tier: Destructive}}},
		{Name: "under", Max: time.Second, Ops: []OpSpec{{Tier: Mutating, Reaches: []string{"scene_apply"}}}}, // prune escalates
		{Name: "unk", Max: time.Second, Ops: []OpSpec{{Tier: Mutating, Reaches: []string{"no_such_op"}}}},
	}
	v := strings.Join(Lint(bad), "\n")
	for _, want := range []string{"BadName: tool name", "slow op=\"\": sync op", "mixed: destructive/exec op mixed", "under op=\"\": declared mutating but reaches scene_apply", "unclassified python op"} {
		if !strings.Contains(v, want) {
			t.Errorf("lint missed %q in:\n%s", want, v)
		}
	}
	// Rejecting the escalating arg makes a Mutating op that reaches scene_apply legal.
	fixed := &Spec{Name: "fixed", Max: time.Second, Ops: []OpSpec{{Tier: Mutating, Reaches: []string{"scene_apply"}, Rejects: []string{"prune"}}}}
	if v := Lint([]*Spec{fixed}); len(v) != 0 {
		t.Fatalf("rejecting prune should satisfy the lint: %v", v)
	}
}

func BenchmarkServerConstruction(b *testing.B) {
	specs := make([]*Spec, 50)
	for i := range specs {
		s := probe(func(ctx context.Context, c *Call) (*Result, error) { return nil, nil })
		s.Name = "probe_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		specs[i] = s
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
		Register(srv, specs, Options{})
	}
}
