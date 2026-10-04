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
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
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
	World     Toolset = "world"
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
	// Needs lists runtime preconditions beyond a live editor (which every non-Offline
	// tool needs): "pie" (a running play session), "plugin" (the UnrealMCP C++
	// plugin), "navmesh", "project" (a configured project directory), "engine".
	Needs []string
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
	// Job, set by an async op, is the started job: the spec layer returns
	// {job_id, state} immediately, or waits up to the caller's wait_s (streaming MCP
	// progress notifications against that call's progress token).
	Job *jobs.Job
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
	// The title lives on the Tool itself (it would be sent twice otherwise).
	a := &mcp.ToolAnnotations{OpenWorldHint: boolp(!s.Offline)}
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
	// Gate is the approval policy for Destructive/Exec ops; nil = off.
	Gate Gate
	// GateTimeout bounds the total approval wait (default DefaultGateTimeout).
	GateTimeout time.Duration
	// SyncApprovalWait bounds how long a sync call itself waits for approval before
	// returning a pending_approval job (default and maximum MaxSyncApprovalWait).
	SyncApprovalWait time.Duration
}

// Register adds specs to the server.
func Register(srv *mcp.Server, specs []*Spec, o Options) {
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	for _, s := range specs {
		o.Logger = logger
		srv.AddTool(s.Tool(), s.handler(o))
	}
}

func (s *Spec) handler(o Options) mcp.ToolHandler {
	logger, fallback := o.Logger, o.Fallback
	resolved := s.resolvedSchema()
	return func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		mutating := s.Tier() > ReadOnly
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
			if args == nil { // "arguments": null
				args = map[string]any{}
			}
			for k, v := range args { // a null optional means "not given" (schemas are non-nullable)
				if v == nil {
					delete(args, k)
				}
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
		// Any non-ReadOnly op may have taken effect when it times out or is cancelled.
		mutating = op.Tier > ReadOnly
		if e := checkParams(s, op, args); e != nil {
			return envelope.ErrorResult(e), nil
		}
		raw, _ := json.Marshal(args)

		deps, ok := session.From(ctx)
		if !ok {
			deps = fallback
		}
		call := &Call{Request: req, Spec: s, Op: op, Args: args, Raw: raw, Deps: deps}
		if op.Tier.Gated() && o.Gate != nil && o.Gate.Required() {
			return s.gated(ctx, call, o, mutating), nil
		}
		return s.run(ctx, call, mutating), nil
	}
}

// invoke runs the handler under the op's declared timing and retry policy.
func (s *Spec) invoke(ctx context.Context, c *Call) (*Result, error) {
	// A read-only or idempotent op may be transparently re-sent if the editor
	// connection drops after the command was written; anything else reports
	// outcome:"unknown" instead of risking a second execution (plan §2.8).
	if c.Op.Tier == ReadOnly || c.Op.Idempotent {
		ctx = uexec.WithRetryPolicy(ctx, uexec.RetryIdempotent)
	}
	// The spec layer bounds a call only when the spec declares timing. A caller's
	// timeout_s overrides it, capped at the op's Max; specs without declared timing
	// (the v1 adapter) leave timeout_s entirely to their handlers.
	_, timeout, max := s.effective(c.Op)
	if ts, ok := c.Args["timeout_s"].(float64); ok && ts > 0 && max > 0 {
		timeout = min(time.Duration(ts*float64(time.Second)), max)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return s.Handler(ctx, c)
}

// run executes the handler and renders the result.
func (s *Spec) run(ctx context.Context, c *Call, mutating bool) *mcp.CallToolResult {
	out, herr := s.invoke(ctx, c)
	if herr != nil {
		return envelope.ErrorResult(envelope.Classify(herr, mutating))
	}
	switch {
	case out == nil:
		return envelope.Result(nil, "ok")
	case out.Passthrough != nil:
		return out.Passthrough
	case out.Job != nil:
		return awaitJob(ctx, c, out.Job)
	}
	return envelope.Result(out.Data, out.Summary, out.Content...)
}

// detached returns a copy of the call for running after its MCP response was sent
// (an approved pending job): no progress token and no wait_s, so nothing is ever sent
// on the original request's token after its response (MCP progress rule).
func detached(c *Call) *Call {
	cc := *c
	if c.Request != nil {
		req := *c.Request
		if c.Request.Params != nil {
			params := *c.Request.Params
			params.Meta = nil
			req.Params = &params
		}
		cc.Request = &req
	}
	cc.Args = make(map[string]any, len(c.Args))
	for k, v := range c.Args {
		if k != "wait_s" {
			cc.Args[k] = v
		}
	}
	cc.Raw, _ = json.Marshal(cc.Args)
	return &cc
}

// awaitJob returns an async op's job immediately, or after waiting up to the
// caller's wait_s (<= MaxWait), forwarding progress lines as MCP progress
// notifications on this call's progress token. Notifications stop before the
// response is sent, as the protocol requires.
func awaitJob(ctx context.Context, c *Call, j *jobs.Job) *mcp.CallToolResult {
	wait := time.Duration(0)
	if ws, ok := c.Args["wait_s"].(float64); ok && ws > 0 {
		wait = min(time.Duration(ws*float64(time.Second)), MaxWait)
	}
	snap := j.Snapshot()
	if wait > 0 {
		tok := c.Request.Params.GetProgressToken()
		sent := 0
		snap, _ = j.WaitFor(ctx, wait, func(sn jobs.Snapshot) {
			if tok == nil || c.Request.Session == nil {
				return
			}
			for ; sent < len(sn.Progress); sent++ {
				_ = c.Request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: tok, Progress: float64(sent + 1), Message: sn.Progress[sent]})
			}
		})
	}
	view := JobView(snap)
	img, imgErr := jobImage(snap)
	if imgErr != "" {
		view["image_error"] = imgErr
	}
	return envelope.Result(view, fmt.Sprintf("job %s %s", snap.ID, snap.Status), img...)
}

// jobImage attaches the PNG a finished job's result names in "image_path" (e.g. a
// playtest contact sheet) as image content, so it reaches the agent through job
// status/wait like a sync tool's image would.
func jobImage(sn jobs.Snapshot) ([]mcp.Content, string) {
	m, ok := sn.Result.(map[string]any)
	if !ok {
		return nil, ""
	}
	p, _ := m["image_path"].(string)
	if p == "" {
		return nil, ""
	}
	img, err := os.ReadFile(p)
	if err != nil || len(img) == 0 {
		return nil, fmt.Sprintf("could not read %s: %v", p, err)
	}
	return []mcp.Content{&mcp.ImageContent{Data: img, MIMEType: "image/png"}}, ""
}

// JobView is the structured form of a job snapshot in tool results.
func JobView(sn jobs.Snapshot) map[string]any {
	v := map[string]any{"job_id": sn.ID, "state": string(sn.Status)}
	if n := len(sn.Progress); n > 0 {
		v["last_progress"] = sn.Progress[n-1]
		v["progress_lines"] = n
	}
	if sn.Result != nil {
		v["result"] = sn.Result
	}
	if sn.Err != "" {
		v["error"] = sn.Err
	}
	return v
}

// gated parks a Destructive/Exec call for approval (plan 2.1). The approval wait
// runs BEFORE the op's own deadline starts. A sync caller waits at most
// min(GateTimeout, MaxSyncApprovalWait); if still pending, the call returns a
// pending_approval job (executed:false) that runs the op once approved, and is
// cancelled (severing the request) if the session ends first.
func (s *Spec) gated(ctx context.Context, c *Call, o Options, mutating bool) *mcp.CallToolResult {
	total := o.GateTimeout
	if total <= 0 {
		total = DefaultGateTimeout
	}
	sessID := ""
	st, haveState := session.StateFrom(ctx)
	if haveState {
		sessID = st.ID()
	}
	decision, sever := o.Gate.Request(GateRequest{Session: sessID, Tool: s.Name, Op: c.Op.Name, Tier: c.Op.Tier, Args: c.Args})
	syncWait := MaxSyncApprovalWait
	if o.SyncApprovalWait > 0 && o.SyncApprovalWait < syncWait {
		syncWait = o.SyncApprovalWait
	}
	syncWait = min(total, syncWait)
	timer := time.NewTimer(syncWait)
	defer timer.Stop()
	select {
	case d := <-decision:
		if !d.Approved {
			return deniedResult(s, d)
		}
		return s.run(ctx, c, mutating)
	case <-ctx.Done():
		sever()
		return envelope.ErrorResult(envelope.Classify(ctx.Err(), false))
	case <-timer.C:
	}
	if c.Deps.Jobs == nil {
		sever()
		return envelope.ErrorResult(envelope.New(envelope.Precondition, "%s is awaiting approval and no job registry is available", s.Name).
			WithDetail("reason", "approval_timeout"))
	}
	remaining := total - syncWait
	// The pending job is owned by the session only while it AWAITS approval (teardown
	// cancels and severs it then). Once approved it is project work like any other job:
	// ownership is cleared so a session ending mid-run lets it finish (and the lease
	// drains) instead of cancelling a destructive op half-way.
	owner := "approval:" + sessID
	self := make(chan *jobs.Job, 1)
	dc := detached(c)
	j := c.Deps.Jobs.StartOwned(context.Background(), owner, func(jctx context.Context, progress func(string)) (any, error) {
		me := <-self
		progress("awaiting approval")
		t := time.NewTimer(remaining)
		defer t.Stop()
		select {
		case d := <-decision:
			if !d.Approved {
				reason := d.Reason
				if reason == "" {
					reason = "denied"
				}
				return nil, envelope.New(envelope.Precondition, "%s was not approved", s.Name).WithDetail("reason", reason)
			}
			me.SetOwner("") // approved: from here on it is project work, not session-scoped
		case <-t.C:
			sever()
			return nil, envelope.New(envelope.Precondition, "%s approval timed out", s.Name).WithDetail("reason", "approval_timeout")
		case <-jctx.Done():
			sever()
			return nil, jctx.Err()
		}
		if jctx.Err() != nil { // teardown raced the approval: the op never starts
			return nil, jctx.Err()
		}
		progress("approved; running")
		out, err := s.invoke(jctx, dc)
		if err != nil {
			return nil, err
		}
		switch {
		case out == nil:
			return map[string]any{}, nil
		case out.Passthrough != nil:
			if out.Passthrough.IsError {
				return out.Passthrough.StructuredContent, fmt.Errorf("%s failed after approval", s.Name)
			}
			return out.Passthrough.StructuredContent, nil
		case out.Job != nil: // an async op: this job finishes when its job does
			snap := out.Job.Wait()
			if snap.Status != jobs.Succeeded {
				return JobView(snap), fmt.Errorf("%s: %s", s.Name, snap.Err)
			}
			return JobView(snap), nil
		}
		return out.Data, nil
	})
	self <- j
	if haveState {
		st.OnTeardown(func() { c.Deps.Jobs.CancelOwned(owner) })
	}
	return envelope.Result(map[string]any{"state": "pending_approval", "executed": false, "job_id": j.ID},
		fmt.Sprintf("NOT executed — awaiting approval (job %s)", j.ID))
}

func deniedResult(s *Spec, d Decision) *mcp.CallToolResult {
	reason := d.Reason
	if reason == "" {
		reason = "denied"
	}
	return envelope.ErrorResult(envelope.New(envelope.Precondition, "%s was not approved", s.Name).WithDetail("reason", reason))
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
