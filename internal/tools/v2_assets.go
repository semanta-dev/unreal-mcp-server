package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/projectconfig"
	"github.com/jdziat/unreal-mcp-server/internal/projectmap"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// v2 asset / reflection / project / viewport / widget tools (OVERHAUL_PLAN.md §2.3
// rows 8–15, 26, 41).

func assetSpecs() []*spec.Spec {
	return []*spec.Spec{viewportSpec(), assetQuerySpec(), assetCreateSpec(), assetEditSpec(), assetImportSpec(),
		reflectSpec(), projectConfigSpec(), projectMapSpec(), widgetQuerySpec(), widgetEditSpec()}
}

// attachPNG adds the PNG the editor wrote (same machine) to the result as image
// content. A file that cannot be read is reported as data["image_error"] rather than
// silently omitted.
func attachPNG(res *spec.Result, data map[string]any, path string) {
	if path == "" {
		data["image_error"] = "the editor reported no image path"
		return
	}
	img, err := os.ReadFile(path)
	if err != nil || len(img) == 0 {
		data["image_error"] = fmt.Sprintf("could not read %s: %v", path, err)
		return
	}
	res.Content = append(res.Content, &mcp.ImageContent{Data: img, MIMEType: "image/png"})
}

// rename copies args[from] to out[to] when present.
func rename(out, args map[string]any, pairs ...string) map[string]any {
	for i := 0; i+1 < len(pairs); i += 2 {
		if v, ok := args[pairs[i]]; ok {
			out[pairs[i+1]] = v
		}
	}
	return out
}

// --- viewport --------------------------------------------------------------------

type viewportIn struct {
	Op            string    `json:"op" jsonschema:"get | set | focus | select | selection"`
	Location      []float64 `json:"location,omitempty" jsonschema:"set: camera [x, y, z]"`
	Rotation      []float64 `json:"rotation,omitempty" jsonschema:"set: camera [pitch, yaw, roll] in degrees"`
	Pilot         string    `json:"pilot,omitempty" jsonschema:"set: pilot this actor (label or object path) with the camera"`
	Eject         bool      `json:"eject,omitempty" jsonschema:"set: stop piloting"`
	GameView      *bool     `json:"game_view,omitempty" jsonschema:"set: Game View on/off (hides editor-only actors, like pressing G)"`
	Actors        []string  `json:"actors,omitempty" jsonschema:"focus/select: labels or object paths (focus default: selection)"`
	Mode          string    `json:"mode,omitempty" jsonschema:"select: replace (default) | add | remove | none"`
	Frame         bool      `json:"frame,omitempty" jsonschema:"select: frame the selection afterwards"`
	Pitch         float64   `json:"pitch,omitempty" jsonschema:"focus: camera pitch in degrees (default -30, negative looks down)"`
	DistanceScale float64   `json:"distance_scale,omitempty" jsonschema:"focus: multiplier on the framing distance (default 2)"`
}

func viewportSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "get", Summary: "camera pose and game-view state", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"viewport_get"}},
		{Name: "set", Summary: "move the camera, pilot/eject an actor, toggle game view", Tier: spec.Ephemeral, Idempotent: true,
			Rejects: []string{"console", "actors", "mode"}, Reaches: []string{"viewport_set"}},
		{Name: "focus", Summary: "frame actors (or the selection)", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"focus_actors"}},
		{Name: "select", Summary: "set the editor selection", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"select_actors", "focus_actors"}},
		{Name: "selection", Summary: "the selected actors", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"get_selection"}},
	}
	return &spec.Spec{
		Name: "viewport", Title: "Editor viewport", Toolset: spec.Core, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Editor viewport camera and selection (UI state, nothing saved).\n- get: camera + game_view.\n- set: location/rotation, pilot or eject, game_view.\n- focus: frame `actors` (default the selection).\n- select: `actors` (mode replace|add|remove|none), frame.\n- selection. Console commands: use console.",
		Schema:      spec.SchemaFor[viewportIn](map[string][]any{"op": spec.OpEnum(ops...), "mode": {"replace", "add", "remove", "none"}}, "op"),
		Replaces:    []string{"viewport_set", "viewport_get", "focus_actors", "select_actors", "get_selection"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			py := map[string]string{"get": "viewport_get", "set": "viewport_set", "focus": "focus_actors",
				"select": "select_actors", "selection": "get_selection"}[c.Op.Name]
			args := pick(c.Args, "location", "rotation", "pilot", "eject", "game_view", "actors", "mode", "frame", "pitch", "distance_scale")
			out, err := v2Op(ctx, c, py, args)
			if err != nil {
				return nil, err
			}
			summary := "viewport " + c.Op.Name
			if n, ok := out["count"]; ok {
				summary = fmt.Sprintf("%v actors selected", n)
			}
			return &spec.Result{Data: out, Summary: summary}, nil
		},
	}
}

// --- asset_query -----------------------------------------------------------------

type assetQueryIn struct {
	Op         string   `json:"op" jsonschema:"list | info | search | deps | tags | thumbnail"`
	Asset      string   `json:"asset,omitempty" jsonschema:"info/deps/tags/thumbnail: asset path, e.g. /Game/Meshes/SM_Rock"`
	Folder     string   `json:"folder,omitempty" jsonschema:"list/search: content folder (default /Game)"`
	Classes    []string `json:"classes,omitempty" jsonschema:"search: /Script/Module.Class paths; with blueprints=true, the PARENT classes"`
	Blueprints bool     `json:"blueprints,omitempty" jsonschema:"search: find Blueprints deriving the classes (a Blueprint's own class is always Blueprint)"`
	Recursive  *bool    `json:"recursive,omitempty" jsonschema:"list/search: include subfolders (default true)"`
	Limit      int      `json:"limit,omitempty" jsonschema:"list/search: max results (default 200; total is always the full count)"`
	Size       int      `json:"size,omitempty" jsonschema:"thumbnail: image size in pixels (default 512)"`
}

func assetQuerySpec() *spec.Spec {
	ro := func(name, summary, py string, req ...string) spec.OpSpec {
		return spec.OpSpec{Name: name, Summary: summary, Tier: spec.ReadOnly, Idempotent: true, Required: req, Reaches: []string{py}}
	}
	ops := []spec.OpSpec{
		ro("list", "asset paths under a folder", "list_assets"),
		ro("info", "class, bounds, LODs, Nanite", "asset_info", "asset"),
		ro("search", "AssetRegistry search by class and folder (no loading)", "asset_query"),
		ro("deps", "dependencies and referencers", "asset_deps", "asset"),
		ro("tags", "registry tags (Blueprint lineage) without loading", "asset_tags", "asset"),
		{Name: "thumbnail", Summary: "render a StaticMesh to a PNG + mesh facts", Tier: spec.Ephemeral, Idempotent: true,
			Required: []string{"asset"}, Reaches: []string{"asset_thumbnail"}},
	}
	return &spec.Spec{
		Name: "asset_query", Title: "Find and inspect assets", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Find and inspect Content Browser assets.\n- list: assets under `folder`.\n- info: class, bounds, LODs, Nanite.\n- search: by /Script `classes` and `folder`; blueprints=true finds Blueprints deriving them.\n- deps / tags: dependencies + referencers / registry tags (lineage), no loading.\n- thumbnail: PNG of a StaticMesh + tris/verts/LODs/slots/bounds.",
		Schema:      spec.SchemaFor[assetQueryIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"list_assets", "asset_info", "asset_query", "asset_deps", "asset_tags", "asset_thumbnail"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			a := c.Args
			var py string
			args := map[string]any{}
			switch c.Op.Name {
			case "list":
				py = "list_assets"
				rename(args, a, "folder", "path", "recursive", "recursive", "limit", "limit")
			case "search":
				py = "asset_query"
				rename(args, a, "classes", "class_paths", "blueprints", "blueprints", "recursive", "recursive", "limit", "limit")
				folder, ok := a["folder"]
				if !ok {
					folder = "/Game" // the documented default (the registry alone would include /Engine and plugins)
				}
				args["package_paths"] = []any{folder}
			case "info", "thumbnail":
				py = "asset_" + c.Op.Name
				rename(args, a, "asset", "asset_path", "size", "size")
			default: // deps, tags
				py = "asset_" + c.Op.Name
				rename(args, a, "asset", "asset")
			}
			out, err := v2Op(ctx, c, py, args)
			if err != nil {
				return nil, err
			}
			res := &spec.Result{Data: out}
			switch c.Op.Name {
			case "list", "search":
				res.Summary = fmt.Sprintf("%v assets", out["total"])
			case "thumbnail":
				p, _ := out["thumbnail_path"].(string)
				if out["rendered"] == false {
					out["image_error"] = fmt.Sprint("the thumbnail did not render: ", out["render_error"])
				} else {
					attachPNG(res, out, p)
				}
				res.Summary = fmt.Sprintf("%v: %v tris, %v LODs", a["asset"], out["num_tris_lod0"], out["num_lods"])
			default:
				res.Summary = fmt.Sprintf("%s %v", c.Op.Name, a["asset"])
			}
			return res, nil
		},
	}
}

// --- asset_create ----------------------------------------------------------------

type assetCreateIn struct {
	Op        string         `json:"op" jsonschema:"create (fails with CONFLICT if dest exists) | replace (DELETES an existing dest first)"`
	Kind      string         `json:"kind" jsonschema:"blueprint | data_asset | data_table | material_instance | widget_blueprint"`
	Dest      string         `json:"dest" jsonschema:"new asset path, e.g. /Game/BP/BP_LaserTurret"`
	Class     string         `json:"class,omitempty" jsonschema:"blueprint/widget_blueprint: parent class; data_asset: its class"`
	RowStruct string         `json:"row_struct,omitempty" jsonschema:"data_table: row struct (/Script/Module.Row or a UserDefinedStruct asset)"`
	Parent    string         `json:"parent,omitempty" jsonschema:"material_instance: parent material asset"`
	Params    map[string]any `json:"params,omitempty" jsonschema:"material_instance: {scalar:{name:value}, vector:{name:[r,g,b,a]}, texture:{name:asset}}"`
	RootPanel string         `json:"root_panel,omitempty" jsonschema:"widget_blueprint: root panel (default CanvasPanel; Overlay for a stacked full-screen menu)"`
}

func assetCreateSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "create", Summary: "create a new asset (CONFLICT if dest exists)", Tier: spec.Mutating,
			Required: []string{"kind", "dest"}, Rejects: []string{"replace"}, Reaches: []string{"asset_create"}},
		{Name: "replace", Summary: "delete the asset at dest, then create", Tier: spec.Destructive,
			Required: []string{"kind", "dest"}, Reaches: []string{"asset_create"}},
	}
	return &spec.Spec{
		Name: "asset_create", Title: "Create an asset", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Create an asset at `dest` (saved).\n- create: CONFLICT if dest exists.\n- replace: DELETES the existing asset first (references break).\nkind: blueprint (class = parent) | data_asset (class) | data_table (row_struct) | material_instance (parent, params) | widget_blueprint (class?, root_panel?; then widget_edit).",
		Schema: spec.SchemaFor[assetCreateIn](map[string][]any{"op": spec.OpEnum(ops...),
			"kind": {"blueprint", "data_asset", "data_table", "material_instance", "widget_blueprint"}}, "op", "kind", "dest"),
		Replaces: []string{"blueprint_create", "dataasset_create", "datatable_create", "create_material_instance", "widget_create"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			kind, _ := c.Args["kind"].(string)
			need := map[string]string{"blueprint": "class", "data_asset": "class", "data_table": "row_struct", "material_instance": "parent"}[kind]
			if v, _ := c.Args[need].(string); need != "" && v == "" {
				return nil, envelope.New(envelope.InvalidArgument, "kind=%s requires %s", kind, need)
			}
			args := pick(c.Args, "kind", "dest", "class", "row_struct", "parent", "params", "root_panel")
			args["replace"] = c.Op.Name == "replace"
			out, err := v2Op(ctx, c, "asset_create", args)
			if err != nil {
				return nil, err
			}
			verb := "created"
			if out["replaced"] == true {
				verb = "replaced"
			}
			out["undoable"] = false // no editor transaction: roll back with git_revert
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%s %s %v", verb, kind, c.Args["dest"])}, nil
		},
	}
}

// --- asset_edit ------------------------------------------------------------------

type assetEditIn struct {
	Op         string         `json:"op" jsonschema:"set_defaults | add_component | assign_subclass"`
	Asset      string         `json:"asset" jsonschema:"the Blueprint asset path"`
	Properties map[string]any `json:"properties,omitempty" jsonschema:"set_defaults: CDO property -> value, e.g. {Range: 4000} (asset paths load as objects)"`
	Class      string         `json:"class,omitempty" jsonschema:"add_component: component class; assign_subclass: the class to assign"`
	Name       string         `json:"name,omitempty" jsonschema:"add_component: the new component's name"`
	Property   string         `json:"property,omitempty" jsonschema:"assign_subclass: a TSubclassOf property, e.g. ProjectileClass"`
}

func assetEditSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "set_defaults", Summary: "set CDO default values", Tier: spec.Mutating, Idempotent: true, Required: []string{"asset", "properties"}, Reaches: []string{"asset_edit"}},
		{Name: "add_component", Summary: "add a component", Tier: spec.Mutating, Required: []string{"asset", "class"}, Reaches: []string{"asset_edit"}},
		{Name: "assign_subclass", Summary: "set a TSubclassOf default", Tier: spec.Mutating, Idempotent: true, Required: []string{"asset", "property", "class"}, Reaches: []string{"asset_edit"}},
	}
	return &spec.Spec{
		Name: "asset_edit", Title: "Edit a Blueprint", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Edit a Blueprint's class defaults; each op compiles and saves.\n" +
			"- op=set_defaults: `properties` on the CDO (per-property failures come back in errors[]).\n" +
			"- op=add_component: add a `class` component (optional `name`).\n" +
			"- op=assign_subclass: set TSubclassOf `property` to `class` (e.g. GameMode DefaultPawnClass).",
		Schema:   spec.SchemaFor[assetEditIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op", "asset"),
		Replaces: []string{"blueprint_set_defaults", "blueprint_add_component", "assign_subclass"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			out, err := v2Op(ctx, c, "asset_edit", pick(c.Args, "op", "asset", "properties", "class", "name", "property"))
			if err != nil {
				return nil, err
			}
			out["undoable"] = false // no editor transaction: roll back with git_revert
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%s on %v", c.Op.Name, c.Args["asset"])}, nil
		},
	}
}

// --- asset_import ----------------------------------------------------------------

type assetImportIn struct {
	Op     string   `json:"op" jsonschema:"files | reimport | datatable"`
	Files  []string `json:"files,omitempty" jsonschema:"files: FBX/texture/audio files on disk to import"`
	Folder string   `json:"folder,omitempty" jsonschema:"files: destination content folder (default /Game/Imported)"`
	Asset  string   `json:"asset,omitempty" jsonschema:"reimport: the asset; datatable: the DataTable"`
	JSON   string   `json:"json,omitempty" jsonschema:"datatable: rows as a JSON string (preferred)"`
	CSV    string   `json:"csv,omitempty" jsonschema:"datatable: rows as a CSV string"`
}

func assetImportSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "files", Summary: "import files (overwrites same-named assets)", Tier: spec.Destructive, Required: []string{"files"}, Reaches: []string{"import_assets"}},
		{Name: "reimport", Summary: "reimport an asset from its source file", Tier: spec.Destructive, Required: []string{"asset"}, Reaches: []string{"asset_reimport"}},
		{Name: "datatable", Summary: "replace a DataTable's rows from JSON/CSV", Tier: spec.Destructive, Required: []string{"asset"}, Reaches: []string{"datatable_import"}},
	}
	return &spec.Spec{
		Name: "asset_import", Title: "Import assets", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Bring outside data into assets. Every op OVERWRITES existing content.\n" +
			"- op=files: import `files` into `folder`; an asset with the same name is replaced.\n" +
			"- op=reimport: reload `asset` from its source file.\n" +
			"- op=datatable: replace ALL rows of DataTable `asset` from `json` (preferred) or `csv`.",
		Schema:   spec.SchemaFor[assetImportIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"import_assets", "asset_reimport", "datatable_import"},
		Handler:  assetImport,
	}
}

func assetImport(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in assetImportIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "files":
		var missing []string
		for i, f := range in.Files {
			abs, err := filepath.Abs(f) // the editor resolves relative paths from its own cwd
			if err == nil {
				in.Files[i] = abs
			}
			if st, err := os.Stat(in.Files[i]); err != nil || st.IsDir() {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			return nil, envelope.New(envelope.NotFound, "%d file(s) not found", len(missing)).WithDetail("missing", missing)
		}
		args := map[string]any{"file_paths": in.Files}
		if in.Folder != "" {
			args["destination_path"] = in.Folder
		}
		out, err := v2Op(ctx, c, "import_assets", args)
		if err != nil {
			return nil, err
		}
		imported, _ := out["imported"].([]any)
		failed, _ := out["failed"].([]any)
		if len(imported) == 0 && len(failed) > 0 {
			return nil, withLog(envelope.New(envelope.OperationFailed, "no file imported").WithDetail("failed", failed), out)
		}
		return &spec.Result{Data: out, Summary: fmt.Sprintf("imported %d, failed %d", len(imported), len(failed))}, nil
	case "reimport":
		out, err := v2Op(ctx, c, "asset_reimport", map[string]any{"asset_path": in.Asset})
		return &spec.Result{Data: out, Summary: "reimported " + in.Asset}, err
	}
	if (in.JSON == "") == (in.CSV == "") {
		return nil, envelope.New(envelope.InvalidArgument, "asset_import op=datatable needs exactly one of json or csv")
	}
	args := map[string]any{"datatable": in.Asset}
	if in.JSON != "" {
		args["json"] = in.JSON
	} else {
		args["csv"] = in.CSV
	}
	out, err := v2Op(ctx, c, "datatable_import", args)
	return &spec.Result{Data: out, Summary: fmt.Sprintf("%s now has %v rows", in.Asset, out["rows"])}, err
}

// --- reflect ---------------------------------------------------------------------

type reflectIn struct {
	Op         string   `json:"op" jsonschema:"object | class | enum"`
	Actor      string   `json:"actor,omitempty" jsonschema:"object: label, object path, or in PIE @gamestate, @pawn or @controller"`
	World      string   `json:"world,omitempty" jsonschema:"object: editor (default) | pie | auto"`
	Class      string   `json:"class,omitempty" jsonschema:"class: /Script path, /Game Blueprint, Module.Class or short name"`
	Enum       string   `json:"enum,omitempty" jsonschema:"enum: a UENUM(BlueprintType) name or UserDefinedEnum asset"`
	Include    []string `json:"include,omitempty" jsonschema:"object/class: glob patterns of property names to include (default all)"`
	Exclude    []string `json:"exclude,omitempty" jsonschema:"object/class: glob patterns to exclude"`
	Properties []string `json:"properties,omitempty" jsonschema:"object/class: read exactly these properties (overrides include/exclude)"`
	MaxProps   int      `json:"max_props,omitempty" jsonschema:"object/class: cap on discovered properties (default 64)"`
	MaxStr     int      `json:"max_str,omitempty" jsonschema:"object: truncate string values to this length (default 512)"`
}

func reflectSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "object", Summary: "a live actor's properties and functions", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"actor"}, Reaches: []string{"reflect"}},
		{Name: "class", Summary: "a class's default values and functions", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"class"}, Rejects: []string{"actor", "world"}, Reaches: []string{"reflect"}},
		{Name: "enum", Summary: "an enum's names and values", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"enum"}, Rejects: []string{"actor", "world"}, Reaches: []string{"reflect"}},
	}
	return &spec.Spec{
		Name: "reflect", Title: "Reflect objects, classes, enums", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Discover what an object exposes, without knowing the game.\n" +
			"- op=object: `actor` in `world` (editor default) → {class, path, properties, functions}.\n" +
			"- op=class: a class contract — its CDO defaults and functions.\n" +
			"- op=enum: enumerator names and values (e.g. to write a predicate over an enum field).",
		Schema:   spec.SchemaFor[reflectIn](map[string][]any{"op": spec.OpEnum(ops...), "world": {"editor", "pie", "auto"}}, "op"),
		Replaces: []string{"reflect_object", "reflect_class", "enum_values"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			out, err := v2Op(ctx, c, "reflect", pick(c.Args, "op", "actor", "world", "class", "enum", "include", "exclude", "properties", "max_props", "max_str"))
			if err != nil {
				return nil, err
			}
			subject := c.Args["actor"]
			if c.Op.Name != "object" {
				subject = c.Args[c.Op.Name]
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("reflected %s %v", c.Op.Name, subject)}, nil
		},
	}
}

// --- project_config --------------------------------------------------------------

type projectConfigIn struct {
	Op      string  `json:"op" jsonschema:"set_default_gamemode | input_action | input_axis | gameplay_tag"`
	Class   string  `json:"class,omitempty" jsonschema:"set_default_gamemode: GameMode class path, e.g. /Game/BP/BP_GM.BP_GM_C"`
	Name    string  `json:"name,omitempty" jsonschema:"input_action/input_axis: mapping name, e.g. Dash or MoveForward"`
	Key     string  `json:"key,omitempty" jsonschema:"input_action/input_axis: UE key name, e.g. SpaceBar, W, LeftMouseButton"`
	Shift   bool    `json:"shift,omitempty" jsonschema:"input_action: modifier"`
	Ctrl    bool    `json:"ctrl,omitempty" jsonschema:"input_action: modifier"`
	Alt     bool    `json:"alt,omitempty" jsonschema:"input_action: modifier"`
	Cmd     bool    `json:"cmd,omitempty" jsonschema:"input_action: modifier"`
	Scale   float64 `json:"scale,omitempty" jsonschema:"input_axis: axis scale (default 1; -1 for the reverse key)"`
	Tag     string  `json:"tag,omitempty" jsonschema:"gameplay_tag: e.g. Ability.Dash"`
	Comment string  `json:"comment,omitempty" jsonschema:"gameplay_tag: dev comment"`
}

func projectConfigSpec() *spec.Spec {
	op := func(name, summary string, req ...string) spec.OpSpec {
		return spec.OpSpec{Name: name, Summary: summary, Tier: spec.Mutating, Idempotent: true, Required: req, Needs: []string{"project"}}
	}
	ops := []spec.OpSpec{
		op("set_default_gamemode", "project default GameMode (DefaultEngine.ini)", "class"),
		op("input_action", "legacy input action mapping (DefaultInput.ini)", "name", "key"),
		op("input_axis", "legacy input axis mapping (DefaultInput.ini)", "name", "key"),
		op("gameplay_tag", "add a gameplay tag (DefaultGameplayTags.ini)", "tag"),
	}
	return &spec.Spec{
		Name: "project_config", Title: "Project config (.ini)", Toolset: spec.Core, Offline: true, Timeout: sync8, Max: sync8, Ops: ops,
		Description: "Edit Config/*.ini directly (no editor; idempotent; the editor reads most at startup).\n- set_default_gamemode `class`.\n- input_action `name` `key` modifiers.\n- input_axis `name` `key` `scale`.\n- gameplay_tag `tag` `comment`.",
		Schema:      spec.SchemaFor[projectConfigIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"set_gamemode", "input_action", "input_axis", "gameplay_tag_add"},
		Handler:     projectConfig,
	}
}

func projectDir(c *spec.Call) (string, error) {
	if c.Deps.ProjectDir == "" {
		e := envelope.New(envelope.Precondition, "no project is configured for this session")
		if c.Deps.Projects != nil {
			return "", e.WithHint("call project with op=attach and the project directory first")
		}
		return "", e.WithHint("start the server with -project <dir> or set UMCP_PROJECT_DIR")
	}
	return c.Deps.ProjectDir, nil
}

func projectConfig(_ context.Context, c *spec.Call) (*spec.Result, error) {
	var in projectConfigIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	dir, err := projectDir(c)
	if err != nil {
		return nil, err
	}
	cfg := filepath.Join(dir, "Config")
	out := map[string]any{"op": in.Op}
	switch in.Op {
	case "set_default_gamemode":
		err, out["game_mode"], out["file"] = projectconfig.SetGameMode(cfg, in.Class), in.Class, "DefaultEngine.ini"
	case "input_action":
		err = projectconfig.AddActionMapping(cfg, projectconfig.ActionMapping{Name: in.Name, Key: in.Key, Shift: in.Shift, Ctrl: in.Ctrl, Alt: in.Alt, Cmd: in.Cmd})
		out["action"], out["key"], out["file"] = in.Name, in.Key, "DefaultInput.ini"
	case "input_axis":
		if in.Scale == 0 {
			in.Scale = 1
		}
		err = projectconfig.AddAxisMapping(cfg, projectconfig.AxisMapping{Name: in.Name, Key: in.Key, Scale: in.Scale})
		out["axis"], out["key"], out["scale"], out["file"] = in.Name, in.Key, in.Scale, "DefaultInput.ini"
	default:
		err, out["tag"], out["file"] = projectconfig.AddGameplayTag(cfg, in.Tag, in.Comment), in.Tag, "DefaultGameplayTags.ini"
	}
	if err != nil {
		return nil, envelope.New(envelope.OperationFailed, "%s: %v", in.Op, err)
	}
	return &spec.Result{Data: out, Summary: fmt.Sprintf("%s written to Config/%v", in.Op, out["file"])}, nil
}

// --- project_map -----------------------------------------------------------------

type projectMapIn struct {
	Op string `json:"op" jsonschema:"project (offline C++ map) | level (live gameplay framework; needs the editor)"`
}

func projectMapSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "project", Summary: "modules + every UCLASS/USTRUCT/UENUM, parsed offline", Tier: spec.ReadOnly, Idempotent: true, Timeout: sync20, Needs: []string{"project"}},
		{Name: "level", Summary: "the level's GameMode wiring (+ live classes in PIE)", Tier: spec.ReadOnly, Idempotent: true, Timeout: sync15, Reaches: []string{"map_gameplay"}},
	}
	return &spec.Spec{
		Name: "project_map", Title: "Map the project", Toolset: spec.Core, Max: sync28, Ops: ops,
		Description: "Orient in a project.\n- project: offline (no editor) — modules, dependencies, every UCLASS/USTRUCT/UENUM with /Script path and header.\n- level: the level's GameMode wiring, plus live classes in PIE.",
		Schema:      spec.SchemaFor[projectMapIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"project_map", "map_gameplay"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			if c.Op.Name == "level" {
				out, err := v2Op(ctx, c, "map_gameplay", nil)
				return &spec.Result{Data: out, Summary: fmt.Sprintf("level GameMode %v", out["world_settings_game_mode"])}, err
			}
			dir, err := projectDir(c)
			if err != nil {
				return nil, err
			}
			m, err := projectmap.Scan(dir)
			if err != nil {
				return nil, envelope.New(envelope.OperationFailed, "scan %s: %v", dir, err)
			}
			return &spec.Result{Data: map[string]any{"project": m.Project, "modules": m.Modules, "types": m.Types},
				Summary: fmt.Sprintf("%s: %d modules, %d types", m.Project, len(m.Modules), len(m.Types))}, nil
		},
	}
}

// --- widget_query / widget_edit --------------------------------------------------

type widgetQueryIn struct {
	Op     string `json:"op" jsonschema:"tree | describe | render"`
	Asset  string `json:"asset,omitempty" jsonschema:"tree: the WidgetBlueprint asset path"`
	Class  string `json:"class,omitempty" jsonschema:"describe: a widget class (omit for the palette); render: the UserWidget class"`
	Width  int    `json:"width,omitempty" jsonschema:"render: pixels (default 1280)"`
	Height int    `json:"height,omitempty" jsonschema:"render: pixels (default 720)"`
}

func widgetQuerySpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "tree", Summary: "canonical widget tree + structural digest", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"asset"}, Reaches: []string{"widget_tree"}},
		{Name: "describe", Summary: "the authorable palette, or one class's props and slot", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"widget_describe"}},
		{Name: "render", Summary: "render a widget class offscreen to a PNG", Tier: spec.Ephemeral, Idempotent: true, Required: []string{"class"}, Reaches: []string{"widget_render"}, Needs: []string{"plugin"}},
	}
	return &spec.Spec{
		Name: "widget_query", Title: "Inspect UMG widgets", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Inspect UMG widgets without PIE.\n- tree: a WidgetBlueprint's tree + digest.\n- describe: the palette, or one class's props and slot type.\n- render: a UserWidget class as a PNG (MCPAuthoring module).",
		Schema:      spec.SchemaFor[widgetQueryIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces:    []string{"widget_tree", "widget_describe", "widget_render"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			switch c.Op.Name {
			case "tree":
				out, err := v2Op(ctx, c, "widget_tree", map[string]any{"blueprint": c.Args["asset"]})
				return &spec.Result{Data: out, Summary: fmt.Sprintf("%v digest %v", c.Args["asset"], out["digest"])}, err
			case "describe":
				out, err := v2Op(ctx, c, "widget_describe", rename(map[string]any{}, c.Args, "class", "widget_class"))
				return &spec.Result{Data: out, Summary: "widget palette"}, err
			}
			out, err := v2Op(ctx, c, "widget_render", rename(map[string]any{}, c.Args, "class", "widget_class", "width", "width", "height", "height"))
			if err != nil {
				return nil, err
			}
			p, _ := out["path"].(string)
			res := &spec.Result{Data: out, Summary: "rendered " + fmt.Sprint(c.Args["class"])}
			attachPNG(res, out, p)
			return res, nil
		},
	}
}

type widgetEditIn struct {
	Op     string         `json:"op" jsonschema:"compose | prune | compile"`
	Asset  string         `json:"asset" jsonschema:"the WidgetBlueprint asset path"`
	Tree   map[string]any `json:"tree,omitempty" jsonschema:"compose/prune: declarative node {name, class, slot?, props?, brush?, font?, is_variable?, children?[]}; every node named; class is a friendly name (TextBlock, ProgressBar, Image, Button, CanvasPanel, VerticalBox, Overlay, ...) or a /Script or /Game path"`
	Mode   string         `json:"mode,omitempty" jsonschema:"compose: full (default; converge the whole tree on the spec, never deleting) | patch (only the given nodes)"`
	Defer  bool           `json:"defer,omitempty" jsonschema:"compose mode=patch: apply without compiling until op=compile"`
	Remove []string       `json:"remove,omitempty" jsonschema:"prune: also remove these named nodes"`
}

func widgetEditSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "compose", Summary: "add/update nodes from a declarative tree (additive), then compile", Tier: spec.Mutating, Idempotent: true,
			Required: []string{"asset", "tree"}, Rejects: []string{"remove", "prune"}, Reaches: []string{"widget_compose"}},
		{Name: "prune", Summary: "converge on the tree, DELETING nodes absent from it (+ remove)", Tier: spec.Destructive,
			Required: []string{"asset", "tree"}, Rejects: []string{"mode", "defer"}, Reaches: []string{"widget_compose"}},
		{Name: "compile", Summary: "compile and save; ends a deferred compose", Tier: spec.Mutating, Idempotent: true,
			Required: []string{"asset"}, Rejects: []string{"tree", "remove", "mode", "defer"}, Reaches: []string{"widget_compile"}},
	}
	return &spec.Spec{
		Name: "widget_edit", Title: "Author UMG widgets", Toolset: spec.UI, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Author a WidgetBlueprint's tree declaratively (create the asset with asset_create kind=widget_blueprint).\n" +
			"- op=compose: apply `tree` — add/update/reorder nodes, slots, props — then compile; never deletes. " +
			"Re-composing the same tree is a no-op (same digest).\n" +
			"- op=prune: like compose, but DELETES every node absent from `tree` and the names in `remove`.\n" +
			"- op=compile: compile + save; returns the digest (and ends a deferred patch).\n" +
			"Check the result with widget_query op=tree / op=render.",
		Schema:   spec.SchemaFor[widgetEditIn](map[string][]any{"op": spec.OpEnum(ops...), "mode": {"full", "patch"}}, "op", "asset"),
		Replaces: []string{"widget_compose", "widget_compile"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			if c.Op.Name == "compile" {
				out, err := v2Op(ctx, c, "widget_compile", map[string]any{"blueprint": c.Args["asset"]})
				if err != nil {
					return nil, err
				}
				out["undoable"] = false
				return &spec.Result{Data: out, Summary: fmt.Sprintf("compiled %v (digest %v)", c.Args["asset"], out["digest"])}, nil
			}
			args := rename(map[string]any{}, c.Args, "asset", "blueprint", "tree", "tree", "mode", "mode", "defer", "defer", "remove", "remove")
			if c.Op.Name == "prune" {
				args["prune"] = true
			}
			out, err := v2Op(ctx, c, "widget_compose", args)
			if err != nil {
				return nil, err
			}
			out["undoable"] = false // no editor transaction: roll back with git_revert
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%s %v → digest %v", c.Op.Name, c.Args["asset"], out["digest"])}, nil
		},
	}
}
