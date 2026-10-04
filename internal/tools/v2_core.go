package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/crash"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// v2 core tools (docs/plans/OVERHAUL_PLAN.md §2.3 rows 1–7): editor, python,
// console, level, actor_query, actor_edit, actor_call.

const (
	sync8  = 8 * time.Second
	sync15 = 15 * time.Second
	sync20 = 20 * time.Second
	sync25 = 25 * time.Second
	sync28 = 28 * time.Second
)

// v2Bridge returns the call's editor bridge, or a PRECONDITION telling the agent how
// to get one.
func v2Bridge(c *spec.Call) (*bridge.Bridge, error) {
	if c.Deps.Bridge != nil {
		return c.Deps.Bridge, nil
	}
	if c.Deps.Restarting {
		e := envelope.New(envelope.EditorBusy, "the session's editor is restarting").
			WithHint("retry in a few seconds; job op=wait follows the restart")
		e.Retryable = true
		return nil, e
	}
	e := envelope.New(envelope.Precondition, "no editor is bound to this session")
	if c.Deps.Projects != nil {
		return nil, e.WithHint("call project with op=attach and the project directory first")
	}
	return nil, e.WithHint("start the Unreal Editor with the project open (remote execution enabled)")
}

// notWhileRestarting refuses work that must not overlap a controlled restart of the
// session's editor (the restart may itself be building or reverting).
func notWhileRestarting(c *spec.Call) error {
	if !c.Deps.Restarting {
		return nil
	}
	e := envelope.New(envelope.EditorBusy, "the session's editor is restarting")
	e.Hint = "wait for the restart (job op=wait), then retry"
	e.Retryable = true
	return e
}

// projectPath resolves a caller's relative file path against the project directory: a
// stdio server's working directory is not the project, and agents write project-relative
// paths (found by the tool-selection eval).
func projectPath(c *spec.Call, p string) string {
	if p == "" || filepath.IsAbs(p) || c.Deps.ProjectDir == "" {
		return p
	}
	return filepath.Join(c.Deps.ProjectDir, filepath.FromSlash(p))
}

// v2Op dispatches a companion op and decodes its result object. Warning/Error lines
// the editor logged during the op are returned as "editor_log" (also on errors).
func v2Op(ctx context.Context, c *spec.Call, op string, args map[string]any) (map[string]any, error) {
	out, _, err := v2OpOutput(ctx, c, op, args)
	return out, err
}

// v2OpOutput is v2Op plus every line the editor printed while the op ran.
func v2OpOutput(ctx context.Context, c *spec.Call, op string, args map[string]any) (map[string]any, []uexec.OutputEntry, error) {
	b, err := v2Bridge(c)
	if err != nil {
		return nil, nil, err
	}
	raw, output, err := b.CallLog(ctx, op, args)
	log := bridge.Problems(output)
	if err != nil {
		if len(log) > 0 {
			e := envelope.Classify(err, c.Op.Tier > spec.ReadOnly)
			return nil, output, e.WithDetail("editor_log", log)
		}
		return nil, output, err
	}
	out := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, output, fmt.Errorf("decode %s result: %w", op, err)
		}
	}
	if len(log) > 0 {
		out["editor_log"] = log
	}
	return out, output, nil
}

// withLog attaches a result's editor_log to an error raised after the call.
func withLog(e *envelope.Error, out map[string]any) *envelope.Error {
	if log, ok := out["editor_log"]; ok {
		e.WithDetail("editor_log", log)
	}
	return e
}

// pick copies the named arguments that are present.
func pick(args map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			out[k] = v
		}
	}
	return out
}

func coreSpecs() []*spec.Spec {
	return []*spec.Spec{editorSpec(), pythonSpec(), consoleSpec(), levelSpec(),
		actorQuerySpec(), actorEditSpec(), actorCallSpec(), undoSpec()}
}

// --- editor ----------------------------------------------------------------------

type editorIn struct {
	Op            string `json:"op" jsonschema:"status | ping | health"`
	ExpectVersion int    `json:"expect_version,omitempty" jsonschema:"health: fail if the companion version is below this (catches a stale module)"`
	Since         string `json:"since,omitempty" jsonschema:"health: RFC3339; count crashes since (default 10 min ago)"`
	ExpectPlugin  int    `json:"expect_plugin,omitempty" jsonschema:"health: fail if the UnrealMCP plugin API is below this"`
}

func editorSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "status", Summary: "engine, project, level, PIE state, viewport camera, selection, actor count", Tier: spec.ReadOnly, Timeout: sync15, Reaches: []string{"editor_status"}},
		{Name: "ping", Summary: "fast liveness probe (companion version, PIE state)", Tier: spec.ReadOnly, Timeout: sync8, Reaches: []string{"editor_ping"}},
		{Name: "health", Summary: "ping + expected companion version + no crash since a time (use after a rebuild)", Tier: spec.ReadOnly, Timeout: sync15, Reaches: []string{"editor_ping"}},
	}
	return &spec.Spec{
		Name: "editor", Title: "Editor status", Toolset: spec.Core, Max: sync28, Ops: ops,
		Description: "Inspect the connected Unreal Editor.\n" +
			"- op=status: engine, project, level, is_in_pie, camera, selection, actor count.\n" +
			"- op=ping: cheap liveness probe.\n" +
			"- op=health: ping, then check expect_version, expect_plugin and for crashes since `since` → {healthy, plugin_api, problems[]}.",
		Schema:   spec.SchemaFor[editorIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"editor_status", "editor_state", "editor_ping", "health_check"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in editorIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			switch c.Op.Name {
			case "status":
				out, err := v2Op(ctx, c, "editor_status", nil)
				return &spec.Result{Data: out, Summary: fmt.Sprintf("level %v, PIE %v", out["current_level"], out["is_in_pie"])}, err
			case "ping":
				out, err := v2Op(ctx, c, "editor_ping", nil)
				return &spec.Result{Data: out, Summary: "editor is responding"}, err
			}
			return editorHealth(ctx, c, in)
		},
	}
}

func editorHealth(ctx context.Context, c *spec.Call, in editorIn) (*spec.Result, error) {
	res := map[string]any{"healthy": true}
	var problems []string
	ping, err := v2Op(ctx, c, "editor_ping", nil)
	if err != nil {
		e := envelope.Classify(err, false)
		res["healthy"] = false
		res["problems"] = []string{"editor unreachable: " + e.Message}
		return &spec.Result{Data: res, Summary: "unhealthy: editor unreachable"}, nil
	}
	ver, _ := ping["version"].(float64)
	papi, _ := ping["plugin_api"].(float64)
	res["version"], res["pie"], res["plugin_api"] = int(ver), ping["pie"], int(papi)
	if in.ExpectVersion > 0 && int(ver) < in.ExpectVersion {
		problems = append(problems, "companion version "+strconv.Itoa(int(ver))+" < expected "+strconv.Itoa(in.ExpectVersion)+" (stale module: rebuild/redeploy)")
	}
	if in.ExpectPlugin > 0 && int(papi) < in.ExpectPlugin {
		problems = append(problems, "UnrealMCP plugin API "+strconv.Itoa(int(papi))+" < expected "+strconv.Itoa(in.ExpectPlugin)+" (copy plugin/UnrealMCP into the project and build strategy=ubt)")
	}
	since := time.Now().Add(-10 * time.Minute)
	if in.Since != "" {
		t, perr := time.Parse(time.RFC3339, in.Since)
		if perr != nil {
			return nil, envelope.New(envelope.InvalidArgument, "since must be RFC3339: %v", perr)
		}
		since = t
	}
	if c.Deps.ProjectDir != "" {
		if rep, _ := crash.FromCrashDir(c.Deps.ProjectDir, since); rep != nil {
			problems = append(problems, "crash detected: "+rep.Summary)
			res["crash"] = rep
		}
	}
	summary := "healthy"
	if len(problems) > 0 {
		res["healthy"], res["problems"] = false, problems
		summary = "unhealthy: " + problems[0]
	}
	return &spec.Result{Data: res, Summary: summary}, nil
}

// --- python ----------------------------------------------------------------------

type pythonIn struct {
	Op         string `json:"op" jsonschema:"run | recipe"`
	Code       string `json:"code,omitempty" jsonschema:"run: Python source executed in the editor (full unreal module access)"`
	Evaluate   bool   `json:"evaluate,omitempty" jsonschema:"run: evaluate a single expression and return its value"`
	Path       string `json:"path,omitempty" jsonschema:"recipe: path to an idempotent level-recipe .py file"`
	CleanSlate bool   `json:"clean_slate,omitempty" jsonschema:"recipe: FIRST DESTROY every actor except WorldSettings (default false)"`
	Save       *bool  `json:"save,omitempty" jsonschema:"recipe: save dirty packages afterwards (default true)"`
}

func pythonSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "run", Summary: "execute Python (or evaluate one expression)", Tier: spec.Exec, Timeout: sync25,
			Required: []string{"code"}, Rejects: []string{"path", "clean_slate", "save"}, Reaches: []string{"note_edit"}},
		{Name: "recipe", Summary: "run a level-recipe file (clean_slate=true wipes the level first)", Tier: spec.Exec, Timeout: sync25,
			Required: []string{"path"}, Rejects: []string{"code", "evaluate"}, Reaches: []string{"apply_level_recipe"}},
	}
	return &spec.Spec{
		Name: "python", Title: "Run Python in the editor", Toolset: spec.Core, Max: sync28, Ops: ops,
		Description: "Run Python in the editor (arbitrary code). Check for a dedicated tool first: game data (tables, data assets, curves, input mappings) is toolset data; the running game's API is toolset game.\n- run: `code` → captured output; evaluate=true → one expression's value.\n- recipe: run the level-recipe file `path`; clean_slate=true FIRST destroys every actor except WorldSettings; save defaults true.",
		Schema:      spec.SchemaFor[pythonIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"execute_python", "apply_level_recipe"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in pythonIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			if c.Op.Name == "recipe" {
				args := map[string]any{"script_path": in.Path, "clean_slate": in.CleanSlate}
				if in.Save != nil {
					args["save"] = *in.Save
				}
				out, err := v2Op(ctx, c, "apply_level_recipe", args)
				return &spec.Result{Data: out, Summary: fmt.Sprintf("recipe applied: %v → %v actors", out["actors_before"], out["actors_after"])}, err
			}
			b, err := v2Bridge(c)
			if err != nil {
				return nil, err
			}
			// Python may change the level with no undo step: the companion's undo journal
			// must know, so a later undo does not revert an older edit underneath it.
			noteEdit := func() { _, _ = v2Op(context.WithoutCancel(ctx), c, "note_edit", map[string]any{"op": "python"}) }
			if in.Evaluate {
				v, err := b.Eval(ctx, in.Code)
				if err != nil {
					return nil, pythonFailure(err)
				}
				noteEdit()
				return &spec.Result{Data: map[string]any{"value": v, "undoable": false}, Summary: v}, nil
			}
			res, err := b.RunPython(ctx, in.Code, uexec.ModeExecFile)
			noteEdit() // even a failed script may have changed things before it raised
			if err != nil {
				return nil, err
			}
			out := bridge.FormatOutput(res)
			if !res.Success {
				return nil, envelope.New(envelope.PythonError, "the script raised an error").WithDetail("output", out)
			}
			return &spec.Result{Data: map[string]any{"output": out, "undoable": false}, Summary: out}, nil
		},
	}
}

func pythonFailure(err error) error {
	if errors.Is(err, uexec.ErrCommandFailed) {
		return envelope.New(envelope.PythonError, "%v", err)
	}
	return err
}

// --- console ---------------------------------------------------------------------

type consoleIn struct {
	Command string `json:"command" jsonschema:"console command, e.g. 'stat fps', 'r.ScreenPercentage 50', 'slomo 4'"`
	World   string `json:"world,omitempty" jsonschema:"editor (default: the editor viewport context) | pie (the player controller)"`
}

func consoleSpec() *spec.Spec {
	return &spec.Spec{
		Name: "console", Title: "Console command", Toolset: spec.Core, Timeout: sync15, Max: sync28,
		Ops:         []spec.OpSpec{{Tier: spec.Exec, Reaches: []string{"console"}}},
		Description: "Run an Unreal console command. world=editor (default): editor context (viewport/show/stat); world=pie: the player controller (cheats like slomo). Returns every printed line as `output` and warnings/errors as editor_log.",
		Schema:      spec.SchemaFor[consoleIn](map[string][]any{"world": {"editor", "pie"}}, "command"),
		Replaces:    []string{"execute_console_command"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in consoleIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			if in.World == "" {
				in.World = "editor"
			}
			out, output, err := v2OpOutput(ctx, c, "console", map[string]any{"command": in.Command, "world": in.World})
			if err != nil {
				return nil, err
			}
			lines := []string{}
			for _, e := range output {
				lines = append(lines, strings.TrimRight(e.Output, "\r\n"))
			}
			out["output"] = lines
			out["undoable"] = false // a console command makes no undo step
			summary := fmt.Sprintf("ran %q in %s", in.Command, in.World)
			if len(lines) > 0 {
				summary += ":\n" + strings.Join(lines, "\n")
			}
			return &spec.Result{Data: out, Summary: summary}, nil
		},
	}
}

// --- level -----------------------------------------------------------------------

type levelIn struct {
	Op    string `json:"op" jsonschema:"open | save_all | set_world_gamemode"`
	Level string `json:"level,omitempty" jsonschema:"level path, e.g. /Game/Maps/L_Arena (revert: default the open one)"`
	Class string `json:"class,omitempty" jsonschema:"set_world_gamemode: the GameMode class"`
	Save  *bool  `json:"save,omitempty" jsonschema:"open: false = never save"`
}

func levelSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "open", Summary: "save dirty packages (save=false: refused instead), then load the level", Tier: spec.Mutating, Timeout: sync25, Required: []string{"level"}, Reaches: []string{"open_level"}},
		{Name: "save_all", Summary: "save every dirty package", Tier: spec.Mutating, Timeout: sync25, Rejects: []string{"save"}, Reaches: []string{"save_all"}},
		{Name: "revert", Summary: "reload the level from disk, DROPPING its unsaved changes", Tier: spec.Destructive, Timeout: sync25, Rejects: []string{"class", "save"}, Reaches: []string{"level_revert"}},
		{Name: "set_world_gamemode", Summary: "set this level's WorldSettings GameMode override (saves)", Tier: spec.Mutating, Timeout: sync15, Required: []string{"class"}, Rejects: []string{"save"}, Reaches: []string{"set_world_gamemode"}},
	}
	return &spec.Spec{
		Name: "level", Title: "Level", Toolset: spec.Core, Max: sync28, Ops: ops,
		Description: "Open and save levels.\n" +
			"- open: saves dirty packages first (save=false: refused if the level has unsaved changes), then loads `level`.\n" +
			"- revert: reloads the level from disk, dropping its unsaved changes.\n" +
			"- save_all.\n" +
			"- set_world_gamemode: the open level's GameMode override = `class`; saves.",
		Schema:   spec.SchemaFor[levelIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"open_level", "save_all", "set_world_gamemode"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in levelIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			switch c.Op.Name {
			case "open":
				args := map[string]any{"level_path": in.Level}
				if in.Save != nil {
					args["save"] = *in.Save
				}
				out, err := v2Op(ctx, c, "open_level", args)
				if err == nil && out["loaded"] == false {
					return nil, withLog(envelope.New(envelope.OperationFailed, "could not load level %s", in.Level), out)
				}
				return &spec.Result{Data: out, Summary: "opened " + in.Level}, err
			case "revert":
				args := map[string]any{}
				if in.Level != "" {
					args["level_path"] = in.Level
				}
				out, err := v2Op(ctx, c, "level_revert", args)
				if err != nil {
					return nil, err
				}
				return &spec.Result{Data: out, Summary: fmt.Sprintf("reverted %v", out["reverted"])}, nil
			case "save_all":
				out, err := v2Op(ctx, c, "save_all", nil)
				if err == nil && out["saved"] == false {
					return nil, withLog(envelope.New(envelope.OperationFailed, "saving reported failures (read-only or checked-out packages?)"), out)
				}
				return &spec.Result{Data: out, Summary: "saved all dirty packages"}, err
			}
			out, err := v2Op(ctx, c, "set_world_gamemode", map[string]any{"class_path": in.Class})
			if err != nil {
				return nil, err
			}
			out["undoable"] = false
			return &spec.Result{Data: out, Summary: "world GameMode set to " + in.Class}, nil
		},
	}
}

// --- actor_query -----------------------------------------------------------------

type actorQueryIn struct {
	Op         string         `json:"op" jsonschema:"list | get | find"`
	World      string         `json:"world,omitempty" jsonschema:"editor (default) | pie | auto (PIE when running)"`
	Actor      string         `json:"actor,omitempty" jsonschema:"get: a label, object path or @ref as for actor_call"`
	Filter     string         `json:"filter,omitempty" jsonschema:"list/find: case-insensitive substring of label or class"`
	Class      string         `json:"class,omitempty" jsonschema:"list/find: only this class and its subclasses"`
	Where      map[string]any `json:"where,omitempty" jsonschema:"find: property → value equality filter on reflected properties"`
	Properties []string       `json:"properties,omitempty" jsonschema:"list/find: reflected properties to include per actor"`
	Limit      int            `json:"limit,omitempty" jsonschema:"max actors returned (default 200; count is always the full total)"`
}

func actorQuerySpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "list", Summary: "actors with label, path, class, location", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"actor_query"}},
		{Name: "get", Summary: "one actor in detail (transform, tags, components)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"actor"}, Reaches: []string{"actor_query"}},
		{Name: "find", Summary: "actors matching class/where, with chosen reflected properties", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"actor_query"}},
	}
	return &spec.Spec{
		Name: "actor_query", Title: "Find actors", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Read actors in the editor level (world=editor, default), the running game (pie) or auto. Results echo the world.\n- list: label/path/class/location (filter, class).\n- get: one `actor` (label, object path, @gamestate, @pawn) with transform, tags, components; a shared label is a CONFLICT.\n- find: list + `where` {prop: value} and `properties`.",
		Schema: spec.SchemaFor[actorQueryIn](map[string][]any{
			"op": spec.OpEnum(ops...), "world": {"editor", "pie", "auto"}}, "op"),
		Replaces: []string{"list_actors", "get_actor", "find_actors"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			args := pick(c.Args, "op", "world", "actor", "filter", "class", "where", "properties", "limit")
			out, err := v2Op(ctx, c, "actor_query", args)
			if err != nil {
				return nil, err
			}
			if c.Op.Name == "get" {
				return &spec.Result{Data: out, Summary: fmt.Sprintf("%v (%v world)", c.Args["actor"], out["world"])}, nil
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%v actors in the %v world", out["count"], out["world"])}, nil
		},
	}
}

// --- actor_edit ------------------------------------------------------------------

type actorEditIn struct {
	Op         string         `json:"op" jsonschema:"spawn | delete | transform | set_properties"`
	World      string         `json:"world" jsonschema:"REQUIRED: editor (the level) or pie (the running game)"`
	Actor      string         `json:"actor,omitempty" jsonschema:"delete/transform/set_properties: a label, an object path, @gamestate or @pawn"`
	Class      string         `json:"class,omitempty" jsonschema:"spawn: /Script/Module.Class, a /Game Blueprint, Module.Class, or a short name (CONFLICT if ambiguous)"`
	Label      string         `json:"label,omitempty" jsonschema:"spawn: the new actor's label"`
	Location   []float64      `json:"location,omitempty" jsonschema:"[x, y, z]"`
	Rotation   []float64      `json:"rotation,omitempty" jsonschema:"[pitch, yaw, roll] in degrees"`
	Scale      []float64      `json:"scale,omitempty" jsonschema:"[x, y, z]"`
	StaticMesh string         `json:"static_mesh,omitempty" jsonschema:"spawn: static mesh asset for a StaticMeshActor"`
	Properties map[string]any `json:"properties,omitempty" jsonschema:"spawn/set_properties: property → value (asset paths load as objects)"`
}

func actorEditSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "spawn", Summary: "spawn into the editor level, or the running game (world=pie; plugin API 5)", Tier: spec.Mutating, Required: []string{"world", "class"}, Rejects: []string{"actor"}, Reaches: []string{"actor_spawn"}, Needs: []string{"plugin>=5 for pie"}},
		{Name: "delete", Summary: "destroy an actor", Tier: spec.Destructive, Required: []string{"world", "actor"}, Reaches: []string{"actor_delete"}},
		{Name: "transform", Summary: "set location/rotation/scale", Tier: spec.Mutating, Idempotent: true, Required: []string{"world", "actor"}, Reaches: []string{"actor_transform"}},
		{Name: "set_properties", Summary: "set reflected properties", Tier: spec.Mutating, Idempotent: true, Required: []string{"world", "actor", "properties"}, Reaches: []string{"actor_set_properties"}},
	}
	return &spec.Spec{
		Name: "actor_edit", Title: "Edit actors", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Change actors. `world` is REQUIRED: editor (the saved level) or pie (the running game, discarded on stop).\nspawn, delete, transform, set_properties: both worlds (pie spawn: plugin API 5); editor edits are one undo step.\n`actor` = label, object path, @gamestate, @pawn; a shared label is a CONFLICT.",
		Schema: spec.SchemaFor[actorEditIn](map[string][]any{
			"op": spec.OpEnum(ops...), "world": {"editor", "pie"}}, "op", "world"),
		Replaces: []string{"spawn_actor", "delete_actor", "set_actor_transform", "pie_set_property", "pie_destroy"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			py := map[string]string{"spawn": "actor_spawn", "delete": "actor_delete", "transform": "actor_transform", "set_properties": "actor_set_properties"}[c.Op.Name]
			args := pick(c.Args, "world", "actor", "class", "label", "location", "rotation", "scale", "static_mesh", "properties")
			out, err := v2Op(ctx, c, py, args)
			if err != nil {
				return nil, err
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%s done in the %v world", c.Op.Name, out["world"])}, nil
		},
	}
}

// --- undo ------------------------------------------------------------------------

type undoIn struct {
	Op string `json:"op" jsonschema:"undo | redo"`
}

func undoSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "undo", Summary: "undo the server's last editor edit", Tier: spec.Destructive, Reaches: []string{"editor_undo"}, Needs: []string{"plugin>=3"}},
		{Name: "redo", Summary: "redo the server's last undone edit", Tier: spec.Destructive, Reaches: []string{"editor_undo"}, Needs: []string{"plugin>=3"}},
	}
	return &spec.Spec{
		Name: "undo", Title: "Undo the server's edits", Toolset: spec.Core, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Step the editor's undo buffer (editor world; refused during PIE). Acts only when the next step is " +
			"the server's own (title \"MCP: …\"): a human's edit on top is CONFLICT and nothing changes. Covers actor_edit, " +
			"scene, snapshot_restore. After an undoable:false result (asset, widget, python, console edits: no undo step) undo is " +
			"CONFLICT (it would revert an older edit) — use snapshot_restore or git_revert.",
		Schema: spec.SchemaFor[undoIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			out, err := v2Op(ctx, c, "editor_undo", map[string]any{"redo": c.Op.Name == "redo"})
			if err != nil {
				return nil, err
			}
			title, _ := out["undone"].(string)
			if title == "" {
				title, _ = out["redone"].(string)
			}
			return &spec.Result{Data: out, Summary: c.Op.Name + ": " + title}, nil
		},
	}
}

// --- actor_call ------------------------------------------------------------------

type actorCallIn struct {
	Actor     string         `json:"actor" jsonschema:"a label, an object path, @gamestate @pawn @controller @gameinstance @playerstate[:n] @hud, or @subsystem:Class (a game subsystem)"`
	Function  string         `json:"function" jsonschema:"the UFUNCTION name to call"`
	Args      map[string]any `json:"args,omitempty" jsonschema:"parameter name → value"`
	Parse     string         `json:"parse,omitempty" jsonschema:"json: the function returns a JSON string; decode it (an error if it is not JSON)"`
	World     string         `json:"world,omitempty" jsonschema:"pie (default). editor is UNSUPPORTED in v2.0"`
	Until     string         `json:"until,omitempty" jsonschema:"poll until this predicate over {result} holds, e.g. 'result >= 3'; the function RE-RUNS each poll"`
	TimeoutS  float64        `json:"timeout_s,omitempty" jsonschema:"until: give up after this many seconds (default 20, max 27)"`
	IntervalS float64        `json:"interval_s,omitempty" jsonschema:"until: seconds between polls (default 0.25)"`
}

func actorCallSpec() *spec.Spec {
	return &spec.Spec{
		Name: "actor_call", Title: "Call a UFUNCTION", Toolset: spec.Core, Timeout: sync28, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.Exec, Required: []string{"actor", "function"}, Reaches: []string{"actor_call"}, Needs: []string{"pie"}}},
		Description: "Call a UFUNCTION on an actor in the running game (PIE) and return its result. With `until`, " +
			"poll the function until a predicate over {result} holds → {met, result, elapsed_s, calls}; met=false " +
			"on timeout is an answer; the function runs again on every poll.",
		Schema:   spec.SchemaFor[actorCallIn](map[string][]any{"world": {"pie", "editor"}, "parse": {"json"}}, "actor", "function"),
		Replaces: []string{"pie_exec", "pie_verify"},
		Handler:  actorCall,
	}
}

func actorCall(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in actorCallIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	args := pick(c.Args, "actor", "function", "args", "world", "parse")
	if in.Until == "" {
		out, err := v2Op(ctx, c, "actor_call", args)
		return &spec.Result{Data: out, Summary: fmt.Sprintf("%s.%s → %v", in.Actor, in.Function, out["result"])}, err
	}
	pred, err := eval.ParsePredicate(in.Until)
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "until: %v", err)
	}
	if len(pred.ObjectPaths()) > 0 {
		return nil, envelope.New(envelope.InvalidArgument, "until reads the call's result (result.<field>), not object paths").
			WithHint("wait on an object path with pie_wait predicate=...")
	}
	timeout := 20 * time.Second
	if in.TimeoutS > 0 {
		timeout = time.Duration(in.TimeoutS * float64(time.Second))
	}
	if dl, ok := ctx.Deadline(); ok {
		// Return met:false before the call's own deadline: keep a margin for the last
		// poll's round trip (a fifth of the window, at most a second).
		left := time.Until(dl)
		if lim := left - min(time.Second, left/5); lim < timeout {
			timeout = lim
		}
	}
	interval := 250 * time.Millisecond
	if in.IntervalS > 0 {
		interval = time.Duration(in.IntervalS * float64(time.Second))
	}
	start := time.Now()
	deadline := start.Add(timeout)
	calls := 0
	var last, world any = nil, "pie"
	for {
		out, err := v2Op(ctx, c, "actor_call", args)
		calls++
		if err != nil {
			var oe *bridge.OpError
			if !errors.As(err, &oe) || !oe.Retryable {
				return nil, err // a bad function or target will not fix itself
			}
		} else {
			last, world = out["result"], out["world"]
			if ok, _ := pred.Eval(map[string]any{"result": last}); ok {
				return &spec.Result{Data: map[string]any{"met": true, "result": last, "world": world, "elapsed_s": time.Since(start).Seconds(), "calls": calls},
					Summary: "condition met after " + strconv.Itoa(calls) + " calls"}, nil
			}
		}
		if time.Now().Add(interval).After(deadline) {
			return &spec.Result{Data: map[string]any{"met": false, "result": last, "world": world, "elapsed_s": time.Since(start).Seconds(), "calls": calls},
				Summary: "condition not met within " + timeout.String()}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
