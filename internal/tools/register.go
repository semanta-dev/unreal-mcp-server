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
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
)

// Deps are the collaborators the tools need. Bridge is required; the rest enable
// the build/lifecycle/git/log tools (P7-P9).
type Deps struct {
	Bridge     *bridge.Bridge
	Jobs       *jobs.Registry
	ProjectDir string
	EngineDir  string
	// Restart, when non-nil (daemon mode), performs a §3.1 CONTROLLED editor restart
	// that PRESERVES this session's lease: it tears down the leased editor, runs
	// buildStep with no editor up (nil for a plain restart), relaunches with the same
	// instance token, and re-pins the lease. In single-project stdio mode it is nil and
	// tools relaunch the editor directly (a fresh process is fine — no lease to keep).
	Restart func(ctx context.Context, buildStep func(context.Context) error) error
}

// RegisterAll adds all tools to the server: the 16 frozen parity tools plus the
// e2e additions (build, PIE, logs, git, lifecycle) when their deps are present.
func RegisterAll(s *mcp.Server, d Deps) {
	registerParityTools(s, d.Bridge)
	if d.Jobs != nil {
		registerBuildTools(s, d)
		registerLifecycleTools(s, d)
	}
	registerPieTools(s, d.Bridge)
	registerLogTools(s, d)
	registerGitTools(s, d)
	registerAuthoringTools(s, d.Bridge)
	// v7 additions: reflection, multi-frame capture, high-level design, tighter
	// editor integration, and the playtest orchestrator.
	registerReflectTools(s, d.Bridge)
	registerCaptureTools(s, d)
	registerSceneTools(s, d.Bridge)
	registerViewportTools(s, d.Bridge)
	registerPlaytestTools(s, d)
	registerDiscoveryTools(s, d)
	registerAuthoring2Tools(s, d)
	registerVerificationTools(s, d)
	registerPWTools(s, d)
	registerRobustnessTools(s, d)
	registerHeadlessTools(s, d)
	registerControlTools(s, d)
	registerHUDTools(s, d)
}

// registerParityTools adds the 16 frozen parity tools.
func registerParityTools(s *mcp.Server, b *bridge.Bridge) {
	// --- Session & raw exec ---
	add(s, "editor_status",
		"Check whether the Unreal Editor is reachable and report engine version, project, current level, and PIE state.",
		structHandler[noArgs](b, "editor_status", func(noArgs) map[string]any { return map[string]any{} }))

	add(s, "execute_python",
		"Execute arbitrary Python in the Unreal Editor (full `unreal` module access). Set evaluate=true to evaluate a single expression and get its value.",
		executePython(b))

	add(s, "execute_console_command",
		"Run an Unreal console command in the editor (e.g. 'stat fps', 'r.ScreenPercentage 50', 'LiveCoding.Compile'). Returns the command's captured editor log output.",
		executeConsoleCommand(b))

	// --- Level & actors ---
	add(s, "open_level",
		"Open a level in the editor by asset path, e.g. '/Game/Maps/L_Arena'. Unsaved changes in the current level are saved first.",
		textHandler[openLevelIn](b, "open_level", func(in openLevelIn) map[string]any {
			return map[string]any{"level_path": in.LevelPath}
		}))

	add(s, "list_actors",
		"List actors in the current level (label, class, location) as a JSON array. Optional case-insensitive filter matching label or class.",
		listActors(b))

	add(s, "get_actor",
		"Get details for one actor by its editor label: transform, class, components.",
		structHandler[actorLabelIn](b, "get_actor", func(in actorLabelIn) map[string]any {
			return map[string]any{"actor_label": in.ActorLabel}
		}))

	add(s, "spawn_actor",
		"Spawn an actor in the current level from a native class or Blueprint asset path.",
		structHandler[spawnActorIn](b, "spawn_actor", func(in spawnActorIn) map[string]any {
			m := map[string]any{
				"class_path": in.ClassPath, "x": in.X, "y": in.Y,
				"pitch": in.Pitch, "yaw": in.Yaw, "roll": in.Roll,
			}
			if in.Z != nil {
				m["z"] = *in.Z
			}
			if in.Label != "" {
				m["label"] = in.Label
			}
			if in.StaticMeshPath != "" {
				m["static_mesh_path"] = in.StaticMeshPath
			}
			return m
		}))

	add(s, "delete_actor",
		"Delete an actor from the current level by its editor label.",
		textHandler[actorLabelIn](b, "delete_actor", func(in actorLabelIn) map[string]any {
			return map[string]any{"actor_label": in.ActorLabel}
		}))

	add(s, "set_actor_transform",
		"Move/rotate/scale an actor by label. Each of location [x,y,z], rotation_pyr [pitch,yaw,roll], scale [x,y,z] is optional.",
		textHandler[setTransformIn](b, "set_actor_transform", func(in setTransformIn) map[string]any {
			m := map[string]any{"actor_label": in.ActorLabel}
			if in.Location != nil {
				m["location"] = in.Location
			}
			if in.RotationPyr != nil {
				m["rotation_pyr"] = in.RotationPyr
			}
			if in.Scale != nil {
				m["scale"] = in.Scale
			}
			return m
		}))

	// --- Assets ---
	add(s, "list_assets",
		"List content browser assets under a path (e.g. '/Game', '/Game/Maps').",
		structHandler[listAssetsIn](b, "list_assets", func(in listAssetsIn) map[string]any {
			m := map[string]any{}
			if in.Path != "" {
				m["path"] = in.Path
			}
			if in.Recursive != nil {
				m["recursive"] = *in.Recursive
			}
			if in.Limit != nil {
				m["limit"] = *in.Limit
			}
			return m
		}))

	add(s, "import_assets",
		"Import external files (FBX meshes, textures, audio) from disk into the content browser at destination_path.",
		structHandler[importAssetsIn](b, "import_assets", func(in importAssetsIn) map[string]any {
			m := map[string]any{"file_paths": in.FilePaths}
			if in.DestinationPath != "" {
				m["destination_path"] = in.DestinationPath
			}
			return m
		}))

	add(s, "save_all",
		"Save all dirty packages (levels and assets).",
		textHandler[noArgs](b, "save_all", func(noArgs) map[string]any { return map[string]any{} }))

	// --- Visual ---
	add(s, "take_screenshot",
		"Render the scene and return it as a PNG image. Uses a synchronous scene capture so it works even when the editor is backgrounded (editor world only, not during PIE).",
		takeScreenshot(b))

	// --- Play / build ---
	add(s, "start_play",
		"Start a play-in-editor session in the current level. simulate=true runs the world without possessing a player.",
		textHandler[startPlayIn](b, "start_play", func(in startPlayIn) map[string]any {
			return map[string]any{"simulate": in.Simulate}
		}))

	add(s, "stop_play",
		"Stop the current play-in-editor session.",
		textHandler[noArgs](b, "stop_play", func(noArgs) map[string]any { return map[string]any{} }))

	add(s, "live_coding_compile",
		"Trigger a Live Coding compile so C++ changes hot-reload into the running editor. Fire-and-forget; check the editor's Live Coding window for results.",
		textHandler[noArgs](b, "live_coding_compile", func(noArgs) map[string]any { return map[string]any{} }))
}

// add registers one typed tool.
func add[In, Out any](s *mcp.Server, name, desc string, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc}, h)
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
