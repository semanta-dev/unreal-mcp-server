package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/design"
	"github.com/jdziat/unreal-mcp-server/internal/desktop"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

// v2 toolsets / project / design / desktop / polyworld tools (OVERHAUL_PLAN.md §2.3
// rows 36, 37, 39, 40, 42–45).

func extraSpecs(d Deps) []*spec.Spec {
	specs := []*spec.Spec{toolsetsSpec(d), designAuditSpec(), designExploreSpec(), desktopCaptureSpec(),
		desktopInputSpec(), polyworldSpec(), polyworldDemolishSpec()}
	if d.Projects != nil {
		specs = append(specs, projectSpec(d.Projects))
	}
	return specs
}

// --- toolsets --------------------------------------------------------------------

type toolsetsIn struct {
	Op      string `json:"op" jsonschema:"list | enable | disable | describe"`
	Toolset string `json:"toolset,omitempty" jsonschema:"enable/disable: the toolset"`
	Tool    string `json:"tool,omitempty" jsonschema:"describe: one tool (default: every tool of the enabled toolsets, briefly)"`
}

func toolsetsSpec(d Deps) *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "list", Summary: "every toolset, enabled or not, with its tools", Tier: spec.ReadOnly, Idempotent: true},
		{Name: "enable", Summary: "add a toolset's tools to this session", Tier: spec.Ephemeral, Idempotent: true, Required: []string{"toolset"}},
		{Name: "disable", Summary: "remove a toolset's tools from this session", Tier: spec.Ephemeral, Idempotent: true, Required: []string{"toolset"}},
		{Name: "describe", Summary: "what a tool needs and does, per op; the cockpit status", Tier: spec.ReadOnly, Idempotent: true},
	}
	return &spec.Spec{
		Name: "toolsets", Title: "Toolsets and capabilities", Toolset: spec.Core, Offline: true, Timeout: sync8, Max: sync8, Ops: ops,
		Description: "Optional tool groups and what each tool needs.\n- list: toolsets (core always on; daemon, headless, design, ui, desktop, polyworld) and their tools.\n- enable / disable `toolset` for this session.\n- describe `tool`: per op tier, async, needs (editor, pie, plugin, navmesh, project, engine). No `tool`: every enabled tool briefly, the cockpit status, the rollback ladder.",
		Schema: spec.SchemaFor[toolsetsIn](map[string][]any{"op": spec.OpEnum(ops...),
			"toolset": {"core", "daemon", "headless", "design", "ui", "desktop", "polyworld"}}, "op"),
		Replaces: []string{"affordances", "cockpit_url"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			ts, ok := spec.ToolsetsFrom(ctx)
			if !ok {
				return nil, envelope.New(envelope.Unsupported, "this server has no toolset manager")
			}
			var in toolsetsIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			switch c.Op.Name {
			case "list":
				var out []map[string]any
				for _, t := range ts.Known() {
					out = append(out, map[string]any{"toolset": t, "enabled": ts.IsEnabled(t), "tools": ts.Tools(t)})
				}
				return &spec.Result{Data: map[string]any{"toolsets": out}, Summary: fmt.Sprintf("%d toolsets", len(out))}, nil
			case "enable", "disable":
				var names []string
				var err error
				if c.Op.Name == "enable" {
					names, err = ts.Enable(spec.Toolset(in.Toolset))
				} else {
					names, err = ts.Disable(spec.Toolset(in.Toolset))
				}
				if err != nil {
					return nil, envelope.New(envelope.InvalidArgument, "%v", err).WithHint("toolsets op=list shows the toolsets")
				}
				return &spec.Result{Data: map[string]any{"toolset": in.Toolset, c.Op.Name + "d": names, "enabled": ts.Enabled()},
					Summary: fmt.Sprintf("%sd %s (%d tools)", c.Op.Name, in.Toolset, len(names))}, nil
			}
			if in.Tool != "" {
				sp, set, ok := ts.Spec(in.Tool)
				if !ok {
					return nil, envelope.New(envelope.NotFound, "no tool %q", in.Tool).WithHint("toolsets op=list shows every tool")
				}
				return &spec.Result{Data: describeSpec(sp, set, ts.IsEnabled(set), true), Summary: sp.Name + ": " + sp.Title}, nil
			}
			var tools []map[string]any
			for _, set := range ts.Enabled() {
				for _, name := range ts.Tools(set) {
					if sp, _, ok := ts.Spec(name); ok {
						tools = append(tools, describeSpec(sp, set, true, false))
					}
				}
			}
			sort.Slice(tools, func(i, j int) bool { return tools[i]["tool"].(string) < tools[j]["tool"].(string) })
			out := map[string]any{"tools": tools, "rollback_ladder": []string{
				"snapshot_restore: put moved actors back (transforms)",
				"scene_clear: remove a scene's actors, then scene apply again",
				"git_revert: restore files to a git checkpoint (assets, levels, C++)"}}
			cockpit := map[string]any{"available": false}
			if d.CockpitURL != nil {
				url, ready := d.CockpitURL()
				if i := strings.Index(url, "#"); i >= 0 {
					url = url[:i] // the fragment is the human's session token: never hand it to the agent
				}
				cockpit = map[string]any{"available": true, "ready": ready, "url": url,
					"note": "the human opens it from the link the launcher printed (it carries their access token)"}
			}
			out["cockpit"] = cockpit
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%d enabled tools", len(tools))}, nil
		},
	}
}

func describeSpec(sp *spec.Spec, set spec.Toolset, enabled, full bool) map[string]any {
	needs := func(op spec.OpSpec) []string {
		var n []string
		if !sp.Offline {
			n = append(n, "editor")
		}
		return append(n, op.Needs...)
	}
	out := map[string]any{"tool": sp.Name, "toolset": set, "enabled": enabled, "tier": sp.Tier().String()}
	var ops []map[string]any
	for _, op := range sp.Ops {
		o := map[string]any{"tier": op.Tier.String()}
		if op.Name != "" {
			o["op"] = op.Name
		}
		if n := needs(op); len(n) > 0 {
			o["needs"] = n
		}
		if op.Async || sp.Async {
			o["async"] = true
		}
		if full {
			o["summary"] = op.Summary
			if len(op.Required) > 0 {
				o["required"] = op.Required
			}
		}
		ops = append(ops, o)
	}
	out["ops"] = ops
	if full {
		out["description"] = sp.Description
	}
	return out
}

// --- project (daemon) ------------------------------------------------------------

type projectIn struct {
	Op      string `json:"op" jsonschema:"attach | list | release"`
	Project string `json:"project,omitempty" jsonschema:"attach: absolute path of the project DIRECTORY (the folder with the .uproject)"`
}

func projectSpec(pm session.ProjectManager) *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "attach", Summary: "bind this session to the project's editor (reuse or spawn)", Tier: spec.Mutating, Idempotent: true, Required: []string{"project"}},
		{Name: "list", Summary: "every editor the daemon manages", Tier: spec.ReadOnly, Idempotent: true, Rejects: []string{"project"}},
		{Name: "release", Summary: "hand this session's editor back", Tier: spec.Ephemeral, Idempotent: true, Rejects: []string{"project"}},
	}
	return &spec.Spec{
		Name: "project", Title: "Daemon projects", Toolset: spec.Daemon, Offline: true, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Multi-project daemon sessions. CALL op=attach FIRST: it binds this session to the project's editor " +
			"(a warm one is reused, else one is spawned) and routes every later tool call there; it also applies the " +
			"project's .umcp.json toolsets. op=list: every managed editor (yours marked). op=release: hand the editor back " +
			"early (it also happens when the session ends).",
		Schema:   spec.SchemaFor[projectIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"project_attach", "project_list", "project_release"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			sid := ""
			if c.Request != nil && c.Request.Session != nil {
				sid = c.Request.Session.ID()
			}
			switch c.Op.Name {
			case "list":
				l := pm.List(sid)
				return &spec.Result{Data: map[string]any{"instances": l, "count": len(l)}, Summary: fmt.Sprintf("%d editors", len(l))}, nil
			case "release":
				pm.Release(sid)
				return &spec.Result{Data: map[string]any{"released": true}, Summary: "released"}, nil
			}
			project, _ := c.Args["project"].(string)
			// The project's .umcp.json is checked BEFORE leasing an editor: an unreadable
			// or invalid file, or gate_policy "require" (no approval surface here), is
			// refused — fail closed, and no editor is spawned or adopted for it.
			pf, perr := session.LoadProjectFile(project)
			if perr != nil {
				return nil, envelope.New(envelope.Precondition, "the project's .umcp.json is invalid: %v", perr).
					WithHint("fix the file (gate_policy must be \"off\" or \"require\")")
			}
			if pf.GatePolicy == "require" {
				return nil, envelope.New(envelope.Precondition, "%s", spec.NoApprovalSurface).
					WithHint("set gate_policy to \"off\" in the project's .umcp.json, or use a server version with approvals")
			}
			id, err := pm.Attach(ctx, sid, project)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"attached": true, "instance": id, "project": project}
			if ts, ok := spec.ToolsetsFrom(ctx); ok {
				want := []spec.Toolset{spec.Daemon}
				for _, t := range pf.Toolsets {
					want = append(want, spec.Toolset(t))
				}
				if aerr := ts.Apply(want); aerr != nil {
					out["toolsets_error"] = aerr.Error()
				}
				out["toolsets"] = ts.Enabled()
			}
			return &spec.Result{Data: out, Summary: "attached " + project}, nil
		},
	}
}

// --- design_audit / design_explore ----------------------------------------------

// designAudits maps each audit kind to its input decoder + runner. Inputs are
// decoded strictly (unknown fields are an error) from `input`.
var designAudits = map[string]struct {
	shape string
	run   func(raw []byte) (any, error)
}{
	"primitive":   {"{scene: {level, actors: [...]}}", auditOf(func(in primitiveAuditIn) any { return audit.Audit(in.Scene) })},
	"decision":    {"{points: [...]}", auditOf(func(in decisionAuditIn) any { return audit.DecisionAudit(in.Points) })},
	"novelty":     {"{trace, max_dead_stretch?}", auditOf(novelty)},
	"feel":        {"{events: [...], within_ms? (120), max_fx_per_event? (4)}", auditOf(feel)},
	"verb":        {"{burst: [...], envelope}", auditOf(func(in verbResponseIn) any { return audit.VerbResponse(in.Burst, in.Envelope) })},
	"in_motion":   {"{samples: [...]}", auditOf(func(in inMotionAuditIn) any { return audit.InMotionAuditDefault(in.Samples) })},
	"render":      {"{config, timeline? | timeline_path?}", renderAudit},
	"audio":       {"{track: [...], events: [...]} (audio op=capture_stop output)", auditOf(func(in audioAuditIn) any { return audit.AudioAuditDefault(in.Track, in.Events) })},
	"utilization": {"{inventory}", auditOf(func(in assetUtilizationIn) any { return audit.Utilization(in.Inventory) })},
	"luminance":   {"{frame_paths: [...]}", framesAudit(visual.AnalyzeLuminanceFrames)},
	"style":       {"{frame_paths: [...]}", framesAudit(visual.AnalyzeStyleFrames)},
}

func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return envelope.New(envelope.InvalidArgument, "input: %v", err)
	}
	return nil
}

func auditOf[In any](f func(In) any) func([]byte) (any, error) {
	return func(raw []byte) (any, error) {
		var in In
		if err := strictDecode(raw, &in); err != nil {
			return nil, err
		}
		return f(in), nil
	}
}

func novelty(in noveltyAuditIn) any {
	if in.MaxDeadStretch > 0 {
		return audit.NoveltyAudit(in.Trace, in.MaxDeadStretch)
	}
	return audit.NoveltyAuditDefault(in.Trace)
}

func feel(in feelAuditIn) any {
	return audit.FeelAudit(in.Events, orDefault(in.WithinMs, 120), orDefaultInt(in.MaxFXPerEvent, 4))
}

func renderAudit(raw []byte) (any, error) {
	var in renderHealthIn
	if err := strictDecode(raw, &in); err != nil {
		return nil, err
	}
	timeline := in.Timeline
	if in.TimelinePath != "" {
		t, err := audit.LoadTimeline(in.TimelinePath)
		if err != nil {
			return nil, envelope.New(envelope.NotFound, "timeline_path: %v", err)
		}
		timeline = t
	}
	return audit.RenderHealth(in.Config, timeline), nil
}

func framesAudit[R any](f func([]audit.Frame) ([]R, R, error)) func([]byte) (any, error) {
	return func(raw []byte) (any, error) {
		var in struct {
			FramePaths []string `json:"frame_paths"`
		}
		if err := strictDecode(raw, &in); err != nil {
			return nil, err
		}
		if len(in.FramePaths) == 0 {
			return nil, envelope.New(envelope.InvalidArgument, "input.frame_paths is empty")
		}
		per, agg, err := f(framesFromPaths(in.FramePaths))
		if err != nil {
			return nil, envelope.New(envelope.InvalidArgument, "%v", err)
		}
		return map[string]any{"aggregate": agg, "per_frame": per}, nil
	}
}

type designAuditIn struct {
	Kind  string         `json:"kind" jsonschema:"primitive | decision | novelty | feel | verb | in_motion | render | audio | utilization | luminance | style"`
	Input map[string]any `json:"input" jsonschema:"the audit's input object; its shape per kind is listed in the description"`
}

func designAuditSpec() *spec.Spec {
	kinds := make([]string, 0, len(designAudits))
	for k := range designAudits {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var b strings.Builder
	b.WriteString("Deterministic design audits over evidence you already captured (offline, read-only) → a report with " +
		"pass/fail findings. `kind` and its `input`:\n")
	enum := make([]any, len(kinds))
	for i, k := range kinds {
		enum[i] = k
		fmt.Fprintf(&b, "- %s: %s\n", k, designAudits[k].shape)
	}
	return &spec.Spec{
		Name: "design_audit", Title: "Design audits", Toolset: spec.Design, Offline: true, Timeout: sync20, Max: sync28,
		Ops:         []spec.OpSpec{{Tier: spec.ReadOnly, Idempotent: true}},
		Description: strings.TrimSpace(b.String()),
		Schema:      spec.SchemaFor[designAuditIn](map[string][]any{"kind": enum}, "kind", "input"),
		Replaces: []string{"primitive_audit", "decision_audit", "novelty_audit", "feel_audit", "verb_response", "in_motion_audit",
			"render_health", "audio_audit", "asset_utilization", "luminance_report", "style_cohesion"},
		Handler: func(_ context.Context, c *spec.Call) (*spec.Result, error) {
			var in designAuditIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(in.Input)
			report, err := designAudits[in.Kind].run(raw)
			if err != nil {
				return nil, err
			}
			return &spec.Result{Data: map[string]any{"kind": in.Kind, "report": report}, Summary: in.Kind + " audit done"}, nil
		},
	}
}

type designExploreIn struct {
	Op          string            `json:"op" jsonschema:"sweep | explore"`
	Scaffold    *design.Scaffold  `json:"scaffold,omitempty" jsonschema:"sweep: the balance scaffold {waves: [{count, hp, spike}], base_damage, focus, spread, splash_min, expand_growth}"`
	Grid        *design.Grid      `json:"grid,omitempty" jsonschema:"sweep: policy grid {s_steps, e_steps, splash_steps} (default 5 each)"`
	Seed        *design.Genotype  `json:"seed,omitempty" jsonschema:"explore: the starting genotype {policy: {s, splash, e}, scaffold}"`
	Evaluations int               `json:"evaluations,omitempty" jsonschema:"explore: evaluation budget (default 500)"`
	SearchSeed  int64             `json:"search_seed,omitempty" jsonschema:"explore: RNG seed (deterministic)"`
	TopK        int               `json:"top_k,omitempty" jsonschema:"explore: elites returned (default 8)"`
	Donors      []design.Scaffold `json:"donors,omitempty" jsonschema:"explore: scaffolds to cross over from"`
}

func designExploreSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "sweep", Summary: "balance sweep + gate over a policy grid", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"scaffold"},
			Rejects: []string{"seed", "evaluations", "search_seed", "top_k", "donors"}},
		{Name: "explore", Summary: "quality-diversity search from a seed genotype", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"seed"},
			Rejects: []string{"scaffold", "grid"}},
	}
	return &spec.Spec{
		Name: "design_explore", Title: "Balance and design search", Toolset: spec.Design, Offline: true, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Offline design analysis.\n" +
			"- op=sweep: simulate every policy on a grid over a wave `scaffold` → dominant policy?, degenerate optimum?, " +
			"axis liveness, fenced corners, win rate by spike, plus a pass/fail gate.\n" +
			"- op=explore: MAP-Elites style search from `seed` → filled cells and the top_k elite genotypes.",
		Schema:   spec.SchemaFor[designExploreIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"balance_sweep", "design_explore"},
		Handler: func(_ context.Context, c *spec.Call) (*spec.Result, error) {
			var in designExploreIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			if c.Op.Name == "sweep" {
				grid := design.Grid{SSteps: 5, ESteps: 5, SplashSteps: 5}
				if in.Grid != nil && *in.Grid != (design.Grid{}) {
					grid = *in.Grid
				}
				gate := design.Gate(*in.Scaffold, grid)
				return &spec.Result{Data: map[string]any{"sweep": balanceSweepReport(design.Sweep(*in.Scaffold, grid)), "gate": gate},
					Summary: map[bool]string{true: "balance gate passes", false: "balance gate fails"}[gate.Pass]}, nil
			}
			res := design.Search(*in.Seed, design.Config{Evaluations: orDefaultInt(in.Evaluations, 500), Seed: in.SearchSeed,
				TopK: orDefaultInt(in.TopK, 8), Donors: in.Donors})
			return &spec.Result{Data: map[string]any{"filled": res.Filled, "elites": res.Elites},
				Summary: fmt.Sprintf("%d cells filled, %d elites", res.Filled, len(res.Elites))}, nil
		},
	}
}

// --- desktop_capture / desktop_input --------------------------------------------

func desktopErr(err error) error {
	if errors.Is(err, desktop.ErrUnsupported) {
		return envelope.New(envelope.UnsupportedPlatform, "%v", err)
	}
	return envelope.New(envelope.OperationFailed, "%v", err)
}

type desktopCaptureIn struct {
	Op       string `json:"op" jsonschema:"list_windows | screen | window"`
	Filter   string `json:"filter,omitempty" jsonschema:"list_windows: case-insensitive title substring"`
	Monitor  *int   `json:"monitor,omitempty" jsonschema:"screen: monitor index (default: the whole virtual desktop)"`
	Region   []int  `json:"region,omitempty" jsonschema:"screen: [x, y, w, h] in virtual-desktop pixels"`
	Title    string `json:"title,omitempty" jsonschema:"window: title substring (default: auto-detect the Unreal Editor)"`
	Pid      int    `json:"pid,omitempty" jsonschema:"window: a process id"`
	Hwnd     uint64 `json:"hwnd,omitempty" jsonschema:"window: an exact handle from list_windows"`
	Method   string `json:"method,omitempty" jsonschema:"window: print (default; works backgrounded, never steals focus) | screen (blit the visible rectangle) | auto"`
	MaxWidth *int   `json:"max_width,omitempty" jsonschema:"screen/window: downscale to at most this width (default 1600; 0 = full resolution)"`
}

func desktopCaptureSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "list_windows", Summary: "visible top-level windows", Tier: spec.ReadOnly, Idempotent: true},
		{Name: "screen", Summary: "OS screenshot of the display / a monitor / a region", Tier: spec.ReadOnly, Idempotent: true},
		{Name: "window", Summary: "OS screenshot of one window (default: the Unreal Editor)", Tier: spec.ReadOnly, Idempotent: true},
	}
	return &spec.Spec{
		Name: "desktop_capture", Title: "See the real screen", Toolset: spec.Desktop, Offline: true, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "OS-level screenshots of what is REALLY on screen (Windows only; elsewhere UNSUPPORTED_PLATFORM): editor " +
			"menus, panels, modal dialogs, crash popups — which the in-editor screenshot tool never sees.\n" +
			"- op=list_windows: title, pid, hwnd, bounds, foreground/minimized.\n" +
			"- op=screen: the desktop, a monitor or a region.\n" +
			"- op=window: one window, even when backgrounded (method=print).\n" +
			"Results carry the source bounds and a coord_hint for turning an image pixel into a desktop_input mouse coordinate.",
		Schema:   spec.SchemaFor[desktopCaptureIn](map[string][]any{"op": spec.OpEnum(ops...), "method": {"print", "screen", "auto"}}, "op"),
		Replaces: []string{"list_windows", "screen_capture", "window_capture"},
		Handler: func(_ context.Context, c *spec.Call) (*spec.Result, error) {
			var in desktopCaptureIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			var shot *desktop.Shot
			var err error
			switch c.Op.Name {
			case "list_windows":
				wins, err := desktop.ListWindows(in.Filter)
				if err != nil {
					return nil, desktopErr(err)
				}
				return &spec.Result{Data: map[string]any{"count": len(wins), "windows": wins}, Summary: fmt.Sprintf("%d windows", len(wins))}, nil
			case "screen":
				monitor := -1
				if in.Monitor != nil {
					monitor = *in.Monitor
				}
				var region *desktop.Rect
				if len(in.Region) == 4 {
					region = &desktop.Rect{X: in.Region[0], Y: in.Region[1], W: in.Region[2], H: in.Region[3]}
				} else if len(in.Region) != 0 {
					return nil, envelope.New(envelope.InvalidArgument, "region must be [x, y, w, h]")
				}
				shot, err = desktop.CaptureScreen(monitor, region, maxWidthOr(in.MaxWidth))
			default:
				shot, err = desktop.CaptureWindow(desktop.Selector{HWND: uintptr(in.Hwnd), PID: in.Pid, Title: in.Title}, in.Method, false, maxWidthOr(in.MaxWidth))
			}
			if err != nil {
				return nil, desktopErr(err)
			}
			data := shotData(shot)
			return &spec.Result{Data: data, Content: []mcp.Content{&mcp.ImageContent{Data: shot.PNG, MIMEType: "image/png"}},
				Summary: fmt.Sprintf("%dx%d capture", shot.Width, shot.Height)}, nil
		},
	}
}

type desktopInputIn struct {
	Op           string   `json:"op" jsonschema:"focus | mouse | keys | type"`
	Title        string   `json:"title,omitempty" jsonschema:"focus: window title substring (default: the Unreal Editor)"`
	Pid          int      `json:"pid,omitempty" jsonschema:"focus: a process id"`
	Hwnd         uint64   `json:"hwnd,omitempty" jsonschema:"focus: an exact window handle"`
	Action       string   `json:"action,omitempty" jsonschema:"mouse: move | click | double_click | down | up | drag | scroll"`
	X            int      `json:"x,omitempty" jsonschema:"mouse: target X (screen px, or relative to the window param)"`
	Y            int      `json:"y,omitempty" jsonschema:"mouse: target Y"`
	ToX          int      `json:"to_x,omitempty" jsonschema:"mouse drag: end X"`
	ToY          int      `json:"to_y,omitempty" jsonschema:"mouse drag: end Y"`
	Button       string   `json:"button,omitempty" jsonschema:"mouse: left (default) | right | middle"`
	ScrollAmount int      `json:"scroll_amount,omitempty" jsonschema:"mouse scroll: notches, + up/right, - down/left (default 3)"`
	Horizontal   bool     `json:"horizontal,omitempty" jsonschema:"mouse scroll: horizontal"`
	Window       string   `json:"window,omitempty" jsonschema:"mouse: a window title; x/y become relative to its top-left (same origin as desktop_capture op=window)"`
	WindowHwnd   uint64   `json:"window_hwnd,omitempty" jsonschema:"mouse: like window, by handle"`
	Steps        int      `json:"steps,omitempty" jsonschema:"mouse drag: interpolation steps (default 12)"`
	Keys         []string `json:"keys,omitempty" jsonschema:"keys: chords pressed in order, e.g. ['ctrl+s'] or ['ctrl+a', 'delete']"`
	Text         string   `json:"text,omitempty" jsonschema:"type: literal Unicode text for the focused control"`
}

func desktopInputSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "focus", Summary: "bring a window to the foreground", Tier: spec.Exec},
		{Name: "mouse", Summary: "real mouse move/click/drag/scroll", Tier: spec.Exec, Required: []string{"action"}},
		{Name: "keys", Summary: "real keyboard chords", Tier: spec.Exec, Required: []string{"keys"}},
		{Name: "type", Summary: "type literal text", Tier: spec.Exec, Required: []string{"text"}},
	}
	return &spec.Spec{
		Name: "desktop_input", Title: "Drive the real mouse and keyboard", Toolset: spec.Desktop, Offline: true, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "OS-level input to whatever window is in front (Windows only; elsewhere UNSUPPORTED_PLATFORM). It can do " +
			"anything a person at the keyboard can, hence Exec.\n" +
			"- op=focus: bring a window forward (default: the Unreal Editor).\n" +
			"- op=mouse: move|click|double_click|down|up|drag|scroll; with `window` the x/y are window-relative pixels from " +
			"desktop_capture op=window.\n" +
			"- op=keys: chords like ctrl+s, F5, escape.\n- op=type: literal text.",
		Schema: spec.SchemaFor[desktopInputIn](map[string][]any{"op": spec.OpEnum(ops...),
			"action": {"move", "click", "double_click", "down", "up", "drag", "scroll"}, "button": {"left", "right", "middle"}}, "op"),
		Replaces: []string{"focus_window", "mouse_control", "key_press", "type_text"},
		Handler: func(_ context.Context, c *spec.Call) (*spec.Result, error) {
			var in desktopInputIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			switch c.Op.Name {
			case "focus":
				w, err := desktop.FocusWindow(desktop.Selector{HWND: uintptr(in.Hwnd), PID: in.Pid, Title: in.Title})
				if err != nil {
					return nil, desktopErr(err)
				}
				return &spec.Result{Data: map[string]any{"window": w, "foreground": w.Foreground}, Summary: "focused " + w.Title}, nil
			case "mouse":
				req := desktop.MouseReq{Action: in.Action, X: in.X, Y: in.Y, ToX: in.ToX, ToY: in.ToY,
					Button: desktop.MouseButton(strings.ToLower(in.Button)), Amount: in.ScrollAmount, Horiz: in.Horizontal, Steps: in.Steps}
				if in.WindowHwnd != 0 {
					req.Window = desktop.Selector{HWND: uintptr(in.WindowHwnd)}
				} else if in.Window != "" {
					req.Window = desktop.Selector{Title: in.Window}
				}
				pt, err := desktop.Mouse(req)
				if err != nil {
					return nil, desktopErr(err)
				}
				return &spec.Result{Data: map[string]any{"cursor": pt}, Summary: in.Action + " done"}, nil
			case "keys":
				if err := desktop.Keys(in.Keys); err != nil {
					return nil, desktopErr(err)
				}
				return &spec.Result{Data: map[string]any{"pressed": in.Keys}, Summary: "pressed " + strings.Join(in.Keys, " ")}, nil
			}
			if err := desktop.TypeText(in.Text); err != nil {
				return nil, desktopErr(err)
			}
			return &spec.Result{Data: map[string]any{"typed": len([]rune(in.Text))}, Summary: fmt.Sprintf("typed %d characters", len([]rune(in.Text)))}, nil
		},
	}
}

func maxWidthOr(p *int) int {
	if p == nil {
		return 1600
	}
	return *p
}

// shotData is a capture's metadata: source bounds and (when downscaled) the scale,
// so a pixel in the image maps back to the screen/window coordinate desktop_input
// expects.
func shotData(shot *desktop.Shot) map[string]any {
	d := map[string]any{"width": shot.Width, "height": shot.Height, "src_width": shot.SrcWidth, "src_height": shot.SrcHeight,
		"scaled": shot.Scaled, "method": shot.Method,
		"bounds": map[string]int{"x": shot.Bounds.X, "y": shot.Bounds.Y, "w": shot.Bounds.W, "h": shot.Bounds.H}}
	if shot.Title != "" {
		d["title"] = shot.Title
	}
	if shot.PID != 0 {
		d["pid"] = shot.PID
	}
	note := ""
	if shot.Scaled && shot.Width > 0 {
		d["scale"] = float64(shot.SrcWidth) / float64(shot.Width)
		note = "The image is downscaled: multiply an image pixel by scale first. "
	}
	if shot.Title != "" {
		d["coord_hint"] = note + "To click it, call desktop_input op=mouse with window=<title> (or window_hwnd) and that pixel as x/y."
	} else {
		d["coord_hint"] = note + "Add bounds.x and bounds.y to the pixel for the absolute screen coordinate (desktop_input op=mouse without window)."
	}
	return d
}

// --- polyworld -------------------------------------------------------------------

type polyworldIn struct {
	Op       string    `json:"op" jsonschema:"status | build | select | road"`
	Option   *int      `json:"option,omitempty" jsonschema:"build: build-catalog option index"`
	Location []float64 `json:"location,omitempty" jsonschema:"build: world [x, y, z]"`
	Building *int      `json:"building,omitempty" jsonschema:"select: the production building index"`
	Supplier *int      `json:"supplier,omitempty" jsonschema:"select: supplier catalog index (who you buy inputs from)"`
	Market   *int      `json:"market,omitempty" jsonschema:"select: market catalog index (who you sell to)"`
	Start    []int     `json:"start,omitempty" jsonschema:"road: grid cell [x, y]"`
	End      []int     `json:"end,omitempty" jsonschema:"road: grid cell [x, y]"`
}

func polyworldSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "status", Summary: "capital and each building's supplier/market/profit", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"company_status"}, Needs: []string{"pie"}},
		{Name: "build", Summary: "place a factory (spends capital)", Tier: spec.Mutating, Required: []string{"option", "location"}, Reaches: []string{"company_build"}, Needs: []string{"pie"}},
		{Name: "select", Summary: "set a building's supplier and/or market", Tier: spec.Mutating, Idempotent: true, Required: []string{"building"}, Reaches: []string{"company_select"}, Needs: []string{"pie"}},
		{Name: "road", Summary: "drag-build a road (clamped to what you can afford)", Tier: spec.Mutating, Required: []string{"start", "end"}, Reaches: []string{"company_road"}, Needs: []string{"pie"}},
	}
	return &spec.Spec{
		Name: "polyworld", Title: "PolyWorld company game", Toolset: spec.PolyWorld, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Play the PolyWorld Company-MVP in the running game (PIE only; PIE_NOT_RUNNING otherwise, never the editor level).\n- status: capital and each building's supplier, market, last-cycle profit.\n- build: place catalog `option` at `location`.\n- select: a `building`'s supplier and/or market.\n- road: grid cell `start` to `end` (X first, then Y).",
		Schema:      spec.SchemaFor[polyworldIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"company_status", "company_build", "company_select", "company_road"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			args := pick(c.Args, "option", "location", "building", "supplier", "market", "start", "end")
			out, err := v2Op(ctx, c, "company_"+c.Op.Name, args)
			if err != nil {
				return nil, err
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%s: capital %v", c.Op.Name, out["capital"])}, nil
		},
	}
}

type polyworldDemolishIn struct {
	Location []float64 `json:"location" jsonschema:"world [x, y, z]: the building nearest to it is demolished"`
}

func polyworldDemolishSpec() *spec.Spec {
	return &spec.Spec{
		Name: "polyworld_demolish", Title: "Demolish a PolyWorld building", Toolset: spec.PolyWorld, Timeout: sync15, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.Destructive, Required: []string{"location"}, Reaches: []string{"company_demolish"}, Needs: []string{"pie"}}},
		Description: "Bulldoze the building nearest `location` in the running game (PIE only): destroys it, frees its grid " +
			"cells, refunds half its cost → {demolished, name, refund, capital}.",
		Schema:   spec.SchemaFor[polyworldDemolishIn](nil, "location"),
		Replaces: []string{"company_demolish"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			out, err := v2Op(ctx, c, "company_demolish", pick(c.Args, "location"))
			if err != nil {
				return nil, err
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("demolished %v (refund %v)", out["name"], out["refund"])}, nil
		},
	}
}
