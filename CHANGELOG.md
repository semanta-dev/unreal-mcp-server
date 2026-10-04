# Changelog

## Unreleased (v2.1.0)

Remediation plan ([`docs/plans/REMEDIATION_PLAN.md`](docs/plans/REMEDIATION_PLAN.md)).

### Behaviour changes
- `scene`, `scene_clear` and `world_query` moved from core to the new optional toolset **`world`** (the move
  freed ~4.3 KB of core tools/list for the plan's additions; `TestToolListBudgets` pins the counts). Enable it with `toolsets op=enable toolset=world`, a
  project's `.umcp.json` `"toolsets": ["world"]`, or `-toolsets world`; calling a moved tool without it returns
  `PRECONDITION` with that hint.
- `playtest op=run`: a crash or a failed setup step / beat now **fails** the run even when the rubric passes (the
  scenario did not play as written); `beat_errors: "warn"` downgrades beat failures to `WARN`. `verdict_reasons` says
  why the verdict is worse than the rubric's, which is still reported unchanged under `rubric`.
- The recorder's per-frame `perf` block (`fps`, `frame_ms`, `hitch_ms`) is gone: it timed the recorder's own tick,
  which its screenshots slow down, not the game. It is now `recorder.tick_ms` / `recorder.max_tick_ms`, and a rubric
  check on `perf.*` — or on `recorder.*` without `"allow_perturbed": true` — is a scenario error (`playtest`,
  `analyze op=rubric`). Measure the game's frame rate with a CsvProfiler capture and `analyze op=perf`.
- `design_audit` refuses missing evidence: an empty input (no points, events, frames, actors…) is `PRECONDITION` with
  `details.reason: insufficient_evidence` and `details.missing`, never a clean report. Each kind names its evidence
  (description and `evidence` in the result). `luminance` refuses frames from a `game_scene` capture (the capture's
  own exposure) unless `source_exposure` states it.
- `design_explore` says what it is: an abstract wave-defense model, not the project's game.

### Added
- **`undo`** (core, destructive): `op=undo|redo` steps the editor's undo buffer only when the next step is the
  server's own edit (titled `MCP: …`); a human's edit on top is `CONFLICT` with its title and nothing changes; refused
  during PIE. The title check and the step are one plugin call. Edits that make no undo step (`asset_create`,
  `asset_edit`, `asset_import`, `widget_edit`, `python`, `console`, `level op=set_world_gamemode`) say
  `undoable: false`, and after one `undo` is `CONFLICT` (it would revert an older edit underneath) — roll back with
  `snapshot_restore` or `git_revert`.
- **Plugin API handshake**: the UnrealMCP plugin reports its API version (`UMCPCoreLibrary::GetPluginApiVersion`, now
  3); `editor op=health` returns `plugin_api` and takes `expect_plugin`; ops that need a newer plugin fail with
  `PRECONDITION` (`editor_code: PLUGIN_MISSING`, `details.needed`/`have`). API 3 also adds `IsPureOrConst`,
  `PeekUndoTitle`/`PeekRedoTitle`, `UndoIfTitled`/`RedoIfTitled` and `FindGameSubsystem` (UE 5.7's Python has none of
  these — [`docs/plans/REMEDIATION_SPIKES.md`](docs/plans/REMEDIATION_SPIKES.md)).
- **Plugin API 4**: UMG authoring works on UE 5.7 again — `asset_create kind=widget_blueprint` and `widget_edit
  op=compose` failed there ("Failed to find property 'widget_tree'": 5.7 hides the widget tree from Python); the plugin
  now hands the companion the tree and its root, registers each new widget (no "did not get a GUID" ensure) and sets
  `is_variable`. Also: `UMCPHUDWidget` binding sources `GameState` and `Subsystem`, class-default read/write as JSON,
  `MountWidget`/`UnmountWidget` and `DescribeLiveWidgets` in PIE. Text props use `unreal.Text(...)`.
- `headless op=tests` counted no results on UE 5.7, which prints `Result={Success}` / `{Fail}`; both spellings are
  parsed now (and `NotRun`/`Skipped` are reported as `skipped`).
- `widget_edit op=compose` reports an unknown `slot` key (`SLOT_KEY_UNKNOWN`) instead of ignoring it; prune unregisters
  the widgets it removes, and so does a spec root that replaces the existing root (the old root's GUID made 5.7 ensure
  on every compile).
- **The game's own API** (toolset `game`, on when the project's `.umcp.json` declares `game_api` — stdio startup and
  daemon attach): `game` (read-only: `capabilities`, `snapshot`, `events since=<cursor>`; each function must be
  `BlueprintPure`/`const`) and `game_command` (Exec; `request_id` required — a re-send returns the recorded result and
  never runs twice — a re-send goes to the world it was first sent to, so a restarted world refuses it as
  `dedup_expired`, and that `request_id` cannot be re-sent; a game that reports no `world_epoch` gets no command). `game_api` is versioned and strict, its object must be a subsystem in one of the project's own modules, and
  an invalid declaration disables only `game` (`toolsets op=list` says why; `gate_policy` still applies). Under
  `gate_policy: require`, `game_command` is refused. Contract: [`docs/plans/GAME_CONTRACT.md`](docs/plans/GAME_CONTRACT.md).
- Object references: `actor_query`, `actor_call`, `reflect` and predicates take `@gameinstance`, `@playerstate[:n]`,
  `@hud` (PIE) and `@subsystem:<Class>` (World / GameInstance / LocalPlayer subsystems; editor and engine subsystems
  only from `reflect`). `actor_call parse=json` decodes a JSON-string return.
- Predicates (`pie_wait`, playtest `wait_until` beats; `actor_call until` reads only its result): `and` / `or` /
  `not` and parentheses; object paths `@ref.prop.Getter().field` (`@subsystem:` takes `Class`, `Module.Class` or
  `/Script/Module.Class`) call only `BlueprintPure`/`const` getters (another is `INVALID_ARGUMENT`); properties need no
  plugin. A bool compares as a bool (`== True` and `== true` both match). `pie_wait timeout_s` goes up to 600 s: past
  `wait_s` it continues as a `job`.
- `actor_call args`: a JSON object for a struct parameter (or an array of them) is built field by field from the
  function's signature — an unknown field or parameter is `INVALID_ARGUMENT` (UE's own conversion silently dropped
  unknown keys: `{"X": 1}` zeroed a Vector); a list is positional fields / array elements.
- `polyworld` is deprecated in favour of `game` / `game_command`
  ([`docs/polyworld-migration.md`](docs/polyworld-migration.md)); it is removed in v2.3.

- **Play like a player** (plugin API 5): `pie op=input action=axis key=MouseX value=… duration_s=…` sends an analog
  axis every game tick (a mouse axis is that frame's delta; a stick or trigger returns to rest the tick after; a hold
  of up to 5 s is waited out and reports `ticks` and `total` — the effect is ticks × value whatever the frame rate;
  `action=release_all` takes no key);
  `pie op=cursor action=move|click|drag position=[x,y] (to=[x,y])` moves, clicks and drags the game's cursor in viewport
  pixels through Slate — UMG and raw-Slate UI alike, with the editor focused or in the background, in a paused game
  too — inside the game viewport only (a position off it, or covered by editor UI, is refused). The OS cursor is
  never moved or captured and the game's input mode is left alone. The game's cursor (what `GetMousePosition` /
  `DeprojectMousePosition` read) stays where the agent put it until `action=release` (from then on it follows the
  real mouse's next move) or PIE ends. `pie op=ui_click
  widget=<name>` clicks the centre of the one visible, enabled live widget with that name (refused when none,
  several, disabled, covered, or nothing takes the click). Game code that reads the hardware cursor itself sees the
  user's mouse.
- `actor_edit op=spawn world=pie` spawns into the running game (plugin API 5; it was `UNSUPPORTED`).
- Playtest beats: `input` (a key/axis, a cursor action, or a widget click) and `game_command` (the game's own command;
  a `request_id` unique per run and beat unless given), and `at_world_s` — beats on the game's clock (paused time does
  not count; a beat the clock never reaches within the window fails, never runs early; the clock is one world's — a
  map travel fails the beat). One clock per scenario; input steps are checked when the scenario is parsed.
- `Needs` may say when it applies: playtest declares `plugin>=5 for input/game_command beats`.

- **Game data and logic authoring** (new toolset `data`, plugin API 6 where Python cannot reach): `data_query` reads a
  DataTable's typed rows, a float curve's keys, and a Blueprint (components — Blueprint and native — variables with
  type, class default and flags, functions, events, and the status + messages of a fresh in-memory compile).
  `data_edit` sets properties on any non-Blueprint asset (per-property errors) or on a settings class (through the
  plugin — many settings classes are invisible to Python; JSON types checked; written to its Default*.ini, which, as
  the Project Settings panel does, rewrites that class's whole section), upserts / deletes DataTable rows by name (never replace-all; row names match in any case; every
  field checked against the table's columns — C++ and Blueprint row structs alike; UE's import ignores unknown fields;
  a failed fill restores the table and leaves no undo step), replaces a curve's keys (all or nothing; a read's keys
  can be written back as they are), adds a Blueprint variable (exact pin types — UE 5.7 silently makes an unknown basic
  type an int; a name the Blueprint or a parent class already uses is refused — UE would rename it), and sets an
  InputAction's keys in a mapping context (creating either when missing; key names checked — the engine maps any
  name). Every edit is saved and a failed save is an error. Property, table and curve edits are one undo step each;
  `add_variable`, `input_mapping` and `settings` say `undoable: false`.

### Fixed
- `world=editor` during PIE found no editor world on UE 5.7 (`get_editor_world()` is None while PIE runs): it is now
  the PIE map's source level.
- A short class name (`AesirAgentSubsystem`, `LevelEditorSubsystem`) resolved only in a fixed list of engine modules;
  any loaded native class now resolves by its short name.

## v2.0.2 — 2026-10-03

- First signed release: every archive and `SHA256SUMS` is signed with Sigstore cosign (keyless, GitHub OIDC) and has
  a build-provenance attestation; builds for windows amd64/arm64, linux amd64 and darwin arm64, plus the UnrealMCP
  plugin source. How to verify: [`docs/operations.md`](docs/operations.md#releases).
- CI: reusable workflow (the release runs it first), Windows `go test`, generated-docs check, the tool-selection
  eval's dry run, release packaging on every push; actions pinned to commit SHAs.
- Fixed: the import-DAG test had no rule for `cmd/tooleval` (it failed in v2.0.1); two companion tests wrote to a
  Windows-only path.

## v2.0.1 — 2026-10-03

- Tool-selection eval (plan §3.5) run: v2 matches v1 on first-call accuracy and completes every task end to end
  ([`docs/validation/tooleval/`](docs/validation/tooleval/README.md)); `cmd/tooleval` harness.
- `toolsets` describes every optional toolset's tools; `python` points at dedicated tools first.
- `analyze`, `playtest` and `design_audit` resolve project-relative file paths against the project.

## v2.0.0 — 2026-10-03

A ground-up overhaul (plan: [`docs/plans/OVERHAUL_PLAN.md`](docs/plans/OVERHAUL_PLAN.md); phase-by-phase evidence:
[`docs/plans/OVERHAUL_PROGRESS.md`](docs/plans/OVERHAUL_PROGRESS.md)). **Breaking**: tool names and arguments changed;
see [`docs/migration-v2.md`](docs/migration-v2.md) for all 155 v1 names.

### Tool surface
- 155 v1 tools → **45** (36 core + optional toolsets `daemon`, `headless`, `design`, `ui`, `desktop`, `polyworld`),
  generated from one spec table; core tools/list ≤ 45 KB (was ~100 KB).
- One argument vocabulary: `op`, `world` (`editor|pie|auto`, explicit for edits), `actor` (label, object path,
  `@gamestate`, `@pawn`, `@controller`), `class` (paths or short names), `[x, y, z]` vectors.
- One result/error contract: objects only; `{"error": {code, message, hint, retryable, outcome, details}}` with a closed
  code set and `outcome: unknown` for mutating calls that may have run.
- Per-op safety tiers drive MCP annotations and the approval gate; `gate_policy: "require"` fails closed (gated ops
  are refused) until the cockpit approval UI is wired to tool calls. `toolsets op=describe` reports each op's tier and
  needs. `-toolsets` / `UMCP_TOOLSETS` and `.umcp.json` toolsets apply at stdio startup.
- 16 advertised-but-unimplemented HUD tools removed.

### New capabilities
- `actor_edit` transform in PIE and set_properties in the editor; editor edits are undoable.
- Snapshots keyed by object path, World Partition aware, with `snapshot_restore` as one undo step.
- `git op=checkpoint` tags `umcp/cp/<n>`; `git_revert` restores files to a checkpoint, closing and relaunching the
  editor safely when the assets are loaded, all-or-nothing.
- Safe editor shutdown (dirty-set check, PIE stop, re-check, graceful quit, reported kill fallback) for restarts,
  full builds and reverts; `-AutoDeclinePackageRecovery` on every launch.
- One playtest orchestration (`playtest op=run`, async) with collected beat errors and crash diagnosis; job results
  can carry images.
- Async jobs with `wait_s` and streamed progress, owned by the project.

### Fixed hazards (v1 behaviour)
- `apply_level_recipe` wiped the level by default (`clean_slate` now defaults false).
- `*_create` could hit Unreal's interactive overwrite prompt; `asset_create` returns CONFLICT, `op=replace` validates
  every input before deleting.
- `scene_apply` overwrote any actor whose label matched; scenes now match only their own tagged actors, and pruning is
  a separate destructive op.
- Actor lookups silently used the first label match; ambiguity is now CONFLICT with the candidates.
- `world` values were silently coerced; unknown worlds are errors. `company_*` fell back to the editor level when PIE
  was off; PolyWorld ops are PIE-only.
- Output paths (screenshots, captures, renders, audio) were caller-controlled; they are server-owned.
- The cockpit's access token was returned to the agent; it never is.
- A non-idempotent op could run twice after a reconnect; dispatch is exactly-once with `outcome: unknown`.

### Live validation (P7) — defects only a real editor exposed, fixed
See [`docs/validation/T4-2026-10-03.md`](docs/validation/T4-2026-10-03.md).
- After any timed-out command the server could never reconnect until restarted (the editor ignored `open_connection`
  while it still believed the old channel was up); the client now sends `close_connection` first.
- A rebuilt server kept running the previous companion in a live editor when the version number was unchanged; the
  install sentinel now includes a source digest.
- `pie op=start` froze the editor (and every later call) behind Unreal's modal "Blueprint Compilation Errors" dialog:
  a plugin pre-flight refuses with the Blueprint list (or `ignore_blueprint_errors=true` plays anyway), and a modal
  guard cancels that dialog; on timeout it lists the editor's other windows without touching them.
- `build` ran UBT beside an editor that was running but not answering (locked DLLs); it now refuses (Windows).
- `git op=checkpoint` failed when `.gitignore` already ignored `Saved/`/`Intermediate/`/`DerivedDataCache/`.
- A class-filtered snapshot diff reported a World Partition actor in an unloaded cell as removed.
- Daemon: a cold start longer than the attach call was cancelled and its editor killed (now detached, attach is
  retryable); attaching a project already open in another editor launched a second one and bound the wrong editor
  (now refused on Windows, and spawns bind only the editor they launched); during a lease restart the session lost its
  project and jobs.
- A stdio server with `-project` could bind another project's editor while its own relaunched (after a build); node
  selection is now strict whenever a project is set, and matches on whole path segments: an editor advertising a
  different project root (another checkout, worktree, `subst` or junction path) is not bound — point `-project` at
  the directory the editor opened.
- Daemon gate fixes: a session that ended mid cold start no longer keeps the editor leased forever; abandoned starts
  are adopted by the next session; cold starts and restart relaunches of one project are serialized (and give up
  with their context or daemon shutdown); restarts refuse overlapping build/revert/lifecycle/headless work.
- `pie op=start` runs the Blueprint pre-flight as its own call before requesting PIE; when it uses most of the call,
  the result is `pie: "requested"` rather than a timeout.
- Also: PIE transforms of Static actors, asset search (`ARFilter`), registry tags by package path (and during PIE),
  HighResShots written to absolute paths, stale PIE screenshots, the `game_scene` capture enum, thumbnail lighting,
  widget render class resolution, PIE console commands, null op arguments in playtest beats, garbage collection after
  the PIE pre-flight compiles.

### Architecture & reliability
- 48 → 29 top-level packages (plan target 28) with an enforced import DAG; one supervisor for stdio and daemon; per-session servers in the
  daemon with lease adoption and draining.
- The companion runs in its own namespace (`__main__._mcp2`) alongside a v1 companion; rollback is a binary swap.
- Tests: in-memory e2e over both dispatch backends, every (tool, op) exercised against an op emulator, the real binary
  over stdio and HTTP, companion contract tests in CPython, ≥ 14 fault scenarios, merged coverage gates in CI.
- Generated docs (`docs/tools.md`, `docs/migration-v2.md`) checked for drift in CI.

## v1 — tag `v1-final`

The Go rewrite of the original Python server: 155 tools over Unreal's Python remote execution, the multi-project
daemon, the MCP Cockpit plugin, playtest capture and design audits.
