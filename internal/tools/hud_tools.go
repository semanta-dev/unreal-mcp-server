package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// HUD/UMG authoring + observation tools (HUD_TOOLING_PLAN.md, gated A+ by a senior
// game dev + CTO + UMG/Slate specialist). The primary verb is widget_compose (a
// declarative nested widget tree); fine-grained edits are node sub-operations of it,
// not separate tools. This file is the Go typed surface; the behavior lives in the
// mcp_bridge.py _op_widget_* ops (Python-first authoring) and the MCPAuthoring C++
// Editor module (composite ConstructWidget, structured compile, FWidgetRenderer).
//
// Phasing (see the plan): 0a = flat primitive authoring (Python-only, this file's
// authoring tools); 0b = the MCPAuthoring module (composite + structured compile);
// 1 = the see-loop (widget_view/inspect/read/capture) + interim bind; 2 = operate;
// 3 = MVVM; 4 = polish (widget_make_rt_material).

// --- Phase 0a: authoring ---

type widgetCreateIn struct {
	Dest        string `json:"dest" jsonschema:"destination WidgetBlueprint asset path, e.g. /Game/UI/WBP_HUD"`
	ParentClass string `json:"parent_class,omitempty" jsonschema:"parent UserWidget class; default /Script/UMG.UserWidget. Use the plugin UMCPHUDWidget ONLY for a gameplay-bound HUD (widget_bind_field/track_actor) — it adds a plugin parent-class dependency you MUST bake out before shipping"`
	RootPanel   string `json:"root_panel,omitempty" jsonschema:"root panel widget class; default CanvasPanel (use Overlay for a stacked full-screen menu)"`
}

type widgetComposeIn struct {
	Blueprint string         `json:"blueprint" jsonschema:"the WidgetBlueprint asset path to compose into"`
	Tree      map[string]any `json:"tree" jsonschema:"declarative nested widget node: {name, class, slot?, props?, brush?, font?, is_variable?, children?[]}. class is a friendly name (TextBlock/ProgressBar/Image/Button/CanvasPanel/VerticalBox/Overlay/…) or a /Script or /Game path; a /Game WBP or UserWidget subclass is embedded as ONE opaque composite node (its subtree is not expanded). Every node MUST be named. slot is slot-class-aware (CanvasPanelSlot: anchor_preset|anchors+offsets+alignment+z; BoxSlot: size+padding+align; GridSlot: row/col/span)"`
	Mode      string         `json:"mode,omitempty" jsonschema:"full (default): reconcile the whole tree (convergent — any starting tree yields the spec). patch: update only the given nodes, leaving others"`
	Remove    []string       `json:"remove,omitempty" jsonschema:"node names to remove (snapshotted first; returns a restore_token)"`
	Prune     bool           `json:"prune,omitempty" jsonschema:"remove any existing node absent from the spec (destructive; snapshotted). Default false = additive"`
	Defer     bool           `json:"defer,omitempty" jsonschema:"patch mode: keep the transaction open (mutations apply but do NOT compile) until an explicit widget_compile"`
}

type widgetBlueprintIn struct {
	Blueprint string `json:"blueprint" jsonschema:"the WidgetBlueprint asset path"`
}

type widgetTreeIn struct {
	Blueprint    string `json:"blueprint" jsonschema:"the WidgetBlueprint asset path"`
	Mode         string `json:"mode,omitempty" jsonschema:"get (default): read the current in-memory tree + structural digest. restore: re-apply a snapshot"`
	RestoreToken string `json:"restore_token,omitempty" jsonschema:"restore mode: the token returned by a prior destructive widget_compose"`
}

type widgetDescribeIn struct {
	WidgetClass string `json:"widget_class,omitempty" jsonschema:"a widget class to describe — its editable props, slot class, and (for UMCPHUDWidget) its BindWidget requirements + named-command handlers. Omit for the full authorable palette (panels + leaves)"`
}

// registerHUDTools registers the HUD/UMG authoring + observation surface. The family
// only makes sense with the plugin's authoring/capture modules present; individual
// tools error with a clear PLUGIN/module code at call time when unavailable (the
// affordance manifest carries NeedsAuthoringModule/NeedsCaptureModule gates).
func registerHUDTools(s *mcp.Server, d Deps) {
	b := d.Bridge

	add(s, "widget_create",
		"Create a WidgetBlueprint (UMG HUD/menu) asset. Defaults to a plain UserWidget with a CanvasPanel root. Returns the root name + any required BindWidget slots. Then author the tree with widget_compose.",
		structHandler[widgetCreateIn](b, "widget_create", widgetCreateArgs))

	add(s, "widget_compose",
		"THE primary UMG authoring verb: apply a declarative nested widget tree to a WidgetBlueprint — add/update/reorder/reparent nodes, set slots/props/brush/font, embed composite sub-widgets — then compile once (atomic). Convergent + idempotent: re-composing the same tree yields the same digest. Returns the read-back tree + digest + compile_log (+ a restore_token for destructive changes).",
		structHandler[widgetComposeIn](b, "widget_compose", widgetComposeArgs))

	add(s, "widget_compile",
		"Compile a WidgetBlueprint and return the structured compiler log (errors/warnings) + digest. Terminates a deferred widget_compose transaction. Fails BINDWIDGET_UNSATISFIED with the log if a required bound widget is missing.",
		structHandler[widgetBlueprintIn](b, "widget_compile", func(in widgetBlueprintIn) map[string]any {
			return map[string]any{"blueprint": in.Blueprint}
		}))

	add(s, "widget_tree",
		"Inspect or restore a WidgetBlueprint's widget tree. mode=get returns the canonical tree JSON + structural digest (the layer-1 oracle, no rendering). mode=restore re-applies a snapshot from a restore_token.",
		structHandler[widgetTreeIn](b, "widget_tree", widgetTreeArgs))

	add(s, "widget_describe",
		"Describe the authorable UMG palette, or one widget class: its editable props, its slot class (what layout body its children take), and — for UMCPHUDWidget — its BindWidget requirements + named-command handlers. Plan authoring against this instead of guessing class/prop names.",
		structHandler[widgetDescribeIn](b, "widget_describe", widgetDescribeArgs))
}

// Arg builders (named so they are directly unit-testable — arg->op payload is the
// contract the bridge sends). Optional keys are omitted so Python defaults apply.

func widgetCreateArgs(in widgetCreateIn) map[string]any {
	m := map[string]any{"dest": in.Dest}
	putIf(m, "parent_class", in.ParentClass)
	putIf(m, "root_panel", in.RootPanel)
	return m
}

func widgetComposeArgs(in widgetComposeIn) map[string]any {
	m := map[string]any{"blueprint": in.Blueprint, "tree": in.Tree}
	putIf(m, "mode", in.Mode)
	if len(in.Remove) > 0 {
		m["remove"] = in.Remove
	}
	putBool(m, "prune", in.Prune)
	putBool(m, "defer", in.Defer)
	return m
}

func widgetTreeArgs(in widgetTreeIn) map[string]any {
	m := map[string]any{"blueprint": in.Blueprint}
	putIf(m, "mode", in.Mode)
	putIf(m, "restore_token", in.RestoreToken)
	return m
}

func widgetDescribeArgs(in widgetDescribeIn) map[string]any {
	m := map[string]any{}
	putIf(m, "widget_class", in.WidgetClass)
	return m
}

// putIf sets m[key]=v only when v is non-empty (keeps op payloads minimal so Python
// defaults apply).
func putIf(m map[string]any, key, v string) {
	if v != "" {
		m[key] = v
	}
}

func putBool(m map[string]any, key string, v bool) {
	if v {
		m[key] = true
	}
}
