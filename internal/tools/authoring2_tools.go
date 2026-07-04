package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/projectconfig"
)

type blueprintCreateIn struct {
	ParentClassPath string `json:"parent_class_path" jsonschema:"C++ or Blueprint parent, e.g. /Script/AesirWaveDefense.Turret"`
	Dest            string `json:"dest" jsonschema:"destination asset path, e.g. /Game/BP/BP_LaserTurret"`
}
type blueprintDefaultsIn struct {
	Blueprint string         `json:"blueprint" jsonschema:"the Blueprint asset path"`
	Defaults  map[string]any `json:"defaults" jsonschema:"CDO property overrides, e.g. {Range:4000, Damage:40}"`
}
type assignSubclassIn struct {
	Target    string `json:"target" jsonschema:"the Blueprint whose CDO to edit"`
	Prop      string `json:"prop" jsonschema:"a TSubclassOf property name, e.g. ProjectileClass"`
	ClassPath string `json:"class_path" jsonschema:"the class to assign"`
}
type bpAddComponentIn struct {
	Blueprint      string `json:"blueprint"`
	ComponentClass string `json:"component_class" jsonschema:"e.g. /Script/Engine.StaticMeshComponent"`
	Name           string `json:"name,omitempty"`
}
type datatableCreateIn struct {
	Dest      string `json:"dest"`
	RowStruct string `json:"row_struct" jsonschema:"/Script/Module.MyRow or a UserDefinedStruct asset path"`
}
type datatableImportIn struct {
	Datatable string `json:"datatable"`
	JSON      string `json:"json,omitempty" jsonschema:"rows as a JSON string (the reliable population path)"`
	CSV       string `json:"csv,omitempty" jsonschema:"rows as a CSV string"`
}
type dataassetCreateIn struct {
	Dest  string `json:"dest"`
	Class string `json:"class" jsonschema:"the DataAsset (sub)class"`
}
type widgetCreateIn struct {
	Dest string `json:"dest"`
}
type classPathIn struct {
	ClassPath string `json:"class_path"`
}
type pieSetPropertyIn struct {
	Target     string         `json:"target" jsonschema:"actor label in the live game world"`
	Properties map[string]any `json:"properties"`
}
type pieTargetIn struct {
	Target string `json:"target" jsonschema:"actor label in the live game world"`
}
type setGameModeIn struct {
	ClassPath string `json:"class_path" jsonschema:"GameMode class path, e.g. /Game/BP/BP_GM.BP_GM_C or /Script/Module.MyGameMode"`
}
type inputActionIn struct {
	Name  string `json:"name" jsonschema:"action name, e.g. Dash"`
	Key   string `json:"key" jsonschema:"UE key name, e.g. SpaceBar, LeftShift, LeftMouseButton"`
	Shift bool   `json:"shift,omitempty"`
	Ctrl  bool   `json:"ctrl,omitempty"`
	Alt   bool   `json:"alt,omitempty"`
	Cmd   bool   `json:"cmd,omitempty"`
}
type inputAxisIn struct {
	Name  string  `json:"name" jsonschema:"axis name, e.g. MoveForward"`
	Key   string  `json:"key" jsonschema:"UE key name, e.g. W"`
	Scale float64 `json:"scale,omitempty" jsonschema:"axis scale, e.g. 1.0 or -1.0"`
}
type gameplayTagIn struct {
	Tag     string `json:"tag" jsonschema:"e.g. Ability.Dash"`
	Comment string `json:"comment,omitempty"`
}

// registerAuthoring2Tools adds the P3 structured-authoring surface: Blueprint
// create/compose/defaults, data assets, and the pure-Go .ini editors (GameMode,
// legacy input, gameplay tags). Graph/node + widget-tree authoring is plugin-only.
func registerAuthoring2Tools(s *mcp.Server, d Deps) {
	b := d.Bridge

	add(s, "blueprint_create",
		"Create a Blueprint asset from a C++/Blueprint parent class (the sanctioned split: logic in C++, Blueprints carry data + composition).",
		structHandler[blueprintCreateIn](b, "blueprint_create", func(in blueprintCreateIn) map[string]any {
			return map[string]any{"parent_class_path": in.ParentClassPath, "dest": in.Dest}
		}))
	add(s, "blueprint_set_defaults",
		"Set default property values on a Blueprint's CDO, then compile + save.",
		structHandler[blueprintDefaultsIn](b, "blueprint_set_defaults", func(in blueprintDefaultsIn) map[string]any {
			return map[string]any{"blueprint": in.Blueprint, "defaults": in.Defaults}
		}))
	add(s, "assign_subclass",
		"Assign a class into a Blueprint CDO's TSubclassOf property (e.g. GameMode.DefaultPawnClass, Weapon.ProjectileClass).",
		structHandler[assignSubclassIn](b, "assign_subclass", func(in assignSubclassIn) map[string]any {
			return map[string]any{"target": in.Target, "prop": in.Prop, "class_path": in.ClassPath}
		}))
	add(s, "blueprint_add_component",
		"Add a component to a Blueprint via the SubobjectDataSubsystem, then compile.",
		structHandler[bpAddComponentIn](b, "blueprint_add_component", func(in bpAddComponentIn) map[string]any {
			m := map[string]any{"blueprint": in.Blueprint, "component_class": in.ComponentClass}
			if in.Name != "" {
				m["name"] = in.Name
			}
			return m
		}))
	add(s, "datatable_create",
		"Create a DataTable asset backed by a row struct.",
		structHandler[datatableCreateIn](b, "datatable_create", func(in datatableCreateIn) map[string]any {
			return map[string]any{"dest": in.Dest, "row_struct": in.RowStruct}
		}))
	add(s, "datatable_import",
		"Populate a DataTable from a JSON (preferred) or CSV string; returns imported:true + row count, or an IMPORT_FAILED code (per-row errors go to the editor log).",
		structHandler[datatableImportIn](b, "datatable_import", func(in datatableImportIn) map[string]any {
			m := map[string]any{"datatable": in.Datatable}
			if in.JSON != "" {
				m["json"] = in.JSON
			}
			if in.CSV != "" {
				m["csv"] = in.CSV
			}
			return m
		}))
	add(s, "dataasset_create",
		"Create a DataAsset instance of a class.",
		structHandler[dataassetCreateIn](b, "dataasset_create", func(in dataassetCreateIn) map[string]any {
			return map[string]any{"dest": in.Dest, "class": in.Class}
		}))
	add(s, "widget_create",
		"Create a Widget Blueprint asset (the tree/binding authoring itself needs the C++ plugin).",
		structHandler[widgetCreateIn](b, "widget_create", func(in widgetCreateIn) map[string]any {
			return map[string]any{"dest": in.Dest}
		}))
	add(s, "set_world_gamemode",
		"Set the current level's WorldSettings GameMode override (a per-map override; use set_gamemode for the project default).",
		structHandler[classPathIn](b, "set_world_gamemode", func(in classPathIn) map[string]any {
			return map[string]any{"class_path": in.ClassPath}
		}))
	add(s, "pie_set_property",
		"Set properties on a LIVE actor in the game world (arrange a test precondition).",
		structHandler[pieSetPropertyIn](b, "pie_set_property", func(in pieSetPropertyIn) map[string]any {
			return map[string]any{"target": in.Target, "properties": in.Properties}
		}))
	add(s, "pie_destroy",
		"Destroy a live actor in the game world by label.",
		structHandler[pieTargetIn](b, "pie_destroy", func(in pieTargetIn) map[string]any {
			return map[string]any{"target": in.Target}
		}))

	// --- pure-Go .ini editors (offline, no editor) ---
	configDir := func(ctx context.Context) (string, bool) {
		pd := resolveDeps(ctx, d).ProjectDir
		if pd == "" {
			return "", false
		}
		return pd + "/Config", true
	}

	add(s, "set_gamemode",
		"Set the project's default GameMode in DefaultEngine.ini (offline, idempotent).",
		func(ctx context.Context, _ *mcp.CallToolRequest, in setGameModeIn) (*mcp.CallToolResult, map[string]any, error) {
			cd, ok := configDir(ctx)
			if !ok {
				return nil, nil, errNoProject
			}
			if err := projectconfig.SetGameMode(cd, in.ClassPath); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"ok": true, "game_mode": in.ClassPath}, nil
		})
	add(s, "input_action",
		"Add/update a legacy input ACTION mapping in DefaultInput.ini (the path aesir uses; offline, idempotent).",
		func(ctx context.Context, _ *mcp.CallToolRequest, in inputActionIn) (*mcp.CallToolResult, map[string]any, error) {
			cd, ok := configDir(ctx)
			if !ok {
				return nil, nil, errNoProject
			}
			err := projectconfig.AddActionMapping(cd, projectconfig.ActionMapping{
				Name: in.Name, Key: in.Key, Shift: in.Shift, Ctrl: in.Ctrl, Alt: in.Alt, Cmd: in.Cmd})
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"ok": true, "action": in.Name, "key": in.Key}, nil
		})
	add(s, "input_axis",
		"Add/update a legacy input AXIS mapping in DefaultInput.ini (offline, idempotent).",
		func(ctx context.Context, _ *mcp.CallToolRequest, in inputAxisIn) (*mcp.CallToolResult, map[string]any, error) {
			cd, ok := configDir(ctx)
			if !ok {
				return nil, nil, errNoProject
			}
			scale := in.Scale
			if scale == 0 {
				scale = 1
			}
			if err := projectconfig.AddAxisMapping(cd, projectconfig.AxisMapping{Name: in.Name, Key: in.Key, Scale: scale}); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"ok": true, "axis": in.Name, "key": in.Key, "scale": scale}, nil
		})
	add(s, "gameplay_tag_add",
		"Append a gameplay tag to DefaultGameplayTags.ini (offline, idempotent).",
		func(ctx context.Context, _ *mcp.CallToolRequest, in gameplayTagIn) (*mcp.CallToolResult, map[string]any, error) {
			cd, ok := configDir(ctx)
			if !ok {
				return nil, nil, errNoProject
			}
			if err := projectconfig.AddGameplayTag(cd, in.Tag, in.Comment); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"ok": true, "tag": in.Tag}, nil
		})
}
