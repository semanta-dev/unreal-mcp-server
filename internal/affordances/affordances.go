// Package affordances is a static capability manifest: for each notable tool, what
// it REQUIRES (an editor? a live PIE session? a built navmesh? the C++ plugin?) and
// whether it mutates state. An autonomous agent reads this to PLAN — e.g. "run
// project_map offline first, build a navmesh before world_query, don't call
// pie_verify outside PIE" — instead of discovering constraints by failing. Kept
// deliberately curated (the non-obvious constraints), not auto-generated, so it
// stays a decision aid rather than noise.
package affordances

import "sort"

// Affordance is one tool's requirements and effects.
type Affordance struct {
	Tool         string `json:"tool"`
	Phase        string `json:"phase"`
	Summary      string `json:"summary"`
	Offline      bool   `json:"offline"`       // runs without a live editor (pure Go / file ops)
	NeedsEditor  bool   `json:"needs_editor"`  // needs a reachable editor
	NeedsPIE     bool   `json:"needs_pie"`     // needs a running play-in-editor session
	NeedsNavmesh bool   `json:"needs_navmesh"` // needs a built NavMeshBoundsVolume
	NeedsPlugin  bool   `json:"needs_plugin"`  // needs the UnrealMCP C++ plugin
	Mutates      bool   `json:"mutates"`       // changes project/level/asset state
}

var registry = []Affordance{
	// P2 discovery
	{Tool: "project_map", Phase: "P2", Offline: true, Summary: "parse C++ types/modules from source; resolve /Script paths offline"},
	{Tool: "asset_query", Phase: "P2", NeedsEditor: true, Summary: "AssetRegistry query; blueprints=true finds BPs deriving a class"},
	{Tool: "asset_deps", Phase: "P2", NeedsEditor: true, Summary: "asset dependencies/referencers"},
	{Tool: "asset_tags", Phase: "P2", NeedsEditor: true, Summary: "asset registry tags (BP lineage) without loading"},
	{Tool: "reflect_class", Phase: "P2", NeedsEditor: true, Summary: "CDO defaults + parent hint"},
	{Tool: "enum_values", Phase: "P2", NeedsEditor: true, Summary: "UENUM/UserDefinedEnum enumerators"},
	{Tool: "map_gameplay", Phase: "P2", NeedsEditor: true, Summary: "GameMode/State/Pawn wiring (live in PIE)"},
	{Tool: "find_actors", Phase: "P2", NeedsEditor: true, Summary: "actors by class + where-filter"},

	// P3 authoring (offline .ini editors vs editor asset authoring)
	{Tool: "set_gamemode", Phase: "P3", Offline: true, Mutates: true, Summary: "DefaultEngine.ini default GameMode"},
	{Tool: "input_action", Phase: "P3", Offline: true, Mutates: true, Summary: "DefaultInput.ini action mapping"},
	{Tool: "input_axis", Phase: "P3", Offline: true, Mutates: true, Summary: "DefaultInput.ini axis mapping"},
	{Tool: "gameplay_tag_add", Phase: "P3", Offline: true, Mutates: true, Summary: "DefaultGameplayTags.ini tag"},
	{Tool: "blueprint_create", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "create BP from a parent class"},
	{Tool: "blueprint_set_defaults", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "set CDO defaults (asset paths auto-resolve)"},
	{Tool: "assign_subclass", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "set a TSubclassOf property"},
	{Tool: "blueprint_add_component", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "add a component via SubobjectDataSubsystem"},
	{Tool: "datatable_create", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "DataTable backed by a row struct"},
	{Tool: "datatable_import", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "populate a DataTable from JSON/CSV"},
	{Tool: "set_world_gamemode", Phase: "P3", NeedsEditor: true, Mutates: true, Summary: "level WorldSettings GameMode override"},
	{Tool: "pie_set_property", Phase: "P3", NeedsPIE: true, Mutates: true, Summary: "set a property on a live actor"},
	{Tool: "pie_destroy", Phase: "P3", NeedsPIE: true, Mutates: true, Summary: "destroy a live actor"},

	// P4 verification
	{Tool: "world_query", Phase: "P4", NeedsPIE: true, NeedsNavmesh: true, Summary: "nav_path/project_point need a navmesh; line_trace/overlap need collision"},
	{Tool: "perf_parse", Phase: "P4", Offline: true, Summary: "parse a CsvProfiler CSV / memreport offline"},
	{Tool: "scenario_run", Phase: "P4", NeedsEditor: true, NeedsPIE: true, Mutates: true, Summary: "run a saved scenario (arrange/act/assert)"},
	{Tool: "scenario_list", Phase: "P4", Offline: true, Summary: "list saved scenarios"},

	// PW instanced content
	{Tool: "instances_count", Phase: "PW", NeedsEditor: true, Summary: "HISM/ISM instance counts (blind spot of list_actors)"},
	{Tool: "instances_list", Phase: "PW", NeedsEditor: true, Summary: "HISM/ISM instance transforms"},
	{Tool: "scene_digest", Phase: "PW", NeedsEditor: true, Summary: "deterministic content hash (instances or actors)"},
	{Tool: "pie_verify", Phase: "PW", NeedsPIE: true, Summary: "poll a JSON getter until a predicate holds"},
	{Tool: "image_compare", Phase: "PW", Offline: true, Summary: "perceptual image compare / golden gate"},

	// P5 robustness
	{Tool: "editor_events", Phase: "P5", Offline: true, Summary: "tail the event stream (works while the channel is busy)"},
	{Tool: "editor_ping", Phase: "P5", NeedsEditor: true, Summary: "cheap liveness + bridge version"},
	{Tool: "scene_snapshot", Phase: "P5", NeedsEditor: true, Summary: "save actor transforms to undo an experiment"},
	{Tool: "scene_restore", Phase: "P5", NeedsEditor: true, Mutates: true, Summary: "restore actor transforms from a snapshot"},
	{Tool: "health_check", Phase: "P5", NeedsEditor: true, Summary: "post-rebuild gate: reachable + version + no crash"},

	// P6 headless / fan-out (spawns its OWN editor-cmd process — needs no LIVE
	// interactive editor, but is not pure-Go, so not marked Offline).
	{Tool: "headless_run", Phase: "P6", Summary: "run a commandlet/automation batch in a separate editor-cmd process (off the live channel)"},
	{Tool: "affordances", Phase: "P6", Offline: true, Summary: "this manifest — plan against tool requirements"},

	// P7 plugin-gated
	{Tool: "capture (include_ui)", Phase: "P7", NeedsPIE: true, NeedsPlugin: true, Summary: "Slate UI/HUD capture needs the MCPCapture plugin"},
}

// Registry returns the full manifest, sorted by tool name.
func Registry() []Affordance {
	out := append([]Affordance(nil), registry...)
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// ByTool returns one tool's affordance.
func ByTool(name string) (Affordance, bool) {
	for _, a := range registry {
		if a.Tool == name {
			return a, true
		}
	}
	return Affordance{}, false
}

// Offline returns the tools that need no live editor (safe to run anytime).
func Offline() []Affordance {
	var out []Affordance
	for _, a := range registry {
		if a.Offline {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}
