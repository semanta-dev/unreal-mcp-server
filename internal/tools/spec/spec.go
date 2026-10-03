// Package spec is the single source of truth for the tool surface
// (docs/plans/OVERHAUL_PLAN.md §2.4). Every tool is a *Spec: its name, toolset, ops
// with per-op tier/timeouts, input schema and handler. Registration, MCP
// annotations, the cockpit gate tier, docs and the migration guide all derive
// from it.
//
// Tools are registered through the raw Server.AddTool so the envelope contract
// (package envelope) holds on every path — including input-validation failures,
// which the SDK's typed AddTool would report as unstructured text.
package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
)

// Tier is an op's safety class; a tool's tier is its worst op (§2.1).
type Tier int

const (
	ReadOnly    Tier = iota // no state change
	Ephemeral               // UI/session state or server-owned scratch files only
	Mutating                // changes project/world content; nothing user-authored is lost
	Destructive             // can lose user-authored content
	Exec                    // runs caller-supplied code/commands
)

var tierNames = [...]string{"readonly", "ephemeral", "mutating", "destructive", "exec"}

func (t Tier) String() string {
	if int(t) < len(tierNames) {
		return tierNames[t]
	}
	return fmt.Sprintf("tier(%d)", int(t))
}

// Gated reports whether the cockpit approval gate applies (policy=require).
func (t Tier) Gated() bool { return t >= Destructive }

// Toolset groups tools that are enabled together.
type Toolset string

const (
	Core      Toolset = "core"
	Daemon    Toolset = "daemon"
	Headless  Toolset = "headless"
	Design    Toolset = "design"
	UI        Toolset = "ui"
	Desktop   Toolset = "desktop"
	PolyWorld Toolset = "polyworld"
)

// OpSpec is one operation of a tool. Single-op tools have one OpSpec with Name "".
type OpSpec struct {
	Name       string
	Summary    string
	Tier       Tier
	Idempotent bool
	// Async ops return a job; Timeout bounds a sync call (0 = no bound) and Max caps
	// a caller's timeout_s override. Zero values inherit the Spec defaults.
	Async        bool
	Timeout, Max time.Duration
	Required     []string // params that must be present for this op
	Rejects      []string // params that must be absent for this op
	Reaches      []string // Python ops this op may dispatch (tier lint)
}

// Handler runs one call. A returned error is classified onto the envelope.
type Handler func(ctx context.Context, c *Call) (*Result, error)

// Call is everything a handler sees.
type Call struct {
	Request *mcp.CallToolRequest
	Spec    *Spec
	Op      *OpSpec
	Args    map[string]any  // validated arguments with schema defaults applied
	Raw     json.RawMessage // the same, as JSON (decode into a typed struct with Decode)
	Deps    session.Deps
}

// Decode unmarshals the validated arguments into v.
func (c *Call) Decode(v any) error {
	if err := json.Unmarshal(c.Raw, v); err != nil {
		return envelope.New(envelope.InvalidArgument, "decoding arguments: %v", err)
	}
	return nil
}

// Result is a successful call's payload. Passthrough, when set, is returned as-is
// (the v1 adapter uses it to keep v1 result shapes).
type Result struct {
	Data        any
	Summary     string
	Content     []mcp.Content
	Passthrough *mcp.CallToolResult
}

// Spec is one tool.
type Spec struct {
	Name, Title, Description string
	Toolset                  Toolset
	Ops                      []OpSpec
	Timeout, Max             time.Duration // defaults inherited by ops
	Async                    bool          // default inherited by ops
	Offline                  bool          // → openWorldHint=false
	Replaces                 []string      // v1 tool names this tool supersedes
	Schema                   *jsonschema.Schema
	Handler                  Handler

	resolveOnce sync.Once // the resolved schema is computed once per Spec, not per server
	resolved    *jsonschema.Resolved
}

// resolvedSchema returns the (cached) resolved input schema.
func (s *Spec) resolvedSchema() *jsonschema.Resolved {
	s.resolveOnce.Do(func() {
		schema := s.Schema
		if schema == nil {
			schema = &jsonschema.Schema{Type: "object"}
		}
		r, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if err != nil {
			panic(fmt.Sprintf("spec %s: bad input schema: %v", s.Name, err))
		}
		s.resolved = r
	})
	return s.resolved
}

// Tier is the tool's worst op tier.
func (s *Spec) Tier() Tier {
	t := ReadOnly
	for _, op := range s.Ops {
		if op.Tier > t {
			t = op.Tier
		}
	}
	return t
}

// op resolves the OpSpec for a call's arguments.
func (s *Spec) op(args map[string]any) (*OpSpec, *envelope.Error) {
	if len(s.Ops) == 1 && s.Ops[0].Name == "" {
		return &s.Ops[0], nil
	}
	name, _ := args["op"].(string)
	for i := range s.Ops {
		if s.Ops[i].Name == name {
			return &s.Ops[i], nil
		}
	}
	return nil, envelope.New(envelope.InvalidArgument, "%s: unknown op %q", s.Name, name).
		WithHint("op must be one of: " + strings.Join(s.OpNames(), ", "))
}

// OpNames lists the tool's op names.
func (s *Spec) OpNames() []string {
	var n []string
	for _, op := range s.Ops {
		n = append(n, op.Name)
	}
	return n
}

// effective returns the op's timeout settings with Spec defaults applied.
func (s *Spec) effective(op *OpSpec) (async bool, timeout, max time.Duration) {
	async, timeout, max = op.Async || s.Async, op.Timeout, op.Max
	if timeout == 0 {
		timeout = s.Timeout
	}
	if max == 0 {
		max = s.Max
	}
	if max == 0 {
		max = timeout
	}
	return
}

func boolp(b bool) *bool { return &b }

// Annotations derives the MCP annotations from the worst op (§2.1 table).
func (s *Spec) Annotations() *mcp.ToolAnnotations {
	a := &mcp.ToolAnnotations{Title: s.Title, OpenWorldHint: boolp(!s.Offline)}
	switch t := s.Tier(); t {
	case ReadOnly:
		a.ReadOnlyHint = true
	case Ephemeral, Mutating:
		a.DestructiveHint = boolp(false)
		if t == Ephemeral {
			a.IdempotentHint = true
			for _, op := range s.Ops {
				a.IdempotentHint = a.IdempotentHint && op.Idempotent
			}
		}
	default:
		a.DestructiveHint = boolp(true)
	}
	return a
}

// Tool builds the MCP tool definition (no output schema — see §2.2 R5).
func (s *Spec) Tool() *mcp.Tool {
	schema := s.Schema
	if schema == nil {
		schema = &jsonschema.Schema{Type: "object"}
	}
	return &mcp.Tool{Name: s.Name, Title: s.Title, Description: s.Description,
		InputSchema: schema, Annotations: s.Annotations()}
}

// Options configures Register.
type Options struct {
	Logger *slog.Logger
	// Fallback is used when the request context carries no per-session Deps.
	Fallback session.Deps
}

// Register adds specs to the server.
func Register(srv *mcp.Server, specs []*Spec, o Options) {
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	for _, s := range specs {
		srv.AddTool(s.Tool(), s.handler(logger, o.Fallback))
	}
}

func (s *Spec) handler(logger *slog.Logger, fallback session.Deps) mcp.ToolHandler {
	resolved := s.resolvedSchema()
	return func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		mutating := s.Tier() > Ephemeral
		defer func() {
			if r := recover(); r != nil {
				logger.Error("tool panic", "tool", s.Name, "panic", r, "stack", string(debug.Stack()))
				res, err = envelope.ErrorResult(envelope.New(envelope.Internal, "%s: internal error", s.Name).
					WithDetail("panic", fmt.Sprint(r))), nil
			}
		}()

		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return envelope.ErrorResult(envelope.New(envelope.InvalidArgument, "arguments are not a JSON object: %v", err)), nil
			}
		}
		if err := resolved.ApplyDefaults(&args); err != nil {
			return envelope.ErrorResult(envelope.New(envelope.InvalidArgument, "applying defaults: %v", err)), nil
		}
		if err := resolved.Validate(&args); err != nil {
			return envelope.ErrorResult(envelope.New(envelope.InvalidArgument, "%v", err)), nil
		}
		op, perr := s.op(args)
		if perr != nil {
			return envelope.ErrorResult(perr), nil
		}
		mutating = op.Tier > Ephemeral
		if e := checkParams(s, op, args); e != nil {
			return envelope.ErrorResult(e), nil
		}
		raw, _ := json.Marshal(args)

		_, timeout, max := s.effective(op)
		if ts, ok := args["timeout_s"].(float64); ok && ts > 0 {
			timeout = time.Duration(ts * float64(time.Second))
			if max > 0 && timeout > max {
				timeout = max
			}
		}
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		deps, ok := session.From(ctx)
		if !ok {
			deps = fallback
		}
		out, herr := s.Handler(ctx, &Call{Request: req, Spec: s, Op: op, Args: args, Raw: raw, Deps: deps})
		if herr != nil {
			return envelope.ErrorResult(envelope.Classify(herr, mutating)), nil
		}
		if out == nil {
			return envelope.Result(nil, "ok"), nil
		}
		if out.Passthrough != nil {
			return out.Passthrough, nil
		}
		return envelope.Result(out.Data, out.Summary, out.Content...), nil
	}
}

// checkParams enforces the op's Required/Rejects lists.
func checkParams(s *Spec, op *OpSpec, args map[string]any) *envelope.Error {
	var missing, rejected []string
	for _, k := range op.Required {
		if v, ok := args[k]; !ok || v == nil || v == "" {
			missing = append(missing, k)
		}
	}
	for _, k := range op.Rejects {
		if _, ok := args[k]; ok {
			rejected = append(rejected, k)
		}
	}
	label := s.Name
	if op.Name != "" {
		label += " op=" + op.Name
	}
	switch {
	case len(missing) > 0:
		sort.Strings(missing)
		return envelope.New(envelope.InvalidArgument, "%s requires: %s", label, strings.Join(missing, ", "))
	case len(rejected) > 0:
		sort.Strings(rejected)
		return envelope.New(envelope.InvalidArgument, "%s does not accept: %s", label, strings.Join(rejected, ", "))
	}
	return nil
}
