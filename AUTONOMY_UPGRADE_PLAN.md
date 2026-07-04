# Autonomy Upgrade Plan — Making the Unreal MCP Server Excel at Autonomous Game Development

Produced by a multi-agent game-developer teardown (5 attack dimensions → 3 UE-reality critiques
→ synthesis), then extended with the concrete gaps a **second MCP-driven project (poly-world)** has
already hit and documented. Extends `GO_REWRITE_PLAN.md` (v0–v6) and `PLAYTEST_UPGRADE_PLAN.md` (v7).

**North star:** an LLM agent autonomously **designs → authors (C++ AND Blueprint/UMG/input/data) →
builds → plays → verifies → self-corrects**, in a loop, at scale, on a real UE 5.7 game — with
minimal human intervention.

**Teardown grades (v7, harsh, against the north star):**

| Dimension | Grade |
|---|---|
| Gameplay authoring reach (BP/framework/input/UMG/data, not just C++ + level dressing) | **D** |
| Autonomous verification & self-correction | **D** |
| Engine/project discovery & world model (cold-start orientation) | **D** |
| Architecture, throughput & robustness under autonomous load | **C** |
| End-to-end autonomous loop (ranged-caster vertical slice) | **D** |

## 1. State of the server — brutally honest

**Genuinely excellent (do not rebuild):** (1) the `build_compile` C++ compile-fix loop (livecoding-vs-full
classification, structured MSVC/UHT/Clang/Linker diagnostics) — the C++ authoring loop *closes today*;
(2) the v7 slate-tick capture model (buffer to disk, collect in one `capture_stop`) and `playtest_capture`
orchestration; (3) the transport (single-flight taint/reconnect, generation-gated reinstall, injection-safe
dispatch, `scene_apply` as a real transaction); plus reflection-driven observe and the temporal rubric.

**The five structural walls blocking a closed loop:** measured against the north star the server is
"a human-shaped C++ workflow with a great compiler and a screenshot montage bolted on." It closes the
loop for exactly one case — a pure-C++ edit whose success shows up in a gamestate field, a class-count,
or the log — and breaks everywhere else:

1. **It can't author anything but C++ and level dressing.** Zero gameplay-content-creation ops: no Blueprint
   create/compose/defaults, no framework wiring, no input assets, no UMG, no DataTables. `execute_python`
   is an unschema'd, unverified minefield.
2. **It can't play the game.** No input-synthesis op anywhere — in possessed PIE the agent controls nothing.
3. **It can't discover what to call.** No asset-registry query, no class *contract* (property types/flags/
   replication/enum values/signatures), no gameplay-wiring introspection. `reflect_object` reads instance
   *values*, not the class the agent is trying to learn.
4. **Verification certifies the easy 20%.** No crash diagnosis, no replication testing (aesir is multiplayer),
   no perf-as-data, no spatial queries (LOS/nav), no find-actor-by-class+property. The rubric will PASS a
   12fps, visually-black, un-replicated build. And the whole capstone is still "gated / never run live."
5. **Robustness cliffs under load.** A >120s op taints the channel while the game-thread command runs on with
   no idempotency; failures return raw tracebacks (no machine-branchable code); there's snapshot+diff but **no
   restore**; the only editor→Go signal is the capture recorder (a PIE crash is invisible until a poll times out).

**The companion C++ editor plugin is a must-have — but LAST, not first.** ~70% of the unlock (all discovery,
most authoring, most verification, robustness of dispatchable ops) is pure Python/Go and ships with no engine
C++. The plugin is the hard floor for a specific unreachable set: BP event-graph node wiring, UMG widget-tree
authoring, PIE input synthesis (legacy `BindAxis/BindAction`), net-role PIE-world enumeration, `FProperty`
flags + native class search, and true off-thread/event-push execution. Build the Python/Go value first (P1–P6),
ship the plugin (P7) as the capability + reliability floor.

### poly-world corroboration (a second MCP-driven project, independently)

poly-world (a logistics-city builder, 9 C++ classes, MCP-driven) hit these exact walls and hand-rolled a
verification substrate *outside* the MCP — which both confirms the teardown and adds builder/sim-specific gaps
(see **Workstream PW**). Already-shipped from poly-world: the `build_compile` editor-self-close + orphan-reclaim
robustness fix (the `siblings_*`/`lifecycle` change, deployed in v7d4f2e6).

## 2. Architecture & design rules (every phase)

- **Additive ops only** — new `_op_*` in `mcp_bridge.py` (hot-loaded, one source of truth), Go tool ships
  base64-JSON. Bump `_MCP_BRIDGE_VERSION` per phase.
- **Single-flight-aware** — batch multi-step work into one op (`scene_apply`-style); high-frequency work uses
  the slate-tick recorder. Blocking synchronous ops can't be chunked across ticks (see P5 + P7 for the honest limit).
- **Structured errors everywhere** (P1) — every op returns `{ok, code, message, retryable, hint, traceback?}`.
- **Gated-testable** — every phase ships pure sub-logic (parsers, predicate/filter builders, diff/restore
  planners, image hashing, digest, crash/source-map parsing) as Go unit tests; the live path is gated + smoked
  against aesir/poly-world.
- **No vaporware** — where a 5.7 API's existence is uncertain, ship behind a live-gated probe + documented
  fallback. Corrections already folded in: use `ARFilter.class_paths` (not deprecated `class_names`); the
  "5.7 `DefaultKeyMappings` deprecation" is fabricated — use legacy `DefaultInput.ini`; the crash reader must be
  Go-side (the in-editor bridge dies with the editor); the op_id worker only decouples *chunkable* ops.

## 3. Phases (ordered by autonomy-unlocked-per-effort)

### P1 — Trustworthy verdicts & deterministic self-correction (foundation, low effort)
- **Bridge:** stable error-code enum (`ASSET_NOT_FOUND, CLASS_UNRESOLVED, NOT_IN_PIE, PROPERTY_READONLY,
  SAVE_BLOCKED, COMPILE_ERROR, SPAWN_FAILED, TIMEOUT, …`) on every op; normalize `errors/warnings/missing` to one
  `{code,label,message}` shape. Perf tier-a: the recorder's `delta_seconds` → stamp `fps`/`hitch_ms` per frame (free).
- **Go:** `internal/crash` (Go-side — survives the editor): parse newest `Saved/Crashes/UECC-*/CrashContext.runtime-xml`
  + crash `.log`; logtail scanner for `Assertion failed`/`Fatal error`/`=== Critical error ===` between markers →
  `{file,line,symbol}`. Wire into `playtest_capture` (dropped-channel + dead-PID → crash report, not a bare error).
  Surface `code` in `OpError`. Fix `pie_exec` schema wording (it's `FindFunction`/`ProcessEvent` — can dispatch Server
  RPCs with authority, not "BlueprintCallable only"). New rubric kinds `range fps`, `max hitch_ms`.
- **Live smoke (the capstone validation the project owes):** one `playtest_capture` on `L_Arena` asserting frames>0,
  `WaveState==IN_PROGRESS`, `nonzero counts.EnemyCharacter`, `log_max errors 0`, + a Server-RPC + BlueprintPure probe.
- **Acceptance (aesir):** agent writes a null-deref into `AEnemyCharacter::Tick` → `playtest_capture` → reads
  `EnemyCharacter.cpp:142 access None` + `{code}` → fixes → re-verifies, no human reading the crash window.

### P2 — Discovery / world model (cold-start orientation; pure Python/Go, high leverage)
- **Bridge:** `asset_query` (AssetRegistry `ARFilter` with `class_paths`+`recursive_classes`), `asset_deps`,
  `asset_tags` (BP lineage without loading), `reflect_class` (CDO values + baked signature docstrings; ~70%),
  `enum_values` (BlueprintType enums only — flag plain `UENUM()` as invisible), `map_gameplay` (WorldSettings +
  `GameMapsSettings` CDO + live PIE mode/state/pawn), `find_actors(class, where, reflect)` — and **feed the same
  predicate into `_recorder_observe`** so the timeline/rubric can key on it. Extend `asset_info` for BP parent/CDO/components.
- **Go:** `internal/projectmap` (pure Go — parse `.uproject` + `Build.cs` + headers → `{class → /Script path, header,
  module}`); tools `asset_query/asset_deps/reflect_class/enum_values/map_gameplay/find_actors/project_map`.
- **Acceptance:** from zero knowledge — resolve `L_Arena`'s GameMode/GameState/Pawn/HUD `/Script` paths, find every BP
  deriving `ATurret`, discover `EWaveState` enumerators to write a wait predicate — no source reading.
- **Plugin-only (P7):** native `UCLASS` substring search over `TObjectRange<UClass>`, `FProperty` flags.

### P3 — Structured authoring: the Python-feasible 70% (closes design→author for C++-logic + BP-data)
Sanctioned split: **logic in C++ (`BlueprintCallable`/`BlueprintImplementableEvent`), Blueprints carry data +
component composition** — exactly aesir's architecture.
- **Bridge:** `blueprint_create` (`BlueprintFactory`), `blueprint_add_component` (`SubobjectDataSubsystem` — the fiddly
  `FAddNewSubobjectParams` dance + one `mark_blueprint_as_structurally_modified`), `blueprint_set_defaults` (CDO +
  `compile_blueprint`), `assign_subclass` (`TSubclassOf` slots), `set_world_gamemode` (verify the exact prop name live),
  `datatable_create` + `datatable_import` (`fill_data_table_from_{csv,json}_string` — the reliable path),
  `dataasset_create`, `curve_create` (gate), `widget_blueprint_create` (asset only; tree/binding → P7),
  `pie_spawn/pie_set_property/pie_destroy` (test preconditions without cheat C++).
- **Go:** `internal/blueprint` (compact JSON schemas + mandatory post-write `reflect` verify); `internal/projectconfig`
  (pure Go: `DefaultEngine.ini GlobalDefaultGameMode`, `DefaultInput.ini` Axis/Action mappings — the legacy path aesir
  uses, highest-ROI input authoring, `DefaultGameplayTags.ini`); tools for all of the above; `design_check` invariant
  (resolvable GameMode + PlayerStart before playtest).
- **Corrections:** Enhanced-Input asset authoring is uncertain → ship legacy `DefaultInput.ini` first, gate IA/IMC assets.
  BP member-variable creation + event-graph nodes are C++-only → P7.
- **Acceptance:** "Create `BP_LaserTurret` from `ATurret`, set Range/Damage/FireInterval, assign HeadMesh, wire into the
  GameMode turret slot, place 6 via `scene_apply`" — no hand-written Python. "Define 5 enemy archetypes in `DT_Enemies`."

### P4 — Spatial + behavioral verification & reusable scenarios (closes verify: functional + perf + spatial)
- **Bridge:** `world_query` (BlueprintCallable in 5.7: `line_trace_single`/`sphere_overlap_actors`;
  `NavigationSystemV1.find_path_to_location_synchronously`/`project_point_to_navigation`) — run against the *game* world
  in PIE. Perf tier-b: trigger `CsvProfiler`/`stat startfile`/`memreport`.
- **Iteration-speed knob (SHIPPED EARLY in P1):** `playtest_capture time_dilation` runs `slomo N` (via a new `console`
  op) right after start_play so a slow real-time sim — a bottleneck that takes ~70s at 10Hz — forms in a few real
  seconds; the recorder still samples on the real-time interval. Scenarios inherit it (fast during act/wait, `slomo 1`
  for a final real-time filmstrip segment).
- **Go:** `internal/scenario` (`scenario/v1` on disk: `{setup, act (pie_exec incl. Server RPCs + teleport-aim + pie_spawn),
  assert (rubric)}` + `scenario_run/list/suite_run` — the saved replayable suite is the regression gate); `internal/perf`
  (CSV/`.memreport` → percentiles + memory buckets); `internal/imgdiff` (pure Go: dHash/aHash + mean-luma/black-frame over
  montage frames; baselines under `Saved/MCP/baselines`; `update_baseline`). Rubric kinds `nav_path`, `line_of_sight`,
  `visual`, `p95 frame_ms`.
- **Honest limit:** `call_method` dispatches Server RPCs, but aesir's `StartFire/StopFire` aren't UFUNCTIONs, and sustained
  locomotion/aim needs the input driver → P7. P4 certifies presence + spatial + state-transition + perf + visual + RPC actions.
- **Acceptance:** "Block out an arena via `scene_apply`, assert `nav_path` spawn→core AND turret→spawn LOS" (catches
  AI-trapping walls pre-playtest); "200-enemy stress beat → FAIL: p95 frame_ms=41>20"; two saved scenarios re-run every change.

### P5 — Robustness under autonomous load (keeps the unattended loop from stalling)
- **Bridge:** latency-decoupling for *chunkable* ops (`op_id` → slate-tick worker → `Saved/PyMCP/ops/<id>.json`; Go polls
  cheap `op_status`; completed-op ring for reconnect-after-timeout reconciliation); `editor_ping`; event push via file tail
  (`Saved/PyMCP/events.ndjson` from PIE-end/import/log-error delegates); extend `level_snapshot` to full per-actor state +
  `scene_restore(token)` (idempotent diff re-apply/destroy); deterministic PIE (fixed timestep + seed, best-effort);
  escape-hatch guardrails (dry-run for `scene_apply`/`apply_level_recipe`, `max_actors` abort, save-opt-in-after-verify).
- **Capture ops ergonomics (SHIPPED EARLY):** `capture_clear` (delete a session's frames, or all, from Saved/MCP/capture
  + the mcp_* pie_highres screenshots — no shell-rm) and `read_capture` (view an existing capture dir as a montage;
  decodes frames by content so extensionless files from an external/C++ capture work — no copy-to-.png-then-Read dance).
- **Go:** `events_since(marker)` (pure file tail — works while the command channel is blocked); post-rebuild health gate
  (auto `open_level` the prior map, confirm `editor_state`+version+throttle-off); `scene_restore` tool; dry-run wiring.
- **Honest hard limit:** the op_id worker only decouples ops chunkable across ticks; a single blocking synchronous call
  (nav/lighting build, big import, runaway `execute_python`) monopolizes the one game thread → still wedges. `editor_ping`
  + completed-op ring make it *survivable*; genuine interruption/off-thread needs P7.
- **Acceptance:** checkpoint arena → try layout → FAIL → `scene_restore` snaps back exactly → try next (clean self-correcting
  design loop); a 400-actor `scene_apply` returns an `op_id` immediately; a PIE run aborts the instant `events.ndjson` shows an ensure.

### P6 — Headless / unattended automation + game-affordance registry (iterate at scale, no human editor)
- **Go:** `internal/headless` (`UnrealEditor-Cmd.exe … -ExecCmds="Automation RunTests …; quit" -unattended -nullrhi` +
  a commandlet path that runs a `scenario/v1` headless → JSON verdict); `internal/affordances` (project `.mcp/game_affordances.json`
  — named drive-actions like `advance_wave`, actor selectors, default rubrics, PIE beats; `playtest_capture` accepts an
  affordance name); `internal/editorpool` (make the 1:1 editor:agent invariant explicit — per-agent editor with a unique
  `-command-addr`, `editor_ping` health, recycle wedged editors, bounded pool; namespace all in-editor module state by
  server-instance id).
- **Honest tiering:** input-free reflection/RPC scenarios headless are feasible now; true synthetic-input Gauntlet is Tier-3 (P7).
- **Acceptance:** a scheduled agent runs `build_compile` + `scenario_suite_run` headless overnight (no window, no human),
  posts PASS/FAIL + crash + perf, opens a fix PR.

### P7 — The companion C++ editor-utility plugin (the flagship; the unreachable 30% + reliability floor)
- **Ship story:** a `UnrealMCPEditor` module in the project's **Editor target**, exposing `BlueprintCallable` UFUNCTIONs
  dispatched exactly like today's ops (Go → base64-JSON → `_op_plugin_*`). C++ changes go through the *existing*
  `build_compile` classifier. A **second local channel** (named pipe / second socket) the Go server owns carries event push
  + off-thread op execution, decoupled from the single-flight game-thread channel.
- **Surface (ROI order):** (0) **⭐ PIE-world capture helper (ELEVATED — user-prioritized, pull forward first).** The v7
  `scene_capture` can't spawn its `SceneCapture2D` into the possessed-PIE world (`EditorActorSubsystem` spawns into the
  *editor* world → `NoneType.capture_component2d`), and `pie_highres`/HighResShot renders nothing when backgrounded — which
  is why the live PIE readability filmstrip currently has to be hand-rolled in C++. A tiny `MCPCapture` runtime helper
  (`UGameInstanceSubsystem`/`UBlueprintFunctionLibrary`) spawns + manages a `USceneCaptureComponent2D` **in the game world**
  and exports frames on a timer, driven by `capture_start`/`capture_stop` exactly as today (Go → base64-JSON → `_op_plugin_capture_*`).
  This makes `capture_start source=game_scene` capture the live possessed-PIE sequence + montage directly, **deleting the
  autopilot/hand-rolled capture path and unblocking the editor-side P6 gates**. Smallest, highest-value plugin slice — ships
  ahead of the rest. (1) **PIE input synthesis** — `MCPTestInput` feeding the pawn's `InputComponent` (drives aesir's
  legacy binds; Enhanced-Input injection would NOT reach these) → makes "plays it" true; (2) **BP event-graph nodes** +
  member variables (K2 via `FBlueprintEditorUtils`/`UEdGraphSchema`); (3) **UMG widget-tree + bindings** over `UWidgetTree`;
  (4) **net-role PIE-world enumeration** → rubric `cross` check for replication verification (+ `LevelEditorPlaySettings`
  2-client listen-server); (5) **full class schema** (native `UCLASS` search, `FProperty` flags, `UFunction` params); (6)
  **off-thread execution + async event push** — the real fix for the P5 blocking-op wedge.
- **Out of scope (honestly deferred, not vaporware):** AnimBP/Niagara *graph* authoring (cover 80% via `duplicate_asset` +
  `User.*` params); frame-accurate determinism; many-agents-share-one-editor.
- **Acceptance:** "play wave 1 (move to cover, fire, kill grunts), reach wave 2, watch a real caster spawn+attack — no cheat
  scaffolding"; "add a wave-countdown `TextBlock` bound to `gamestate.WaveNumber` in pure Blueprint"; "add a `Replicated Shield`
  → FAIL: server=100 client=0 (missing DOREPLIFETIME)".

## Workstream PW — Deterministic & instanced-content verification (builder/sim games; from poly-world)

A cross-cutting workstream (composes with P2/P4) that generalizes what poly-world hand-rolled. **Game-agnostic** — any
instanced/builder/sim project benefits (and it de-blinds aesir too). poly-world proved the actor-centric model breaks for
instanced worlds.

- **PW1 — Instance-aware observation.** `list_actors`/`pie_observe.counts`/`level_diff` are actor-keyed and blind to
  Hierarchical/Instanced Static Meshes: poly-world's whole city is HISM instances inside **one** `APolyField` actor, so
  `level_diff` sees 1 actor whether it holds 3 instances or 4096.
  - Bridge: `instances_count(actor?, tag?, mesh?)` and `instances_list` over `HierarchicalInstancedStaticMeshComponent`/
    `InstancedStaticMeshComponent` (per-component instance transforms); extend `pie_observe` with an `instances` field
    (tallies by component tag/mesh) alongside `counts`. Fixes the "counts count actors, not instances" blindness.
- **PW2 — Content-digest oracle (deterministic verification).** poly-world quantizes instance transforms → canonical lines
  → sort → SHA1, and runs it in BOTH browser JS *and* C++ (`PolyVerify.cpp`) to compare — because the MCP has no digest.
  - Go `internal/digest` (pure, unit-tested): quantize (`floor(v/b+0.5)`, angle-normalize `[0,360)`, `scale*1000`),
    canonical code-point sort, SHA1 → `{hash, count, worst_bucket_margin}`; params are inputs (bucket sizes). Bridge
    `scene_digest(scope: actors|instances|both, tag?, quantize)` returns the raw transforms (or computes editor-side);
    Go computes the deterministic hash. **One MCP oracle replaces the hand-rolled JS+C++ dual digest** and is the
    authored-content acceptance gate (`digest.hash == expected`).
- **PW3 — `pie_verify` (generalized functional harness).** `pie_wait_until` only works over the `pie_observe` schema —
  blind to sim state in subsystems/HISM. poly-world wraps `pie_exec → GetVerificationJSON() → evaluate predicate` in JS.
  - Go `pie_verify(target, ufunction, predicate, timeout_s, interval_s)`: poll a BlueprintCallable getter returning a JSON
    string, parse it, evaluate a predicate (reuse `internal/predicate`) over the parsed object until it holds or times out.
    First-class functional verification for any game exposing a JSON snapshot getter. Add a `func` rubric kind that reads
    a getter each frame.
- **PW4 — Golden-image verification.** poly-world decodes PNGs and samples color-band regions at projected coords
  (`p6_pixels.js`) to assert readability, against a fixed golden camera.
  - Extend `internal/imgdiff` (P4): golden-baseline compare (per-pixel + perceptual hash distance) and named
    **region color-band assertions** (`region(cx,cy,rad) → dominant channel`), with an explicit `update_baseline`.
    `capture_golden` uses `take_screenshot` (already honors an explicit camera; editor-only, backgrounded-safe). New rubric
    kinds `golden` (hash/pixel distance ≤ ε) and `region_color` (a region is red/green/teal). **Two capture channels stay
    separate** (editor golden pose vs PIE pawn camera) — already handled by v7's `scene_capture` vs `pie_highres`.
- **Already shipped (from poly-world):** the `build_compile` editor-self-close + orphan-reclaim robustness fix (v7d4f2e6).

**PW acceptance:** author a poly-world map via `apply_level_recipe`, then `scene_digest(instances, tag="static") == expected`
(4096 instances verified, not "1 field actor"); drive a live sim and `pie_verify(gamestate, GetVerificationJSON,
"nodes.0.SupplyRatio < 0.5")` to gate a starvation event; capture the golden overview and assert `region_color` red at the
starved node and teal at the healthy link — all as first-class MCP tools, no hand-rolled JS/C++.

## How the phases close the loop

| Loop stage | Closed by |
|---|---|
| Design | P2 discovery + P4 spatial pre-checks + P5 dry-run |
| Author — C++ | v7 `build_compile`, hardened by P1 (crash/codes) + P5 (auto-reopen map) |
| Author — BP/framework/data/input | P3 (Python 70%) + **P7** (event-graph, UMG tree, IA/IMC) |
| Build | v7, extended to compile the P7 plugin via the same classifier |
| Play | P3 `pie_spawn`/Server-RPC (preconditions) + **P7** input synthesis |
| Observe | P2 `find_actors`/`reflect_class` + PW1 instances + P1 perf + P5 events |
| Assert | P1 codes + P4 scenarios/nav/LOS/visual/perf + PW2 digest + PW3 `pie_verify` + PW4 golden + **P7** replication `cross` |
| Self-correct | P1 codes + crash diagnosis + P5 `scene_restore` + P4 regression suite |
| At scale | P6 headless + affordance registry + editor pool |

## Autonomous vertical slice (acceptance): ranged-caster enemy, end-to-end

With no human, add a ranged caster (keeps distance, fires bolts, spawns from wave 2 on `L_Arena`), a "Wave 2 — Casters
incoming" HUD line, and a `Dash` input; certify and self-correct:

1. Orient (`map_gameplay`, `project_map`, `asset_query`, `enum_values`, `reflect_class` — P2)
2. Author C++ (caster fields, `ACasterBolt`, distance-keeping tick) + build (`build_compile` + P1 codes)
3. Author data: `Caster` row in `DT_Enemies` (`datatable_import` — P3)
4. Author BP: `BP_Caster` from `AEnemyCharacter`, defaults, assign mesh+bolt slot, wire into wave-2 table (`blueprint_*` — P3)
5. Author input: `Dash` `ActionMapping` (`input_ini_edit` — P3) + C++ handler + build
6. Author HUD: "Wave 2" `TextBlock` bound to `gamestate.WaveNumber` (`widget_create` P3 + **P7** tree+binding, or C++ pre-plugin)
7. Pre-play: `nav_path` spawn→core, caster→player LOS, GameMode+PlayerStart resolvable (`world_query`, `design_check` — P4)
8. Play: `pie_spawn` a Caster at a known transform (P3) / **P7** input synthesis to play through wave 1
9. Assert (scenario): exactly one `AEnemyCharacter[Archetype==Caster]`, stays >600u, spawns a bolt, Dash moves the pawn,
   HUD visible, `p95 frame_ms<20`, `log_max errors 0`, no near-black frames (`find_actors` + `scenario_run` + rubric); **P7**
   replication `cross` if a caster field is `Replicated`
10. Self-correct: read `{code}` + crash `{file,line}` + evidence frame; `scene_restore` on a bad layout (P1 + P5)
11. Regress: save the scenario; `scenario_suite_run` headless overnight; open a fix PR on FAIL (P4/P6)

**P1–P6 + PW certify the slice** via `pie_spawn` + Server-RPC + reflection/find_actors + scenarios/digest/golden. **P7 upgrades
it** to genuine player-driven play, pure-BP HUD binding, and replication certification — closing the north-star loop.

## Feasibility ledger

- **Pure Python (bridge ops):** error taxonomy, perf tier-a, asset_query/deps/tags, reflect_class (partial), enum_values
  (BlueprintType only), map_gameplay, find_actors, blueprint create/compose/defaults/assign, set_world_gamemode,
  datatable/dataasset/curve(gate), widget_create, pie_spawn/set/destroy, world_query, op_id worker/editor_ping/events tail,
  scene snapshot+restore, dry-run, **instances_count/list, scene_digest raw read, pie_verify getter**.
- **Pure Go (no editor):** crash scanner, projectmap, projectconfig (ini), scenario engine, perf parser, imgdiff, headless
  runner, affordances, editor pool, all rubric reducers, **digest quantize+hash, golden/region imgdiff**.
- **Genuinely needs the C++ plugin (P7):** PIE input synthesis (legacy binds), BP event-graph nodes + member variables, UMG
  widget-tree + bindings, net-role PIE-world enumeration, FProperty flags + native class search, off-thread + async event push.
- **Dropped / corrected:** "5.7 `DefaultKeyMappings` deprecation" (fabricated → legacy ini); `ARFilter.class_names`
  (deprecated → `class_paths`); crash op as a bridge op (must be Go-side); op_id worker "solves the wedge" (only chunkable
  ops); AnimBP/Niagara graph authoring, frame-accurate determinism, shared-editor multi-agent (out of scope, fallbacks noted).

_Full teardown transcript + per-agent findings: `.../tasks/wyr9dpxjm.output` and the workflow journal._
