package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/projectmap"
)

type assetQueryIn struct {
	ClassPaths       []string `json:"class_paths,omitempty" jsonschema:"class paths to match, e.g. /Script/Engine.StaticMesh; with blueprints=true these are the PARENT classes, e.g. /Script/AesirWaveDefense.Turret"`
	PackagePaths     []string `json:"package_paths,omitempty" jsonschema:"content roots to search, e.g. /Game/Blueprints"`
	Blueprints       bool     `json:"blueprints,omitempty" jsonschema:"find Blueprint assets DERIVING the class_paths (a BP's own class is always /Script/Engine.Blueprint, so a plain query never finds them). Use for 'every BP deriving ATurret'."`
	Recursive        *bool    `json:"recursive,omitempty" jsonschema:"recurse package paths; default true"`
	RecursiveClasses *bool    `json:"recursive_classes,omitempty" jsonschema:"match subclasses of class_paths; default true"`
	Limit            int      `json:"limit,omitempty" jsonschema:"max results; default 200"`
}

type assetIn struct {
	Asset string `json:"asset" jsonschema:"an asset object or package path, e.g. /Game/BP/BP_Turret"`
}

type reflectClassIn struct {
	ClassPath  string   `json:"class_path" jsonschema:"/Script/Module.Class or a /Game Blueprint path"`
	Include    []string `json:"include,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
	Properties []string `json:"properties,omitempty"`
	MaxProps   int      `json:"max_props,omitempty"`
}

type enumValuesIn struct {
	EnumPath string `json:"enum_path" jsonschema:"a UENUM(BlueprintType) name (e.g. EWaveState) or a UserDefinedEnum asset path"`
}

type mapGameplayIn struct {
	Level string `json:"level,omitempty" jsonschema:"reserved; current editor/PIE world is used"`
}

type findActorsIn struct {
	ClassPath string            `json:"class_path,omitempty" jsonschema:"filter to this actor class (/Script or Blueprint path); default all actors"`
	Where     map[string]string `json:"where,omitempty" jsonschema:"filter to actors whose property equals a value (string-compared), e.g. Archetype maps to Caster"`
	Reflect   []string          `json:"reflect,omitempty" jsonschema:"property names to include per matched actor"`
	World     string            `json:"world,omitempty" jsonschema:"auto|editor|game; default auto"`
	Limit     int               `json:"limit,omitempty" jsonschema:"max results; default 100"`
}

// registerDiscoveryTools adds the P2 cold-start orientation tools: a pure-Go
// project map (offline) plus editor-side asset-registry / reflection / gameplay
// introspection so an agent can learn a project's classes, assets, and framework
// wiring without pre-baked knowledge.
func registerDiscoveryTools(s *registrar, d Deps) {
	b := d.Bridge

	add(s, "project_map",
		"Map the project's C++ surface OFFLINE (no editor): modules + dependencies + every UCLASS/USTRUCT/UENUM and its /Script path, parsed from the .uproject/Build.cs/headers. The fastest way to resolve a /Script path or find a class's header.",
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mapGameplayIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			m, err := projectmap.Scan(resolveDeps(ctx, d).ProjectDir)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"project": m.Project, "modules": m.Modules, "types": m.Types}, nil
		})

	add(s, "asset_query",
		"Find content-browser assets by class and/or path via the AssetRegistry (no dumb path walk). e.g. every Blueprint deriving a C++ class.",
		structHandler[assetQueryIn](b, "asset_query", func(in assetQueryIn) map[string]any {
			m := map[string]any{}
			if len(in.ClassPaths) > 0 {
				m["class_paths"] = in.ClassPaths
			}
			if len(in.PackagePaths) > 0 {
				m["package_paths"] = in.PackagePaths
			}
			if in.Blueprints {
				m["blueprints"] = true
			}
			if in.Recursive != nil {
				m["recursive"] = *in.Recursive
			}
			if in.RecursiveClasses != nil {
				m["recursive_classes"] = *in.RecursiveClasses
			}
			if in.Limit > 0 {
				m["limit"] = in.Limit
			}
			return m
		}))

	add(s, "asset_deps",
		"List an asset's hard dependencies and referencers (what it needs / what needs it).",
		structHandler[assetIn](b, "asset_deps", func(in assetIn) map[string]any {
			return map[string]any{"asset": in.Asset}
		}))

	add(s, "asset_tags",
		"Read an asset's registry tags — Blueprint lineage (ParentClass/NativeParentClass/GeneratedClass) WITHOUT loading it.",
		structHandler[assetIn](b, "asset_tags", func(in assetIn) map[string]any {
			return map[string]any{"asset": in.Asset}
		}))

	add(s, "reflect_class",
		"Reflect a CLASS contract: its CDO default property values, callables, and parent — the class an agent is trying to learn (vs reflect_object which reads a live instance).",
		structHandler[reflectClassIn](b, "reflect_class", func(in reflectClassIn) map[string]any {
			m := map[string]any{"class_path": in.ClassPath}
			if len(in.Include) > 0 {
				m["include"] = in.Include
			}
			if len(in.Exclude) > 0 {
				m["exclude"] = in.Exclude
			}
			if len(in.Properties) > 0 {
				m["properties"] = in.Properties
			}
			if in.MaxProps > 0 {
				m["max_props"] = in.MaxProps
			}
			return m
		}))

	add(s, "enum_values",
		"List a UENUM(BlueprintType) or UserDefinedEnum's enumerators (names + values) — e.g. to write a wait predicate over an enum field.",
		structHandler[enumValuesIn](b, "enum_values", func(in enumValuesIn) map[string]any {
			return map[string]any{"enum_path": in.EnumPath}
		}))

	add(s, "map_gameplay",
		"Discover the gameplay framework: the level's WorldSettings GameMode override, the project default GameMode, and (in PIE) the live mode/state/controller/pawn classes.",
		structHandler[mapGameplayIn](b, "map_gameplay", func(in mapGameplayIn) map[string]any {
			m := map[string]any{}
			if in.Level != "" {
				m["level"] = in.Level
			}
			return m
		}))

	add(s, "find_actors",
		"Find live actors by class with an optional where-filter and selected reflected props (find a runtime-spawned actor by class+property).",
		structHandler[findActorsIn](b, "find_actors", func(in findActorsIn) map[string]any {
			m := map[string]any{}
			if in.ClassPath != "" {
				m["class_path"] = in.ClassPath
			}
			if len(in.Where) > 0 {
				m["where"] = in.Where
			}
			if len(in.Reflect) > 0 {
				m["reflect"] = in.Reflect
			}
			if in.World != "" {
				m["world"] = in.World
			}
			if in.Limit > 0 {
				m["limit"] = in.Limit
			}
			return m
		}))
}
