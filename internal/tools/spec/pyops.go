package spec

// PyOp classifies one companion-module op (internal/bridge/py, `_OPS`). This table
// replaces the v1 manifest (internal/manifest) and is kept in bijection with
// bridge.CompanionOps() by TestPyOpsBijection — an unclassified op fails the build.
//
// Tiers were assigned by reading every op body (sweep recorded in
// docs/plans/OVERHAUL_PROGRESS.md §P3a). Policy calls, applied consistently:
//   - PIE game-world state is session state → Ephemeral (pie_set_property, pie_destroy,
//     pie_input, start/stop_play); it is gone when PIE stops.
//   - Saving dirty packages is a write, not a loss → Mutating (open_level, save_all).
//   - Ops whose world defaults to auto (company_*) are graded for the worst case: with
//     PIE off they act on the editor level.
type PyOp struct {
	Tier        Tier
	Escalations []Escalation
	Note        string
	// Plugin is the UnrealMCP plugin API the op needs (its _need_plugin(N) call); a tool
	// op reaching it must declare Needs "plugin>=N" (or higher). 0 = none.
	Plugin int
}

// Escalation raises an op's tier when an argument's EFFECTIVE value (after the op's
// own default) is truthy — so a flag that defaults on escalates by default.
type Escalation struct {
	Arg     string
	Default any // the op's default for Arg (checked against the Python source)
	Tier    Tier
}

// PyOps is the classification of every companion op.
var PyOps = map[string]PyOp{
	"actor_call":           {Tier: Exec, Note: "calls a caller-named UFUNCTION (PIE)"},
	"actor_delete":         {Tier: Destructive},
	"editor_undo":          {Tier: Destructive, Plugin: 3, Note: "undo/redo the next editor transaction, only if the server made it"},
	"note_edit":            {Tier: Ephemeral, Note: "records an untracked server edit in the companion's undo journal"},
	"actor_query":          {Tier: ReadOnly},
	"actor_set_properties": {Tier: Mutating},
	"actor_spawn":          {Tier: Mutating},
	"actor_transform":      {Tier: Mutating},
	"asset_create": {Tier: Mutating, Note: "an existing dest is a CONFLICT unless replace (deletes it first)",
		Escalations: []Escalation{{Arg: "replace", Default: false, Tier: Destructive}}},
	"asset_edit": {Tier: Mutating, Note: "Blueprint CDO edits; compiles and saves"},
	"apply_level_recipe": {Tier: Exec, Note: "exec()s a caller-supplied script file",
		Escalations: []Escalation{{Arg: "clean_slate", Default: false, Tier: Destructive}}},
	"asset_deps":          {Tier: ReadOnly},
	"asset_info":          {Tier: ReadOnly},
	"asset_query":         {Tier: ReadOnly},
	"asset_reimport":      {Tier: Destructive, Note: "overwrites the asset from its source file"},
	"asset_tags":          {Tier: ReadOnly},
	"asset_thumbnail":     {Tier: Ephemeral, Note: "writes Saved/MCP/AssetThumbs; transient capture actors may dirty the level (reported as dirtied)"},
	"audio_capture_start": {Tier: Ephemeral},
	"audio_capture_stop":  {Tier: Ephemeral, Note: "writes the capture to out_dir (unchecked path)"},
	"capture_list":        {Tier: ReadOnly},
	"capture_poll":        {Tier: ReadOnly},
	"capture_poses":       {Tier: Ephemeral},
	"capture_start":       {Tier: Ephemeral},
	"capture_stop":        {Tier: Ephemeral},
	"cockpit_info":        {Tier: ReadOnly, Note: "returns the cockpit session token — never surface to the agent"},
	"company_build":       {Tier: Mutating},
	"company_demolish":    {Tier: Destructive, Note: "world=auto falls back to the editor level when PIE is off"},
	"company_road":        {Tier: Mutating},
	"company_select":      {Tier: Mutating},
	"company_status":      {Tier: ReadOnly},
	"console":             {Tier: Exec},
	"datatable_import": {Tier: Mutating,
		Escalations: []Escalation{{Arg: "json", Default: nil, Tier: Destructive}, {Arg: "csv", Default: nil, Tier: Destructive}},
		Note:        "fill_data_table_from_*_string replaces every existing row"},
	"design_probe":        {Tier: ReadOnly},
	"editor_ping":         {Tier: ReadOnly},
	"editor_status":       {Tier: ReadOnly},
	"focus_actors":        {Tier: Ephemeral},
	"get_selection":       {Tier: ReadOnly},
	"import_assets":       {Tier: Destructive, Note: "replace_existing=True and save=True are hard-coded"},
	"instances_count":     {Tier: ReadOnly},
	"instances_list":      {Tier: ReadOnly},
	"list_assets":         {Tier: ReadOnly},
	"live_coding_compile": {Tier: Mutating, Note: "fixed command; compiles and hot-patches project C++"},
	"map_gameplay":        {Tier: ReadOnly},
	"open_level":          {Tier: Mutating, Note: "saves all dirty packages before loading"},
	"pie_exec":            {Tier: Exec, Note: "playtest beats; calls a caller-named UFUNCTION"},
	"pie_input":           {Tier: Ephemeral},
	"pie_observe":         {Tier: ReadOnly},
	"pie_screenshot":      {Tier: Ephemeral},
	"pie_set_property":    {Tier: Ephemeral},
	"play_test_sound":     {Tier: Ephemeral},
	"reflect":             {Tier: ReadOnly},
	"save_all":            {Tier: Mutating},
	"scene_apply":         {Tier: Mutating, Note: "tag-scoped (mcp_scene:<id>); never deletes (prune is scene_prune)"},
	"scene_bounds":        {Tier: ReadOnly},
	"scene_actors":        {Tier: ReadOnly},
	"scene_prune":         {Tier: Destructive, Note: "destroys this scene's tagged actors absent from the spec"},
	"snapshot_actors":     {Tier: ReadOnly},
	"snapshot_restore":    {Tier: Mutating, Note: "moves existing actors back (one undo step) and saves map packages"},
	"pie_preflight":       {Tier: Ephemeral}, // compiles dirty Blueprints in memory; saves nothing
	"pie_start":           {Tier: Ephemeral},
	"packages_state":      {Tier: ReadOnly},
	"quit_editor":         {Tier: Mutating, Note: "graceful editor exit; refuses (PRECONDITION) while anything is unsaved, so nothing is lost"},
	"pie_stop":            {Tier: Ephemeral},
	"scene_clear":         {Tier: Destructive},
	"select_actors":       {Tier: Ephemeral},
	"set_world_gamemode":  {Tier: Mutating},
	"take_screenshot":     {Tier: Ephemeral},
	"viewport_get":        {Tier: ReadOnly},
	"viewport_set":        {Tier: Ephemeral, Note: "camera/pilot/game view only (console moved to the console tool)"},
	"widget_compile":      {Tier: Mutating},
	"widget_compose": {Tier: Mutating,
		Escalations: []Escalation{{Arg: "prune", Default: false, Tier: Destructive}, {Arg: "remove", Default: nil, Tier: Destructive}},
		Note:        "a spec root that differs from the current root also orphans the existing tree"},
	"widget_describe": {Tier: ReadOnly},
	"widget_render":   {Tier: Ephemeral, Note: "writes a PNG under Saved/MCP/WidgetRenders"},
	"widget_tree":     {Tier: ReadOnly},
	"world_query":     {Tier: ReadOnly},
}

// WorstTier is the highest tier an op can reach with any arguments.
func (p PyOp) WorstTier() Tier {
	t := p.Tier
	for _, e := range p.Escalations {
		if e.Tier > t {
			t = e.Tier
		}
	}
	return t
}

// EffectiveTier is the op's tier for a concrete argument set.
func (p PyOp) EffectiveTier(args map[string]any) Tier {
	t := p.Tier
	for _, e := range p.Escalations {
		v, ok := args[e.Arg]
		if !ok {
			v = e.Default
		}
		if truthy(v) && e.Tier > t {
			t = e.Tier
		}
	}
	return t
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}
