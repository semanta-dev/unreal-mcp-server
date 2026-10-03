// Package tools registers the parity MCP tools over the official
// modelcontextprotocol/go-sdk, mapping companion-module ops to structured/text/
// image results (GO_REWRITE_PLAN.md §7, §9). Tool NAMES are frozen to match the
// Python server exactly so the A/B parity diff stays meaningful.
package tools

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// Deps are the collaborators the tools need (defined in package session so the
// daemon can build them without importing tools).
type Deps = session.Deps

// RegisterAll adds all tools to the server: the 16 frozen parity tools plus the
// e2e additions (build, PIE, logs, git, lifecycle) when their deps are present.
func RegisterAll(srv *mcp.Server, d Deps) {
	spec.Register(srv, Specs(d), spec.Options{Fallback: d})
}

// registrar collects specs during registration.
type registrar struct{ specs []*spec.Spec }

// Specs returns every tool spec (v1 surface, adapted) for the given deps.
func Specs(d Deps) []*spec.Spec {
	s := &registrar{}
	registerParityTools(s, d.Bridge)
	if d.Jobs != nil {
		registerBuildTools(s, d)
		registerLifecycleTools(s, d)
	}
	registerLogTools(s, d)
	registerGitTools(s, d)
	// v7 additions: reflection, multi-frame capture, high-level design, tighter
	// editor integration, and the playtest orchestrator.
	registerDesignTools(s)
	registerCompanyTools(s, d.Bridge)
	registerDemolishTool(s, d.Bridge)
	registerRoadTool(s, d.Bridge)
	registerPlaytestTools(s, d)
	registerVerificationTools(s, d)
	registerPWTools(s, d)
	registerRobustnessTools(s, d)
	registerHeadlessTools(s, d)
	registerDesktopTools(s, d)
	registerCockpitTools(s, d)
	registerProjectTools(s, d)
	s.specs = append(s.specs, v2Specs()...)
	return s.specs
}

// registerCockpitTools adds cockpit_url, which returns the browser control+observability
// URL the launcher opens once an editor with the MCPCore plugin is reachable.
func registerCockpitTools(s *registrar, d Deps) {
	if d.CockpitURL == nil {
		return
	}
	add(s, "cockpit_url",
		"Return the MCP Cockpit URL — a browser page for live control + observability of the editor (feed, gates, STOP). Open it in a browser. Empty with ready=false until an editor with the UnrealMCP (MCPCore) plugin is running.",
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, map[string]any, error) {
			url, ready := d.CockpitURL()
			out := map[string]any{"url": url, "ready": ready}
			if !ready {
				out["note"] = "cockpit not open yet — start the Unreal editor with the UnrealMCP plugin compiled, then retry"
			}
			return nil, out, nil
		})
}

// registerParityTools adds the 16 frozen parity tools.
func registerParityTools(s *registrar, b *bridge.Bridge) {
	// --- Session & raw exec ---

	// --- Level & actors ---

	// --- Assets ---
	// --- Visual ---
	// --- Play / build ---
	add(s, "live_coding_compile",
		"Trigger a Live Coding compile so C++ changes hot-reload into the running editor. Fire-and-forget; check the editor's Live Coding window for results.",
		textHandler[noArgs](b, "live_coding_compile", func(noArgs) map[string]any { return map[string]any{} }))
}

// add registers one typed tool.
// add registers a v1 tool through the spec layer (envelope, annotations, per-call
// recovery) while keeping its v1 name, schema and result shape.
func add[In, Out any](s *registrar, name, desc string, h mcp.ToolHandlerFor[In, Out]) {
	s.specs = append(s.specs, spec.Typed(name, desc, v1Tier(name), h))
}

// v1Tier classifies a v1 tool by its same-named companion op's worst-case tier
// (interim, until the v2 specs carry explicit per-op tiers). Tools that are not a
// same-named op are listed explicitly in v1ToolTiers; anything else is Mutating.
func v1Tier(name string) spec.Tier {
	if t, ok := v1ToolTiers[name]; ok {
		return t
	}
	if op, ok := spec.PyOps[name]; ok {
		return op.WorstTier()
	}
	return spec.Mutating
}

var v1ToolTiers = map[string]spec.Tier{
	"execute_python": spec.Exec, "execute_console_command": spec.Exec, "headless_run": spec.Exec,
	"pie_input": spec.Exec, "pie_verify": spec.Exec, "playtest_capture": spec.Exec, "scenario_run": spec.Exec,
	"mouse_control": spec.Exec, "key_press": spec.Exec, "type_text": spec.Exec, "focus_window": spec.Exec,
	"git_revert_to": spec.Destructive, "scene_restore": spec.Destructive, "editor_restart": spec.Destructive,
	"import_assets": spec.Destructive, "asset_reimport": spec.Destructive,
	"git_status": spec.ReadOnly, "git_diff": spec.ReadOnly, "git_log": spec.ReadOnly,
	"logs_tail": spec.ReadOnly, "logs_since": spec.ReadOnly, "logs_mark": spec.ReadOnly, "editor_events": spec.ReadOnly,
	"job_status": spec.ReadOnly, "project_map": spec.ReadOnly, "project_list": spec.ReadOnly, "project_attach": spec.Mutating, "project_release": spec.Ephemeral, "perf_parse": spec.ReadOnly, "scenario_list": spec.ReadOnly,
	"image_compare": spec.ReadOnly, "read_capture": spec.ReadOnly, "affordances": spec.ReadOnly, "cockpit_url": spec.ReadOnly,
	"list_windows": spec.ReadOnly, "screen_capture": spec.ReadOnly, "window_capture": spec.ReadOnly,
	"layout_preview": spec.ReadOnly, "playtest_evaluate": spec.ReadOnly, "health_check": spec.ReadOnly,
	"scene_plan": spec.ReadOnly, "scene_digest": spec.ReadOnly, "editor_state": spec.ReadOnly,
}

// structHandler dispatches a companion op and returns its result object as
// structured content (the SDK also mirrors it into JSON text content).
func structHandler[In any](b *bridge.Bridge, op string, args func(In) map[string]any) mcp.ToolHandlerFor[In, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, map[string]any, error) {
		raw, err := bridgeFromCtx(ctx, b).Call(ctx, op, args(in))
		if err != nil {
			return nil, nil, err
		}
		out := map[string]any{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, nil, err
			}
		}
		return nil, out, nil
	}
}

// textHandler dispatches a companion op and returns its message plus any captured
// editor Warning/Error output as text (parity with the Python format_output).
func textHandler[In any](b *bridge.Bridge, op string, args func(In) map[string]any) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		text, err := bridgeFromCtx(ctx, b).CallText(ctx, op, args(in))
		if err != nil {
			return nil, nil, err
		}
		return textResult(text), nil, nil
	}
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
