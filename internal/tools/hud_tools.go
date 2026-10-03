package tools

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
func registerHUDTools(s *registrar, d Deps) {
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

	// --- Phase 1: See + Drive + interim Bind ---

	add(s, "widget_view",
		"Show or hide a WidgetBlueprint in the running (PIE) viewport. show returns a handle to drive/observe it; hide releases it. The viewport keeps a shown widget alive (no manual rooting); keep_alive holds it across a hide/re-show.",
		structHandler[widgetViewIn](b, "widget_view", widgetViewArgs))
	add(s, "widget_set_fields",
		"Push values into a live (shown) widget in one round-trip: set named fields (health Percent, ammo Text, visibility) via the UMCPHUDWidget setters — which force a Slate refresh so the on-screen widget actually updates — and/or invoke named UFUNCTIONs. Partial-failure: per-entry issues. (A UUserWidget is not an actor, so pie_set_property/pie_exec can't reach it — this is the driver.)",
		structHandler[widgetSetFieldsIn](b, "widget_set_fields", widgetSetFieldsArgs))
	add(s, "widget_inspect",
		"Read a LIVE widget's runtime state in the current viewport: per node name/class/visible/text/percent/checked/enabled + on-screen geometry (current viewport only — for multi-resolution geometry use widget_capture). discover:true enumerates on-screen widgets the project itself created (not just bridge-shown ones).",
		structHandler[widgetInspectIn](b, "widget_inspect", widgetInspectArgs))
	add(s, "widget_read",
		"Fast single value read from a live widget (a named sub-widget's field, e.g. ProgressBar.Percent) — the predicate source for pie_verify. Works by name; no is_variable needed.",
		structHandler[widgetReadIn](b, "widget_read", widgetReadArgs))
	add(s, "widget_capture",
		"Render a WidgetBlueprint OFFSCREEN at one or more resolutions (no PIE, no foreground) and return per-resolution geometry and/or pixels — the multi-resolution layout oracle (assert anchored elements stay pinned + inside the SafeZone across 720p/1080p/4K) and the golden-image source for image_compare. Geometry inside an embedded composite is opaque (capture the child WBP standalone).",
		structHandler[widgetCaptureIn](b, "widget_capture", widgetCaptureArgs))
	add(s, "widget_bind_field",
		"Configure a live gameplay data binding on a UMCPHUDWidget: a target widget field (ProgressBar.Percent, TextBlock.Text) pulls from a gameplay source (owning_pawn/pc/player_state/world_actor/ability_system) each tick, with an optional conversion (ratio for health, format_text for ammo, int_to_text, bool_to_visibility). Validates the path at author time (BIND_PATH_UNRESOLVED).",
		structHandler[widgetBindFieldIn](b, "widget_bind_field", widgetBindFieldArgs))
	add(s, "widget_track_actor",
		"Configure a marker widget to track a world actor: each tick it projects the actor's world location to DPI-correct widget space and positions the marker (forced point-anchor + centered pivot). The objective-marker binding — a configured property, no graph authoring.",
		structHandler[widgetTrackActorIn](b, "widget_track_actor", widgetTrackActorArgs))
	add(s, "set_input_mode",
		"Set the player input mode (GameOnly / GameAndUI / UIOnly) and cursor visibility — required to make a menu interactive (UIOnly + cursor) and to hand control back to gameplay (GameOnly).",
		structHandler[setInputModeIn](b, "set_input_mode", setInputModeArgs))
	add(s, "widget_set_focus",
		"Set keyboard/gamepad focus on a named sub-widget of a live widget — pairs with a pie_input Up/Down/Accept nav nudge to verify controller/keyboard-navigable menus (not just mouse clicks).",
		structHandler[widgetSetFocusIn](b, "widget_set_focus", widgetSetFocusArgs))
	add(s, "pie_set_source",
		"Arrange a gameplay source value in the running game so a widget_bind_field pull can be verified: set a dotted actor/component property (HealthComponent.Health) or a GAS attribute base. The test-arrange verb for component + GAS binding sources.",
		structHandler[pieSetSourceIn](b, "pie_set_source", pieSetSourceArgs))

	// --- Phase 2: Wire / Operate ---

	add(s, "set_hud_widget",
		"Register a HUD WidgetBlueprint to auto-appear when the game starts (PIE/editor-gated, cvar-guarded, cooked-safe) — so an authored HUD shows in-game without a project HUD convention. The shipped path is assign_subclass into the project's own HUD property.",
		structHandler[setHudWidgetIn](b, "set_hud_widget", setHudWidgetArgs))
	add(s, "widget_bind_event",
		"Bind a UMCPButton's OnClicked to a named command (Resume/Quit/OpenPanel) — a persisted node property consumed at runtime, no graph authoring. Discover commands via widget_describe. Fails WIDGET_NOT_MCPBUTTON on a plain Button.",
		structHandler[widgetBindEventIn](b, "widget_bind_event", widgetBindEventArgs))
	add(s, "ui_click",
		"Click a live UMG widget by name or viewport coordinate via the Slate pointer path (works on a backgrounded editor; needs a realized painted window — returns SLATE_WINDOW_UNAVAILABLE under headless -nullrhi). Returns the target widget's own state delta; verify downstream effects with a follow-up widget_read.",
		structHandler[uiClickIn](b, "ui_click", uiClickArgs))

	// --- Phase 3: Declarative bind (MVVM, spike-gated) ---

	add(s, "widget_viewmodel_create",
		"Author a UMVVMViewModelBase Blueprint with typed FieldNotify properties (the MVVM data source). Spike-gated: falls back to the UMCPHUDWidget push/pull path if MVVM authoring is intractable in this UE version.",
		structHandler[viewModelCreateIn](b, "widget_viewmodel_create", viewModelCreateArgs))
	add(s, "widget_bind_mvvm",
		"Add MVVM view bindings (widget property <- viewmodel field, with an optional conversion) to a WidgetBlueprint. Fails BIND_TYPE_MISMATCH with the compile log on an unbindable type pair. Spike-gated.",
		structHandler[widgetBindMvvmIn](b, "widget_bind_mvvm", widgetBindMvvmArgs))

	// --- Phase 4: Polish ---

	add(s, "widget_make_rt_material",
		"Build the one UI material a render-target minimap needs: an MD_UI material sampling a SceneCapture render target, a dynamic instance, applied to a minimap Image brush. The last mile that makes a world-render minimap fully buildable.",
		structHandler[makeRtMaterialIn](b, "widget_make_rt_material", makeRtMaterialArgs))
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

// --- Phase 1: See + Drive + interim Bind ---

type widgetViewIn struct {
	Mode      string `json:"mode" jsonschema:"show or hide"`
	Blueprint string `json:"blueprint,omitempty" jsonschema:"show: the WidgetBlueprint to instantiate + add to the viewport"`
	Z         int    `json:"z,omitempty" jsonschema:"show: viewport Z-order"`
	Owner     string `json:"owner,omitempty" jsonschema:"show: owning player-controller actor label (default the first local PC)"`
	Handle    string `json:"handle,omitempty" jsonschema:"hide: the handle returned by a prior show"`
	KeepAlive bool   `json:"keep_alive,omitempty" jsonschema:"show: hold a strong ref (C++ UPROPERTY) so the handle survives a hide/re-show"`
}
type widgetSetFieldsIn struct {
	Handle string           `json:"handle" jsonschema:"a live widget handle from widget_view show"`
	Fields []map[string]any `json:"fields,omitempty" jsonschema:"[{widget_name, field, value}] — pushed via the typed setter matching value's type, with a forced Slate refresh"`
	Calls  []map[string]any `json:"calls,omitempty" jsonschema:"[{ufunction, args}] — invoke named UFUNCTIONs (e.g. RunNamedCommand)"`
}
type widgetInspectIn struct {
	Handle          string `json:"handle,omitempty" jsonschema:"a live widget handle; omit with discover:true to enumerate on-screen widgets"`
	Discover        bool   `json:"discover,omitempty" jsonschema:"enumerate on-screen widgets the project created (not just bridge-shown ones)"`
	IncludeGeometry bool   `json:"include_geometry,omitempty" jsonschema:"include current-viewport on-screen rects (painted widgets only)"`
	IncludeValues   bool   `json:"include_values,omitempty" jsonschema:"include per-node text/percent/checked/enabled"`
}
type widgetReadIn struct {
	Handle     string `json:"handle" jsonschema:"a live widget handle"`
	WidgetName string `json:"widget_name" jsonschema:"the named sub-widget to read"`
	Field      string `json:"field,omitempty" jsonschema:"the field/property to read (e.g. Percent, Text); default a sensible per-class value"`
}
type widgetCaptureIn struct {
	Blueprint    string  `json:"blueprint" jsonschema:"the WidgetBlueprint to render offscreen"`
	Resolutions  [][]int `json:"resolutions,omitempty" jsonschema:"list of [width,height] to render at (e.g. [[1280,720],[1920,1080],[3840,2160]]); default [[1920,1080]]"`
	Geometry     bool    `json:"geometry,omitempty" jsonschema:"return per-widget on-screen rects (works headless via a layout-only pass)"`
	Pixels       bool    `json:"pixels,omitempty" jsonschema:"export a PNG per resolution (needs an active RHI; RHI_UNAVAILABLE under -nullrhi)"`
	WarmupFrames int     `json:"warmup_frames,omitempty" jsonschema:"frames to render before reading (warms fonts/materials); default 3"`
}
type widgetBindFieldIn struct {
	Blueprint    string `json:"blueprint"`
	TargetWidget string `json:"target_widget" jsonschema:"the widget to drive, e.g. HealthBar"`
	TargetField  string `json:"target_field" jsonschema:"its field, e.g. Percent or Text"`
	Source       string `json:"source" jsonschema:"owning_pawn | owning_pc | player_state | world_actor:<label> | ability_system"`
	Path         string `json:"path,omitempty" jsonschema:"dotted source path, e.g. Health or HealthComponent.Health"`
	MaxPath      string `json:"max_path,omitempty" jsonschema:"denominator path for conversion=ratio, e.g. MaxHealth"`
	Attribute    string `json:"attribute,omitempty" jsonschema:"GAS attribute name (source=ability_system)"`
	MaxAttribute string `json:"max_attribute,omitempty" jsonschema:"GAS max attribute for ratio"`
	Conversion   string `json:"conversion,omitempty" jsonschema:"none|ratio|int_to_text|float_to_text|float_to_percent|bool_to_visibility|format_text"`
	Format       string `json:"format,omitempty" jsonschema:"format_text conversion string with {value}/{max}, e.g. '{value} / {max}'"`
}
type widgetTrackActorIn struct {
	MarkerWidget string    `json:"marker_widget" jsonschema:"the marker widget to reposition each tick"`
	Blueprint    string    `json:"blueprint"`
	Target       string    `json:"target" jsonschema:"world-actor label to track, or owning_pawn"`
	WorldOffset  []float64 `json:"world_offset,omitempty" jsonschema:"[x,y,z] world-space offset from the actor origin"`
}
type setInputModeIn struct {
	Mode       string `json:"mode" jsonschema:"GameOnly | GameAndUI | UIOnly"`
	ShowCursor bool   `json:"show_cursor,omitempty"`
}
type widgetSetFocusIn struct {
	Handle     string `json:"handle"`
	WidgetName string `json:"widget_name" jsonschema:"the sub-widget to focus"`
}
type pieSetSourceIn struct {
	Target     string `json:"target" jsonschema:"actor label in the live game world"`
	SourceKind string `json:"source_kind" jsonschema:"property | ability_system"`
	Path       string `json:"path,omitempty" jsonschema:"property: dotted path, e.g. HealthComponent.Health"`
	Attribute  string `json:"attribute,omitempty" jsonschema:"ability_system: the GAS attribute name"`
	Value      any    `json:"value" jsonschema:"the value to set"`
}

// --- Phase 2: Wire / Operate ---

type setHudWidgetIn struct {
	HudWidget string `json:"hud_widget" jsonschema:"the HUD WidgetBlueprint class to auto-spawn"`
	Z         int    `json:"z,omitempty"`
	Owner     string `json:"owner,omitempty"`
}
type widgetBindEventIn struct {
	Blueprint string `json:"blueprint"`
	Widget    string `json:"widget" jsonschema:"a UMCPButton node name"`
	Event     string `json:"event,omitempty" jsonschema:"default OnClicked"`
	Command   string `json:"command" jsonschema:"a RunNamedCommand name discovered via widget_describe (Resume/Quit/OpenPanel)"`
}
type uiClickIn struct {
	Handle     string    `json:"handle,omitempty" jsonschema:"a live widget handle (with widget_name)"`
	WidgetName string    `json:"widget_name,omitempty" jsonschema:"the named widget to click"`
	Coord      []float64 `json:"coord,omitempty" jsonschema:"[x,y] viewport coordinate to click instead of a named widget"`
}

// --- Phase 3: Declarative bind (MVVM) ---

type viewModelCreateIn struct {
	Dest   string           `json:"dest" jsonschema:"destination viewmodel Blueprint path"`
	Fields []map[string]any `json:"fields" jsonschema:"[{name,type}] FieldNotify properties"`
}
type widgetBindMvvmIn struct {
	Blueprint      string           `json:"blueprint"`
	ViewModelClass string           `json:"viewmodel_class"`
	Bindings       []map[string]any `json:"bindings" jsonschema:"[{widget, property, source_field, conversion?, mode?}]"`
}

// --- Phase 4: Polish ---

type makeRtMaterialIn struct {
	Dest         string `json:"dest" jsonschema:"destination UI material asset path"`
	RenderTarget string `json:"render_target" jsonschema:"the SceneCapture TextureRenderTarget2D asset path to sample"`
	ParamName    string `json:"param_name,omitempty" jsonschema:"texture parameter name; default RT"`
	ImageWidget  string `json:"image_widget,omitempty" jsonschema:"a minimap Image node to apply the material to"`
}

func widgetViewArgs(in widgetViewIn) map[string]any {
	m := map[string]any{"mode": in.Mode}
	putIf(m, "blueprint", in.Blueprint)
	putIf(m, "handle", in.Handle)
	putIf(m, "owner", in.Owner)
	if in.Z != 0 {
		m["z"] = in.Z
	}
	putBool(m, "keep_alive", in.KeepAlive)
	return m
}

func widgetSetFieldsArgs(in widgetSetFieldsIn) map[string]any {
	m := map[string]any{"handle": in.Handle}
	if len(in.Fields) > 0 {
		m["fields"] = in.Fields
	}
	if len(in.Calls) > 0 {
		m["calls"] = in.Calls
	}
	return m
}

func widgetInspectArgs(in widgetInspectIn) map[string]any {
	m := map[string]any{}
	putIf(m, "handle", in.Handle)
	putBool(m, "discover", in.Discover)
	putBool(m, "include_geometry", in.IncludeGeometry)
	putBool(m, "include_values", in.IncludeValues)
	return m
}

func widgetReadArgs(in widgetReadIn) map[string]any {
	m := map[string]any{"handle": in.Handle, "widget_name": in.WidgetName}
	putIf(m, "field", in.Field)
	return m
}

func widgetCaptureArgs(in widgetCaptureIn) map[string]any {
	m := map[string]any{"blueprint": in.Blueprint}
	if len(in.Resolutions) > 0 {
		m["resolutions"] = in.Resolutions
	}
	putBool(m, "geometry", in.Geometry)
	putBool(m, "pixels", in.Pixels)
	if in.WarmupFrames > 0 {
		m["warmup_frames"] = in.WarmupFrames
	}
	return m
}

func widgetBindFieldArgs(in widgetBindFieldIn) map[string]any {
	m := map[string]any{"blueprint": in.Blueprint, "target_widget": in.TargetWidget, "target_field": in.TargetField, "source": in.Source}
	putIf(m, "path", in.Path)
	putIf(m, "max_path", in.MaxPath)
	putIf(m, "attribute", in.Attribute)
	putIf(m, "max_attribute", in.MaxAttribute)
	putIf(m, "conversion", in.Conversion)
	putIf(m, "format", in.Format)
	return m
}

func widgetTrackActorArgs(in widgetTrackActorIn) map[string]any {
	m := map[string]any{"blueprint": in.Blueprint, "marker_widget": in.MarkerWidget, "target": in.Target}
	if len(in.WorldOffset) == 3 {
		m["world_offset"] = in.WorldOffset
	}
	return m
}

func setInputModeArgs(in setInputModeIn) map[string]any {
	return map[string]any{"mode": in.Mode, "show_cursor": in.ShowCursor}
}

func widgetSetFocusArgs(in widgetSetFocusIn) map[string]any {
	return map[string]any{"handle": in.Handle, "widget_name": in.WidgetName}
}

func pieSetSourceArgs(in pieSetSourceIn) map[string]any {
	m := map[string]any{"target": in.Target, "source_kind": in.SourceKind, "value": in.Value}
	putIf(m, "path", in.Path)
	putIf(m, "attribute", in.Attribute)
	return m
}

func setHudWidgetArgs(in setHudWidgetIn) map[string]any {
	m := map[string]any{"hud_widget": in.HudWidget}
	putIf(m, "owner", in.Owner)
	if in.Z != 0 {
		m["z"] = in.Z
	}
	return m
}

func widgetBindEventArgs(in widgetBindEventIn) map[string]any {
	m := map[string]any{"blueprint": in.Blueprint, "widget": in.Widget, "command": in.Command}
	putIf(m, "event", in.Event)
	return m
}

func uiClickArgs(in uiClickIn) map[string]any {
	m := map[string]any{}
	putIf(m, "handle", in.Handle)
	putIf(m, "widget_name", in.WidgetName)
	if len(in.Coord) == 2 {
		m["coord"] = in.Coord
	}
	return m
}

func viewModelCreateArgs(in viewModelCreateIn) map[string]any {
	return map[string]any{"dest": in.Dest, "fields": in.Fields}
}

func widgetBindMvvmArgs(in widgetBindMvvmIn) map[string]any {
	return map[string]any{"blueprint": in.Blueprint, "viewmodel_class": in.ViewModelClass, "bindings": in.Bindings}
}

func makeRtMaterialArgs(in makeRtMaterialIn) map[string]any {
	m := map[string]any{"dest": in.Dest, "render_target": in.RenderTarget}
	putIf(m, "param_name", in.ParamName)
	putIf(m, "image_widget", in.ImageWidget)
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
