# G interface contract (REMEDIATION_PLAN.md G.6)

Status: **implemented and live-verified on the scratch copies (G.1–G.5)**; frozen at G.7 (then only additive
changes; the held-out tasks may rely on everything below). Patches for the real repositories:
[`game-patches/`](game-patches/) (`aesir-G.patch` + `aesir-G-assets.py`, `polyworld-G.patch`).

The game-side surface the server and the game-making eval rely on. Both games expose it through one subsystem
declared in the project's `.umcp.json` `game_api` (R1.4). Every function is a `UFUNCTION`; read-only ones are
`BlueprintPure` and `const` (the R1.3 getter rule, checked live with `UMCPCoreLibrary::IsPureOrConst`); commands are
`BlueprintCallable`. All take and return JSON strings.

## Common API (both games)

| Function | Kind | Returns |
|---|---|---|
| `GetCapabilitiesJson()` | pure, const | `{api_version, world_epoch, commands: [{name, tier, args}], event_kinds: [...]}` — `tier` is informational (R1.5: `game_command` is always Exec) |
| `PeekSnapshotJson()` | pure, const, **no side effects** | the game state + `world_epoch`, `events_cursor` (poly-world: the copy its ticker builds, ≤ 0.25 s old and rebuilt after every command — building one hydrates the economy, which writes) |
| `GetEventsSince(FString Cursor)` | pure, const | `{events: [...], next_cursor, gap: bool, dropped: n, world_epoch, reason?}` — the cursor is `""` (from the start) or `"<world_epoch>:<seq>"` (it carries the epoch, so it is a string, not the `int64` of plan r6) |
| `ExecuteCommandJson(FString RequestJson)` | callable | `{accepted, request_id, world_epoch, result?, error_code?, message?}` |

- **`world_epoch`**: a GUID made per world — when the subsystem initialises and, for Aesir's GameInstance subsystem,
  on every world it enters (map travel, a new PIE session), which also clears its journal, dedup memory and difficulty. Cursors encode it
  (`"<epoch>:<seq>"`); `GetEventsSince` with an old-epoch cursor returns `gap: true, reason: "world_changed"` and the
  events of the current world from its start.
- **Event journal**: a ring buffer of ≥ 512 events. When the cursor has fallen behind the oldest kept event,
  `gap: true` and `dropped` = the number of events lost.
- **Commands**: the request is `{"command": name, "request_id": "...", "world_epoch": "...", ...args}`.
  `request_id` is required. Repeats of a `request_id` within the same world are deduplicated: the recorded response
  is returned, nothing runs again, and at least 256 ids are remembered. A request whose `world_epoch` is not the
  current one returns `accepted: false, error_code: "dedup_expired"` and **does not run**. The server's
  `game_command` always sends the epoch; Aesir also refuses a request without one, while poly-world accepts one
  without an epoch (its gRPC clients predate it) and dedups it within the current world only.
- An event is `{seq, t (world seconds), kind, actor?, target?, by_player, data?}`; a `hit` carries `data.visual_t`
  (when its visual feedback was drawn), which the playtest timeline lifts to `visual_t`.

## Server outputs the eval probes read (R5 contract)

- `playtest op=run` writes `<capture session dir>/playtest.json` (`Saved/MCP/capture/<session>/`) with the verdict,
  rubric and the merged event timeline `events: [{t, kind, actor?, target?, by_player, data?, visual_t?}]`.
- `playtest op=batch` writes `Saved/MCP/playtest/batch-<id>.json` with `runs: [{seed, verdict, waves: [{wave,
  clear_s}]}]`.
- Definitions (the feel audit and telemetry kinds use the same ones): **hit-to-visual latency** = `visual_t − t` of
  a `hit` event (seconds; reported in ms); **time-to-kill** of a target = the `kill` event's `t` − the first `hit` on
  that target by the weapon; **wave clear time** = `wave_end.data.clear_s`.

## aesir-wave-defense

Subsystem `UAesirAgentSubsystem` (GameInstance subsystem, game module `AesirWaveDefense`).

| Item | Name |
|---|---|
| snapshot | `{wave_number, wave_state, enemies_remaining, enemies_on_field, intermission_remaining_s, player: {alive, health, max_health, weapon, ammo, max_ammo, location}, kills, score, difficulty}` |
| commands | `start_wave` (from intermission; `wave_in_progress` during a wave, `run_over` after defeat/victory), `set_difficulty {level: easy\|normal\|hard}` (enemy health ×0.75 / ×1 / ×1.5, this world only, nest spawns included); both tier `mutating` |
| event kinds | `weapon_fire`, `hit {weapon, damage, distance, visual_t}` (`visual_t`: when the HUD drew the hit marker), `kill {by_player}`, `death`, `dash`, `vfx`, `sfx`, `camera_shake`, `wave_start`, `wave_end {wave, clear_s}`. Recorded by the authority (standalone / listen server / PIE); multiplayer clients record nothing |
| wave table | DataTable `/Game/Data/DT_Waves`, rows `Wave_01`…`Wave_13`, row struct `FAesirWaveRow {EnemyCount, SpawnInterval, BruteChance, SprinterChance}`; a wave with no row (past the table, a deleted row, a table of another row type) uses the game's built-in formula. Every 5th wave adds a boss on top of `EnemyCount` |
| tuning asset | `UAesirTuning` (`UPrimaryDataAsset`) `/Game/Data/DA_AesirTuning`: `PlayerDamageMultiplier`, `EnemyHealthMultiplier`, `DamageFalloff` (→ curve), `Waves` (→ table) |
| damage curve | `UCurveFloat` `/Game/Data/C_DamageFalloff`: damage multiplier by distance (units) |
| input | Enhanced Input action `/Game/Input/IA_Dash` in mapping context `/Game/Input/IMC_Aesir` (default key Left Shift), bound in `AAesirCharacter` |

## poly-world

Subsystem `UPolyWorldAgentSubsystem` (World subsystem, game module `PolyWorld`) — the existing `polyworld.agent.v1`
API plus: `GetEventsSince(FString)`, `PeekSnapshotJson()`, `world_epoch` everywhere, per-command tiers and arguments in
`GetCapabilitiesJson`, `request_id` + `error_code`/`message` on every result (the `error` object stays for its gRPC
clients). Events are `world.revised` (`data` = `{economy_digest, capital}`, `actor` = the company, plus `cycle`).
Commands as today (`place_building`, `place_road`, `demolish`, `step_cycles`, …).
