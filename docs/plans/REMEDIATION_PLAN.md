# Remediation plan — from "drives the editor" to "makes and judges a game"

Status: **r6 — approved; R0.2/R0.3 spike results in [`REMEDIATION_SPIKES.md`](REMEDIATION_SPIKES.md) (plugin API numbering gained an R3 row)** (CTO review: r1 B−, r2 A−, r3 A, r4 A, r5 A, **r6 A+**; every finding is addressed — see §11–§15). Source: the game-designer
adversarial review of v2.0.2 (overall **C−**; infrastructure B+, game-making D+).

## 1. Problem

The server is a reliable editor remote — PIE control, crash/modal handling, safe shutdown, checkpoints/revert and
async jobs are solid and live-validated — but an agent cannot do the work a game needs:

- **reach the game's own API**: refs stop at labels, paths, `@gamestate|@pawn|@controller` (`95_v2_core.py:60-78`);
  poly-world built `UPolyWorldAgentSubsystem` + gRPC because the server can't reach subsystems;
- **play like a player**: digital key taps only (`MCPControlSubsystem.cpp:48`, amount fixed at 1.0); Aesir's mouse
  aim (`AesirCharacter.cpp:182-183`) is undrivable; no cursor/UI clicks;
- **judge the result**: playtests can't act, beat errors don't fail the verdict (`v2_ops.go:703` vs `:743`), audits
  have no data source, `perf.fps` is recorder-perturbed, the balance explorer is an abstract model;
- **author data and logic**: DataAssets can't be edited, DataTables are replace-all/no-read, Blueprints are opaque;
- **close the UI loop**: binding/interaction ops were dropped; the plugin's `MCPHUDWidget` binder is unreachable.

## 2. Goals and measures

All goals are proven **live** on scratch copies of aesir-wave-defense and poly-world (the P7 harness: own multicast
group, own git repos), never on the user's projects.

| Goal | Measure |
|---|---|
| G1 Truthful judging | A playtest with a failed beat/setup step is never `PASS`; no rubric uses recorder-perturbed fps; every audit names its evidence and returns `PRECONDITION` (`details.reason: insufficient_evidence`) without it |
| G2 Reach the game | The agent reads and drives each game's agent subsystem (snapshot, validate, command, events) with **zero calls to the `python` tool** |
| G3 Play like a player | Through tools only: aim + fire in Aesir (mouse-axis input), click a UMG button, click a PolyWorld world tile |
| G4 Data-driven tuning | Read a DataTable, upsert a row, edit a DataAsset field and a curve key, and show the effect in a playtest |
| G5 UI loop | Bind a HUD readout to game state, mount it in PIE, verify it in a screenshot that includes UMG |
| G6 Evidence-based judging | Playtests record a gameplay event stream (sources per §5); `feel` and `decision` audits run on recorded events with no hand-built input; `audio` audit runs on game-journal events where the engine has no hook |
| G7 No ergonomic regression | Tool-selection eval (extended with ≥ 12 cases for the new tools, §4 R0.4) stays ≥ v2.0.2: first tool ≥ 97 %, valid args ≥ 99 % |

### The game-making eval (acceptance test for the plan)

- **25 multi-step tasks** on the scratch projects, live (not the emulator): 20 authored with the plan, **5 held out**
  — written by the game-designer reviewer **at G.7**, after phase G has made the game interfaces real, and sealed
  then (SHA-256 of the file committed before R1 starts, plaintext committed only at the final run), never seen by
  implementers. Held-out tasks may use only the **G interface contract** (§4 phase G: `DT_Waves`, `UAesirTuning`,
  `IA_Dash`, the `UAesirAgentSubsystem`/poly-world command names and event kinds), which is frozen at G.7.
- Coverage: each of G2–G6 by **≥ 3 tasks**; ≥ 8 tasks are multi-step author → play → verify. Examples: "make wave 2
  spawn 20 % more enemies and prove it in a playtest", "show the wave number on the HUD", "aim at and kill the nearest
  enemy", "connect the depot to the factory by road and verify the company earns revenue".
- **k = 3 runs** per task; a task passes if ≥ 2/3 runs meet its live predicate. A predicate is mechanical, written
  with the task: world/asset state read after the run (e.g. `DT_Waves` row values, actor counts), the recorded tool
  calls (e.g. a playtest ran after the edit), and — where the task asks a question — the answer checked by exact
  match or a numeric tolerance against a value read from the game. **No LLM judge.** A run aborted by the cost cap or
  a turn limit (40 turns) **fails**.
- **Pass bar (final):** ≥ 20/25 tasks pass, each goal G2–G6 ≥ 2 of its tasks pass, Python-tool calls ≤ 0.1/run.
- **Baseline** with the v2.0.2 server against the **post-G game copies** (phase G done, so every task is possible in
  principle and the delta measures the server alone; Python allowed), run at the end of phase G (G.7); an optional pre-G
  baseline may be added for context. **Final after R6.** Both committed under `docs/validation/gameeval/`, with the
  served model recorded per turn.
- **Runner:** `cmd/tooleval` extended with a `live` mode that drives a real server against a scratch editor; requests
  go through the user's Claude router (`ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`), model id `claude-sonnet-5`;
  if the served model differs between baseline and final, results are reported per served model.
- **Cost:** cap $1.50 per run and $150 per full eval (harness aborts beyond); the user is asked before **every** paid
  run that is not a pilot — the game-eval baseline and final, and the tool-selection re-runs (R0.1, R0.4, final);
  pilots of ≤ 3 tasks run without asking.

## 3. Constraints

- **Lints and pins are acceptance criteria in every phase**: `TestV2SpecsLint` (tiers, ≤ 8 ops/tool, no destructive
  op beside read-only ops, `world=auto` only on read-only tools), `TestEveryOpIsWired` (a T1 cell per new op),
  `TestToolListBudgets` (core ≤ 45 000 B; counts pinned — updated deliberately per the ledger), the surface golden
  (`UMCP_UPDATE_GOLDEN=1`), generated docs (`go generate ./internal/tools`), `TestPyOpsBijection` (every companion op
  classified), and the archtest DAG.
- **Live feasibility before code**: no op is planned on an unverified UE 5.7 Python call (R0.2 spike decides "Python"
  or "plugin" per call). Every new op's acceptance includes a **live T4 check**, not pytest alone.
- **Plugin stays optional**: plugin-backed ops declare `Needs: plugin>=N` and fail with `PRECONDITION` + rebuild hint
  when the plugin is missing or older (R0.5 handshake). The plugin API version is bumped **by every phase that
  changes the plugin** (§8 table), and each op declares the version of its own phase.
- **Game-callable functions**: every game-side function the server calls (phase G) is `UFUNCTION(BlueprintCallable)`
  — read-only ones `BlueprintPure` and `const` — so they are visible to Python and pass the R1.3 getter rule; "called
  from the companion in a live editor" is part of each G acceptance.
- **No silent fallbacks, closed error set, tiers** (CLAUDE.md) for every op.

## 4. Phases

Estimates are engineer-days (one agent-assisted engineer) and **include** spikes, plugin rebuilds of both scratch
projects, live T4 runs and the specialist gate per phase.

### R0 — Foundations and truthfulness (≈ 13 d)

| ID | Item | Acceptance |
|---|---|---|
| R0.1 | **Byte ledger** (§6) committed; toolset moves done first (`world` toolset) so later phases have headroom. The move is a listed behaviour change (§8): core text that names moved tools (the rollback ladder `docs.go:48`, `docs.go:162`, `v2_extras.go:105`) says "toolset `world`"; `V1Calls` targets that land on `scene`/`world_query` (`migration.go:65-169`) carry the toolset in their note; the `toolsets` description/enum growth is in the ledger | `TestToolListBudgets` with the new pins; migration test; tool-selection eval re-run on the moved tools (paid, user approves; no regression) |
| R0.2 | **Live UE 5.7 feasibility spike** (2 d): run once, in a scratch editor, each uncertain call — `FRichCurve` key edit, Blueprint `NewVariables` read, `InputMappingContext` mapping edit, PIE-world actor spawn, DataTable row add/remove, game-instance/world subsystem lookup, HighResShot vs UMG, `BlueprintCallable` `int64`/`FString` UFUNCTION round-trip, **reading `FUNC_BlueprintPure`/`FUNC_Const` of a function** (R1.3), **reading the title of the next undo/redo transaction** (R0.9) — and record "Python" or "plugin" per call in `docs/plans/REMEDIATION_SPIKES.md`. Plugin fallbacks named in advance: `UMCPCoreLibrary::IsPureOrConst(UClass*, FName)`, `PeekUndoTitle()/PeekRedoTitle()`, `UndoIfTitled(Prefix)/RedoIfTitled(Prefix)` (in the R0.5 plugin version) | the table exists; later items cite it; the fallbacks land in R0.5 if needed |
| R0.3 | **Event-source spike** (1 d): confirm each row of the §5 matrix in Aesir (damage, spawn/destroy hooks; journal needs) | matrix updated with results |
| R0.4 | **Eval harness**: `cmd/tooleval` `live` mode (scratch editor, predicates, cost/turn caps), the 20 plan tasks (the 5 held-out tasks are written and sealed at G.7), tool-selection corpus + 12 cases (`game` vs `actor_call` vs `python`, `undo`, data ops); a 3-task pilot on v2.0.2. (The paid baseline runs after phase G, §2.) | harness tests; pilot committed under `docs/validation/gameeval/pilot/` |
| R0.5 | **Plugin API handshake**: `UMCPCoreLibrary::GetPluginApiVersion()` (plugin API = 3, plus the R0.2 fallbacks if needed); companion reads it; specs declare `Needs: plugin>=N`; stale → `PRECONDITION` "rebuild the UnrealMCP plugin (build strategy=ubt)" | T1 + live with old/new plugin |
| R0.6 | Playtest verdict: any beat/setup error ⇒ `FAIL` (`beat_errors: warn` opt-in ⇒ `WARN`); reasons listed | T1 test; CHANGELOG "Behaviour changes" (ships as v2.1.0) |
| R0.7 | `perf.fps` → `recorder_tick_ms`; rubric lint rejects recorder perf checks unless `allow_perturbed: true` | eval tests |
| R0.8 | Audits declare evidence; missing ⇒ `PRECONDITION`/`insufficient_evidence`; `luminance` refuses `game_scene` frames without `source_exposure`; `design_explore` labelled "abstract wave model, not your game"; dead `internal/critique`/`persona` refs removed; tool-eval README states its scope | pytest per audit + doc check |
| R0.9 | **Undo** (moved up — a quick win): new **`undo`** tool (Destructive, editor world): `op=undo|redo`. The stack is shared with the human, so it acts **only when the target transaction's title starts with `MCP: `** (every `_undoable` edit is titled so: `95_v2_core.py:255`, `60_scene.py:183,215`, `97_v2_play.py:192,267`); otherwise `CONFLICT` with `details.title` and nothing is undone; returns the undone title. **Check and undo are one game-thread step** — a single companion op (or the plugin's `UndoIfTitled(prefix)` if R0.2 finds no Python title read) — so no edit can land between them. Refused while PIE runs (`PRECONDITION`). **Scope**: actor/scene/snapshot edits and the new `data_edit` ops (R3, wrapped in `MCP:` transactions); `asset_create`/`asset_edit` and plugin widget edits open no transaction today (`96_v2_assets.py:79,108`, MCPAuthoring) and are **not undoable** — their results say `undoable: false` and the tool description points to `git_revert`/checkpoints for them | live: spawn → undo → gone, redo → back; a manual edit on top ⇒ `CONFLICT`, stack unchanged; undo during PIE ⇒ `PRECONDITION`; an `asset_edit` result has `undoable: false` |

### G — Consumer-game changes (≈ 8 d; sequential, before R1; owner: this effort, each game repo change committed path-limited after the user approves the phase)

| ID | Item | Acceptance |
|---|---|---|
| G.1 | poly-world: `UFUNCTION(BlueprintPure) FString GetEventsSince(const FString& Cursor) const` (a string: the cursor carries the world epoch, G.6) (wraps `GetEventsJson`); a side-effect-free `UFUNCTION(BlueprintPure) FString PeekSnapshotJson() const` (no `RefreshRevision`); `GetCapabilitiesJson` made `BlueprintPure` const and declares each command's tier; the command function stays `BlueprintCallable` | each called from the companion in a live editor; pure/const confirmed by the R1.3 check; poly-world tests pass |
| G.2 | aesir: `UAesirAgentSubsystem` (GameInstance subsystem) — `BlueprintPure` const capabilities, snapshot (wave, enemies, player, weapon) and `GetEventsSince(int64)`; `BlueprintCallable` commands (`start_wave`, `set_difficulty`) with `request_id` and an idempotent result lookup; event journal (hit, kill, death, weapon fire, VFX/SFX/camera-shake cues) | each called from the companion in a live editor |
| G.3 | aesir: wave tuning moved from `EditDefaultsOnly` GameMode fields (`AesirWaveDefenseGameMode.h:62-91`) to a `DT_Waves` DataTable + `UAesirTuning` DataAsset with a damage curve, read by the native GameMode | game behaves as before; wave values come from the table |
| G.4 | aesir: one Enhanced Input action (`IA_Dash`) bound in C++ (`AesirCharacter`), so R3.6 has a consumer | live: injected action fires |
| G.5 | plugin: `EMCPBindSource` gains `GameState` and `Subsystem` (appended — enum order preserved), `MCPHUDWidget` resolves them; plugin API → 4 | live: a TextBlock shows the wave number |
| G.6 | **G interface contract** (both games; frozen by G.7): a **`world_epoch`** (GUID created when the subsystem initialises) is returned by `capabilities`, `snapshot` and `GetEventsSince`; cursors encode it; `game_command` sends the epoch the server last saw (filled from its most recent `game` read) and a mismatch returns `PRECONDITION` (`details.reason: dedup_expired`) **without running** — a fresh subsystem cannot remember earlier worlds' `request_id`s, so the epoch is what makes this detectable; `GetEventsSince` with an old-epoch cursor returns `gap: true, reason: world_changed`. `GetEventsSince(Cursor)` returns `{events, next_cursor, gap: bool, dropped: n}` — `gap` set when the cursor fell behind the journal (poly-world keeps 512 events, `PolyWorldAgentSubsystem.cpp:134`); command dedup window ≥ 256 `request_id`s for one world's lifetime (`:637-641`), and across worlds the epoch check above keeps "never blind retry" true; the names `DT_Waves`, `UAesirTuning`, `IA_Dash` and each game's command and event kinds | live: journal overrun ⇒ `gap: true` with `dropped`; duplicate `request_id` ⇒ one effect; restart PIE, then re-send the same `request_id` with the old epoch and read events with the old cursor ⇒ `PRECONDITION`, `gap: true` (`world_changed`), nothing run |
| G.7 | **Contract freeze + paid baseline** (§2): G interface contract (G.6) frozen; the 5 held-out tasks written by the game-designer reviewer and sealed; v2.0.2 server run against the post-G scratch copies (user approves) | contract frozen in `docs/plans/GAME_CONTRACT.md`; held-out SHA-256 committed before R1 starts; baseline committed under `docs/validation/gameeval/baseline/` |

### R1 — Reach the game (≈ 9 d)

| ID | Item | Acceptance |
|---|---|---|
| R1.1 | `_resolve_object` (separate from `_resolve_actor`): `@gameinstance`, `@playerstate[:n]`, `@hud`, `@subsystem:<Class>` (World/GameInstance subsystems; **Editor subsystems only in read-only `reflect`**); objects get an object view (no transform) | pytest + live (both games) |
| R1.2 | `actor_call` accepts object refs; `parse: "json"` (explicit) decodes JSON-string returns; struct/array args | pytest + live |
| R1.3 | Predicates: `and`/`or`/`not`; paths may read object properties and call **only `BlueprintPure`/`const` getters**, enforced by the flag check R0.2 selected (Python, or the plugin's `IsPureOrConst`; with neither available, function calls in predicates are refused — properties only); waits beyond 25 s continue as a job (`wait_s` + `job`) | eval tests + live (a non-pure function ⇒ `INVALID_ARGUMENT`) |
| R1.4 | **`game_api` convention v1** in `.umcp.json`: `{"version": 1, "object": "@subsystem:<Class>", "capabilities": "<UFUNCTION>", "snapshot": "<UFUNCTION>", "command": "<UFUNCTION>", "events": "<UFUNCTION>"}` — strict schema (unknown fields, wrong version ⇒ error), object must be a World/GameInstance subsystem in a **game module** (not Engine/Editor/plugins), each function a named UFUNCTION on it. **A `game_api` error is local**: it disables only the `game` toolset and is reported (stderr log, `toolsets` entry `game: unavailable` with the reason, `project op=attach` result); the rest of `.umcp.json` — `gate_policy` above all — still loads and applies | config tests (stdio and daemon attach): a bad `game_api` with `gate_policy: require` ⇒ session starts, gate enforced, `game` unavailable |
| R1.5 | Tools (new toolset `game`, auto-enabled when `game_api` is declared — stdio startup and daemon attach): **`game`** (read-only: `capabilities`, `snapshot`, `events since=<cursor>`) and **`game_command`** (separate tool, **fixed tier Exec** — gating, MCP annotations and retry policy are all derived from the op's static tier before the handler runs (`spec/spec.go:206-222,312,323`), so a per-command tier could not change them; the tiers a game declares in `capabilities` are informational only: echoed in results, shown in the cockpit and `game op=capabilities`, never used to relax a gate). `request_id` is mandatory and passed to the game with the last-seen `world_epoch` (G.6), which deduplicates repeats within that world (with no epoch read yet in this session, the server reads `capabilities` once before the first command — a command is never sent without an epoch); that is the only reason the op is marked `Idempotent: true` (stated in its spec comment and description), and an `outcome: unknown` is resolved by re-sending the same `request_id` (the game returns the recorded result), never by a new call. Under `gate_policy: require` every `game_command` is refused by `DenyGate` (no approval surface) | T1 with a fake game API (duplicate `request_id` ⇒ one effect, same result); `TestV2SpecsLint` green; a spec test pins `game_command` = Exec + Idempotent; live on both games |
| R1.6 | `polyworld` toolset deprecated: mapping table `polyworld op=X` → `game_command name=Y` (args differ: CompanyMVP world locations vs AgentSubsystem city/option/anchor/orientation — table lists both), `V1Calls` updated (`migration.go`), removed in v2.3 | docs + migration test |

### R2 — Play like a player (≈ 10 d; plugin)

| ID | Item | Acceptance |
|---|---|---|
| R2.1 | `pie op=input` gains `axis` + `value` (mouse X/Y, gamepad sticks/triggers); the plugin injects **every tick** for `duration_s` (axis values are per-frame) | live: Aesir yaw changes by the injected amount |
| R2.2 | `pie op=cursor` move/click/drag in viewport coordinates via Slate; events from a virtual Slate user, inside the game viewport only — the user's OS cursor is never moved or captured and the game's input mode is left alone (revised after the R2 review: switching it to GameAndUI broke an FPS's mouse-look for good); updates the cached cursor so `DeprojectMousePosition` sees it | live: click a UMG button and a PolyWorld tile, focused and unfocused editor |
| R2.3 | `pie op=ui_click widget=<name>` (resolve a live widget, click its centre) | live |
| R2.4 | Playtest beats: `input` (R2.1–2.3) and `game_command` (R1.5) added to the existing exec/console/wait beats; schedule on **world time** (`at_world_s`) | T1 + live scenario |
| R2.5 | PIE spawn per R0.2: Python if the spike finds a game-world spawn, else a plugin UFUNCTION (`UMCPControlSubsystem::SpawnInGame`) | live |

R2's plugin changes (per-tick axis injection, cursor/UI click, `SpawnInGame`) ship as plugin API 5; R2 ops declare
`Needs: plugin>=5` and pass the §7 rebuild gate.

### R3 — Data and logic authoring (≈ 9 d; new toolset `data`)

Tools (all in toolset `data`, keeping core within budget): **`data_query`** (read-only: `table` rows, `blueprint`
describe) and **`data_edit`** (Mutating: `set_properties` on any asset, `table_upsert`, `curve_keys`, `add_variable`,
`input_mapping`; Destructive: `table_delete`).

| ID | Item | Acceptance |
|---|---|---|
| R3.1 | `set_properties` on DataAssets/PrimaryDataAssets/settings, per-property errors | live on `UAesirTuning` (G.3) |
| R3.2 | `table` read (typed rows), `table_upsert` / `table_delete` keyed (never replace-all) | live on `DT_Waves` (G.3) |
| R3.3 | `curve_keys` (per R0.2: Python or plugin) | live on the G.3 damage curve |
| R3.4 | `blueprint` describe: components, variables, functions/events, compile status + messages | live on an Aesir BP |
| R3.5 | `add_variable`; Blueprint **graph editing stays a non-goal** (logic in C++ via `build`) | live |
| R3.6 | `input_mapping`: create/edit `InputAction` + `InputMappingContext` assets (per R0.2) | live: `IA_Dash` (G.4) remapped and fired via R2.1 |

### R4 — UI loop (≈ 5 d; plugin API 7 for `mount`/`live_tree`; §7 rebuild gate)

| ID | Item | Acceptance |
|---|---|---|
| R4.1 | `widget_edit op=bind` → `MCPHUDWidget` bindings incl. the G.5 `GameState`/`Subsystem` sources | live: wave number on the HUD |
| R4.2 | `widget_query op=mount` (add to viewport in PIE) and `op=live_tree` (live tree with geometry/visibility) | live |
| R4.3 | `screenshot op=pie ui=true` routed to the **existing** plugin capture with UI (`MCPCaptureSubsystem` `include_ui` → `FSlateApplication::TakeScreenshot`). (An R0.2 note that HighResShot already includes UMG was wrong — corrected in G.5, `REMEDIATION_SPIKES.md` row 11.) | live screenshot shows a mounted UMG HUD |

### R5 — Judge with evidence (≈ 12 d; plugin API 8; §7 rebuild gate)

| ID | Item | Acceptance |
|---|---|---|
| R5.1 | **Event recorder in the plugin** (C++, never Python on the game thread): `UMCPEventRecorder` binds the engine hooks of the §5 matrix (world `OnActorSpawned`, per-actor `OnTakeAnyDamage`/`OnDestroyed` bound at spawn **and on every actor already in the world when recording starts** — placed nests, the core, the player; all bindings removed on disable and at PIE end), stamps world time, and writes a bounded ring buffer (drop count reported); the companion drains it per recorder tick (`DrainEvents(cursor)`) and merges game-journal events (G.1/G.2 `GetEventsSince`) into the playtest timeline; a plugin-buffer drop or a journal `gap` (G.6) becomes a **timeline gap** marker, and an audit or telemetry rubric over a range with a gap returns `insufficient_evidence`. Off unless the scenario enables it; `Needs: plugin>=8`, older plugin ⇒ engine-hook rows `unavailable` (audits then `insufficient_evidence`) | live Aesir playtest timeline has hit/kill/death/VFX/SFX events; drop count 0 on a 5-minute wave run; damage to a placed (pre-existing) actor is recorded; no bindings remain after PIE end |
| R5.2 | Audits read a playtest result directly (`design_audit kind=feel source=<result>`) | live feel + decision audits on Aesir |
| R5.3 | Telemetry rubric kinds `histogram`, `rate`, `time_between` (time-to-kill, deaths by cause, waves per minute) | eval tests + live |
| R5.4 | Perf pass: `playtest op=run perf=true` — CsvProfiler with the recorder off, then `analyze perf`; rubric reads `perf_csv.p95_frame_ms` (the CsvProfiler pass's own namespace — `perf.*` is rejected by the R0.7 lint); overhead of R5.1 measured here | live |
| R5.5 | Seeded batches: `playtest op=batch seeds=[…]` (async), aggregate verdicts + variance; balance through the game's own headless tests (`headless op=tests`, e.g. poly-world `BalanceSweepTests`) | live batch of 5 seeds |

### R6 — Rollback (≈ 3 d; trimmed)

| ID | Item | Acceptance |
|---|---|---|
| R6.1 | Snapshots capture chosen properties; `snapshot_restore` restores them | pytest + live |
| R6.2 | `level op=open save=false`; `dry_run` on `data_edit` and `asset_create` | pytest + live |
| R6.3 | `world_query op=sphere_overlap` takes object types (pawns included — a correctness bug) | live |

Deferred to a follow-up plan (no eval task needs them): instanced placement, navmesh rebuild, World Partition region
loading, data layers; non-goals: landscape/foliage/PCG authoring, Blueprint graph editing, packaging, multiplayer.

## 5. Gameplay event sources (R5.1; confirmed in R0.3)

| Event | Source |
|---|---|
| damage dealt/taken | engine (plugin recorder, R5.1): per-actor `OnTakeAnyDamage`/`OnTakePointDamage`, bound on actor spawn (Aesir calls `Super::TakeDamage`) |
| actor spawned / destroyed | engine: world `OnActorSpawned`, actor `OnDestroyed` |
| player input | plugin (it injects / observes input) |
| death / kill | **game journal** (a game concept): Aesir `UAesirAgentSubsystem` (G.2), poly-world events (G.1) |
| VFX (Niagara), SFX, camera shake | **game journal** — no global engine hook (`AesirWeapon.cpp:250`, `AesirCharacter.cpp:393` call them directly); G.2 emits cues |
| economy / logistics (poly-world) | game journal (G.1) |

An audit whose evidence has no source for a game returns `insufficient_evidence` (R0.8), never a score.

## 6. Byte ledger (core tools/list ≤ 45 000 B; today 44 983 B)

| Change | Δ core bytes (est.) |
|---|---|
| Move `world_query`, `scene`, `scene_clear` to a new **`world`** toolset (catalogued in `toolsets`) | **−4 400** |
| `undo` tool (R0.9) | +450 |
| Actor/object refs in `actor_call`/`actor_query`/`reflect`/`pie_observe` descriptions (R1.1–1.2) | +250 |
| Predicate syntax + wait continuation (R1.3) | +120 |
| `pie` `axis`/`value`/`cursor`/`ui_click` (R2.1–2.3) | +500 |
| Playtest beats/`perf`/`batch` (R2.4, R5.4, R5.5) | +300 |
| `widget_query` `mount`/`live_tree` (R4.2), `screenshot ui` (R4.3) | +320 |
| `snapshot` properties, `level save`, `asset_create dry_run` (R6) | +200 |
| R0.6–R0.8 description wording | +60 |
| `toolsets` description/enum: `world`, `game`, `data` entries (+ "game: unavailable" reason field) | +330 |
| Core text renaming moved tools as "toolset `world`" (R0.1) | +80 |
| **Net** | **≈ −1 800 → ~43.2 KB** |
| *Measured after R0 (46 tools / 34 core)* | *41 818 B core (−3 165 B vs v2.0.2's 44 983 B); all toolsets 61 188 B* |
| *Measured after R2 (48 tools / 34 core)* | *43 808 B core; all toolsets 65 006 B* |
| *Measured after R3 (50 tools / 34 core)* | *43 989 B core; all toolsets 68 506 B* |

New optional toolsets: `game` (`game`, `game_command`), `data` (`data_query`, `data_edit`), `world` (moved tools).
Count pins: **50 tools / 34 core** after the plan (from 45 / 36). All toolsets ≤ 75 000 B (est. ~66 KB). Each phase
re-measures and updates the pins, golden and generated docs in the same commit.

## 7. Plugin rebuild gate and CI

GitHub runners have no UE 5.7, so plugin C++ is gated by a scripted checklist, run per phase that touches it:
copy `plugin/UnrealMCP` into both scratch projects → `build strategy=ubt` (the server's own safe-shutdown build) →
`editor op=health` + plugin API version check (R0.5) → that phase's T4 items. The script and its log are committed
with the phase. A self-hosted Windows UE 5.7 runner (compile-only job) is recommended, not required.

## 8. Versioning and compatibility

Released as **v2.1.0** with a "Behaviour changes" CHANGELOG section listing: R0.6/R0.7 (verdicts, rubric
validation) and the **`world` toolset move** — `world_query`, `scene`, `scene_clear` leave core; clients enable
toolset `world` (stdio `-toolsets world`, `.umcp.json` `toolsets`, or `toolsets op=enable`). scenario/v1 stays v1
(additive fields only). `game_api` is versioned (`version: 1`). `polyworld` toolset: deprecated in v2.1, removed in
v2.3 (R1.6).

Plugin API versions — one bump per phase that changes the plugin; each op declares the version of its phase:

| API | Phase | Adds |
|---|---|---|
| 3 | R0.5 | `GetPluginApiVersion`, R0.2 fallbacks (`IsPureOrConst`, `PeekUndoTitle`/`PeekRedoTitle`, `UndoIfTitled`/`RedoIfTitled`) if needed |
| 4 | G.5 | `EMCPBindSource` `GameState`/`Subsystem`; widget-tree access for 5.7 (`GetWidgetTree`, `Get/SetRootWidget`, `RegisterWidget`, `SetWidgetIsVariable`), `Get/SetClassDefaultJson`; `MountWidget`/`UnmountWidget`, `DescribeLiveWidgets` (R4.2's mount and live tree, needed by G.5's acceptance) |
| 5 | R2 | `InjectAxis` (per tick), `MoveCursor`/`ClickAt`/`DragCursor`/`ClickWidget` (a virtual Slate user routed down an explicit widget path; the player's input device), `SpawnInGame` |
| 6 | R3 | `GetCurveKeysJson` / `SetCurveKeysJson`, `DescribeBlueprintJson` (R0.2 spike rows 5–6) |
| 7 | R4 | (mount and live tree arrived in API 4) the UI-capture route, compose fixes |
| 8 | R5 | `UMCPEventRecorder` |

## 9. Sequencing and effort

R0 → G (ends with the paid baseline) → R1 → R2 → R3 → R4 → R5 → R6 → final eval. One engineer, so phases are
sequential in effort; R3.6's acceptance depends on R2.1, which this order respects. Gates: each phase passes its
acceptance, lints/pins, plugin rebuild gate (§7) where applicable, and a specialist review at **A or better** before
merge.

| Phase | R0 | G | R1 | R2 | R3 | R4 | R5 | R6 | Eval runs | Total |
|---|---|---|---|---|---|---|---|---|---|---|
| Days | 13 | 8 | 9 | 10 | 9 | 5 | 12 | 3 | 2 | **≈ 71** |

## 10. Risks

| Risk | Mitigation |
|---|---|
| A UE 5.7 Python call doesn't exist | R0.2 spike decides Python vs plugin before any op is built |
| Synthetic input steals the user's cursor/focus | GameAndUI, no capture; tested focused and unfocused (R2.2) |
| `game_command` as an escape hatch | strict schema, game-module subsystem allowlist, declared tiers (Exec default), `request_id`, `DenyGate` under `require` |
| Event recorder perturbs the game | off by default; overhead measured (R5.4) |
| Moving `world` tools out of core hurts discoverability | `toolsets` catalogues them; tool-selection eval re-run in R0.1 |
| Eval overfitting | 5 sealed held-out tasks; k = 3; baseline vs final |
| Consumer repos have the user's WIP | path-limited commits, user approval per phase (as in P7) |
| `undo` reverts the user's own edit | acts only on `MCP: `-titled transactions, else `CONFLICT` (R0.9) |
| An agent rewrites `.umcp.json` (e.g. drops `gate_policy`) through `python` or a file write | the protection is the tier model: every route that can write arbitrary files — `python`, `console`, `actor_call`, `playtest` (Exec), `git_revert` (Destructive) — is refused under `require` (no companion op writes arbitrary paths); in addition the server logs a warning when the file's digest changes between session start and end. **Known limit:** `python` under `gate_policy: off` can still write any file; the policy is read at session start only, so a rewrite affects later sessions — documented in operations.md |

## 11. CTO r1 findings → r2 (r2 snapshot; superseded where §12–§13 say otherwise)

| r1 finding | r2 |
|---|---|
| No core budget | §6 ledger, `world` toolset move first (R0.1), `game`/`data` toolsets, pins updated per phase |
| Tier-lint breaks (undo in `editor`, nav in `world_query`, one `game` tool) | `undo` own Destructive tool (R0.9); nav deferred; `game` + `game_command` split (R1.5); lint green in every acceptance (§3) |
| Unowned game-side changes | Phase G (G.1–G.5) with estimates and approval gates |
| Event tap overclaimed | §5 source matrix, R0.3 spike, G6 rewritten (journal-sourced audio/feel) |
| `@subsystem` / `game_api` security | `_resolve_object`, editor subsystems read-only, pure/const getters only, versioned strict schema, allowlist, declared tiers, `request_id`, `DenyGate`, explicit `parse: json` (R1.1–R1.5) |
| Acceptance pytest-only on uncertain 5.7 calls | R0.2 live spike; live T4 in every op's acceptance (§3) |
| No plugin CI/version | R0.5 handshake, §7 rebuild gate (+ optional self-hosted runner) |
| Eval doesn't prove goals | 25 tasks (5 sealed), k = 3, pass bar, ≥ 3 tasks per goal, baseline (r2: before R0; r3: on post-G games, G.6), pinned model id + served-model reporting, cost caps, user approval for paid runs, +12 tool-selection cases |
| Existing capability ignored; migration | R2.4 extends existing beats; R4.3 routes to existing `include_ui`; R1.6 mapping table + `V1Calls` + removal version; daemon attach enables `game` |
| R2 input risks | GameAndUI/no capture, per-tick axis, cursor cache (R2.1–R2.2) |
| Estimates/order | r2: ≈ 66 d (r3: ≈ 71 d) incl. spikes/rebuilds/gates; undo moved to R0; spikes in R0; R6 trimmed, world items deferred |

## 12. CTO r2 findings → r3

| r2 finding | r3 |
|---|---|
| G.1/G.2 UFUNCTIONs not Python-visible | `BlueprintCallable`; read-only ones `BlueprintPure` const; "called from the companion live" in acceptance (§3, G.1, G.2) |
| Pure/const flag check and undo title not spiked | both in R0.2 with named plugin fallbacks; R1.3 refuses function calls if neither works |
| Undo can revert the user's edits | only `MCP: `-titled transactions, else `CONFLICT`; acceptance includes a manual edit on top |
| Baseline confounded by game changes | baseline = v2.0.2 server on post-G games (G.6); mechanical predicates, no LLM judge; cap/turn-limit abort = fail |
| Plugin API version contradictory | per-phase bumps 3–7 (§8 table); each op declares its phase's version |
| `game_api` error blocks the session; `.umcp.json` rewritable | `game_api` errors local to the `game` toolset, `gate_policy` still applies (R1.4); `.umcp.json` protected + known limit (§10) |
| `world` move unlisted breaking change | §8 behaviour change; R0.1 fixes core references and `V1Calls` notes; `toolsets` growth in the ledger (§6) |
| Recorder location unspecified | `UMCPEventRecorder` in the plugin, ring buffer drained by the companion, API 7, rebuild gate (R5.1) |
| R0 underestimated; parallelism; paid re-runs | R0 13 d, G 8 d (baseline), total ≈ 71 d, sequential; every non-pilot paid run asks the user (§2) |

## 13. CTO r3 findings → r4

| r3 finding | r4 |
|---|---|
| Per-command `game_command` tier can't affect static-tier gating | option (a): `game_command` fixed Exec; declared tiers informational only; `Idempotent: true` justified by `request_id` dedup; spec test pins both (R1.5) |
| Undo scope and atomicity undefined | scope listed (asset/widget edits `undoable: false`); title check + undo in one game-thread op or `UndoIfTitled`; refused during PIE (R0.9) |
| Held-out tasks sealed before G interfaces exist | written and sealed at G.6; G interface contract frozen there (§2) |
| Recorder misses pre-existing actors | binds existing actors at enable, unbinds at disable/PIE end (R5.1) |
| Stale §11; empty `.umcp.json` protection claim | §11 marked as r2 snapshot with r3 values; protection stated as the tier model + digest warning (§10) |

## 14. CTO r4 findings → r5

| r4 finding | r5 |
|---|---|
| Game journals can lose events silently; dedup lost across PIE | G.7 contract: `gap`/`dropped` from `GetEventsSince`; dedup window stated; re-send after restart ⇒ `PRECONDITION`; R5.1 timeline gaps ⇒ `insufficient_evidence` |
| R0.4 still sealed held-out tasks | R0.4 has the 20 plan tasks; G.6 writes and seals the 5 held-out tasks |
| `UndoIfTitled` missing from fallbacks | in R0.2 fallbacks and the §8 API 3 row (with redo variants) |

## 15. CTO r5 findings → r6

| r5 finding | r6 |
|---|---|
| A restarted world can't recognise old `request_id`s; old cursors read silently empty | `world_epoch` GUID in every read; `game_command` sends the last-seen epoch, mismatch ⇒ `PRECONDITION` (`dedup_expired`) without running; old-epoch cursor ⇒ `gap: true, world_changed`; live restart check (G.6) |
| Phase G header said 7 d / parallel; G.7 listed after the step that freezes it | header "≈ 8 d; sequential, before R1"; contract is G.6, freeze + seal + baseline is G.7 (references in §2/§13/§14 tables keep their historical numbering) |
