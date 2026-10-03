# P3a — companion op tier sweep

Every `_OPS` entry (81) was read, helper calls followed, and graded on the v2 5-tier model
(plan §2.1). The result is encoded in `internal/tools/spec/pyops.go` (tier + argument
escalations evaluated on *effective* values, i.e. after the op's own defaults). This file
records the reasoning and the hazards found along the way.

## Policy calls
- **PIE game-world state is session state → Ephemeral** (`pie_set_property`, `pie_destroy`,
  `pie_input`, `start_play`, `stop_play`, `play_test_sound`): gone when PIE stops.
- **Saving dirty packages is a write, not a loss → Mutating** (`open_level`, `save_all`,
  `set_world_gamemode`, `scene_restore`).
- **`world=auto` ops are graded for the worst case**: `company_*` fall back to the editor
  level when PIE is off, so `company_demolish` is Destructive and build/road/select Mutating.
- **`live_coding_compile` → Mutating**: the command is fixed (not caller-supplied); it compiles
  whatever C++ is on disk. (`build` is Mutating in the v2 surface as well.)
- **`datatable_import` → Mutating base, Destructive when `json`/`csv` is given**:
  `fill_data_table_from_*_string` replaces every existing row.

## Changes vs the v1 manifest (internal/manifest, now deleted)
| op | v1 manifest | v2 | why |
|---|---|---|---|
| asset_reimport | scc | Destructive | overwrites the asset from its source file |
| import_assets | scc | Destructive | `replace_existing=True`, `save=True` hard-coded |
| save_all, widget_compile | scc | Mutating | write-only; no SCC tier in v2 |
| pie_destroy, stop_play | destructive | Ephemeral | PIE-only state |
| pie_set_property, pie_input, start_play, play_test_sound | mutating | Ephemeral | PIE/session state |
| focus_actors, select_actors | mutating | Ephemeral | viewport/selection UI state |
| viewport_set | exec | Ephemeral + `console`→Exec escalation | base is UI state; console strings are Exec |
| asset_thumbnail, take_screenshot, pie_screenshot, capture_*, audio_capture_*, widget_render, level_snapshot | readonly | Ephemeral | write server-owned scratch files and/or spawn transient actors that dirty the level |
| scene_apply | mutating | Mutating + `prune`→Destructive | prune destroys tagged actors not in the plan |
| widget_compose | mutating | Mutating + `prune`/`remove`→Destructive | removes widgets; a changed root orphans the tree |
| datatable_import | mutating | Mutating + `json`/`csv`→Destructive | replaces all rows |
| apply_level_recipe | exec | Exec (+ `clean_slate` default **true** → Destructive) | wipes every non-WorldSettings actor by default |

## Hazards found (scheduled)
| Hazard | Where | Fix scheduled |
|---|---|---|
| `apply_level_recipe` wipes the level by default (`clean_slate` defaults true) | 30_level_assets.py:54,58,24 | P5a: becomes `python recipe`, `clean_slate` default false |
| Unsanitized `session` / `filename` / `out_path` → writes outside `Saved/` | capture_start/poses, take_screenshot, pie_screenshot, audio_capture_stop, widget_render | P5c: confine all output paths under `Saved/MCP/` (reject `..`/absolute), T1 + pytest |
| `cockpit_info` returns the cockpit session token | 99_dispatch / cockpit probe | internal op only — never exposed as a tool result (P5e `toolsets describe` returns URL/status only) |
| `*_create` ops with an existing `dest` may hit UE's replace prompt (unverified unattended behaviour) | blueprint/dataasset/datatable/material/widget create | P5b: check existence first → `CONFLICT` unless `overwrite:true` (Destructive escalation) |
| `scene_apply` overwrites transform/mesh/material of any user actor whose label collides with a spec label | 60_scene.py | P5c: `scene apply` matches by `mcp_scene:<id>` tag, never by bare label |
| `world=auto` on `company_*` acts on the editor level when PIE is off | 30_level_assets.py:316 | P5e: `polyworld` ops fixed to `world: pie` (PIE_NOT_RUNNING otherwise) |
| transient capture actors dirty the editor level (affects the git_revert dirty check) | thumbnail/screenshot/capture | P5c: capture helpers clear the dirty flag they caused, or record it in the result |
