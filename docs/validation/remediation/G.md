# Phase G live validation — 2026-10-03

Game-side changes (plan G.1–G.5) built and checked on the scratch copies (`_p7scratch/aesir`,
`_p7scratch/PolyWorld`, own multicast group, own git repos). The patches for the real repositories are in
[`../../plans/game-patches/`](../../plans/game-patches/); the contract they implement is
[`../../plans/GAME_CONTRACT.md`](../../plans/GAME_CONTRACT.md). Plugin gate (API 4):
[`G-plugin-gate-aesir.txt`](G-plugin-gate-aesir.txt), [`G-plugin-gate-PolyWorld.txt`](G-plugin-gate-PolyWorld.txt).

## G.2 — `UAesirAgentSubsystem` (aesir, PIE)

| Check | Result |
|---|---|
| found through `MCPCoreLibrary.find_game_subsystem` (GameInstance subsystem) | yes |
| `IsPureOrConst`: `GetCapabilitiesJson`, `PeekSnapshotJson`, `GetEventsSince` / `ExecuteCommandJson` | true, true, true / false |
| snapshot before | `wave_number 0, wave_state intermission`, player health 100, Rifle 30/30 |
| `start_wave` (`request_id` live-1, current epoch) | `accepted`, `wave_number 1`; snapshot `in_progress`, `enemies_remaining 5` |
| same `request_id` again | the identical recorded response (nothing ran) |
| `world_epoch: "old"` | `dedup_expired`, not run |
| no `request_id` | `invalid_argument` |
| `start_wave` during a wave | `wave_in_progress` |
| `GetEventsSince("")` | `wave_start {wave 1, enemies 5}`; cursor `<epoch>:1` |
| `GetEventsSince("oldepoch:5")` | `gap: true, reason: world_changed` |
| firing (two `LeftMouseButton` taps) | events `weapon_fire`, `hit`, `sfx`, `vfx`, `camera_shake`; ammo 30 → 28 |

## G.3 — wave table and tuning (aesir)

| Check | Result |
|---|---|
| assets (`aesir-G-assets.py`) | `DT_Waves` rows `Wave_01`–`Wave_10`, `C_DamageFalloff` (flat 1.0), `DA_AesirTuning` → both, saved |
| `Wave_01.EnemyCount` set to 7 → `start_wave` | `enemies_remaining 7` (the game reads the table); restored to 5 |
| curve import | CSV rows `time,value` with no header (a header row became a key: value 0 at t=0 — found and fixed) |

## G.4 — Enhanced Input dash (aesir)

| Check | Result |
|---|---|
| `IA_Dash` in `IMC_Aesir` | `default_key_mappings.mappings` = `[(IA_Dash, LeftShift)]` (the old `mappings` property is deprecated) |
| `pie op=input key=LeftShift` | a `dash` event in the journal |

## G.1 — poly-world agent API (PIE)

| Check | Result |
|---|---|
| `IsPureOrConst`: `GetCapabilitiesJson`, `PeekSnapshotJson`, `GetEventsSince` / `ExecuteCommandJson` | true, true, true / false |
| capabilities | 21 commands with tiers (ephemeral / mutating / destructive), `world_epoch` |
| `PeekSnapshotJson` twice | the same revision (no side effects) |
| events | 5 `world.revised` events, no gap; an old-epoch cursor → `world_changed` |
| `step_cycles` (current epoch) / its repeat / an old epoch | accepted, cycle 4 → 6 / the identical response / `dedup_expired` |

## G.5 — HUD binding (aesir, plugin API 4)

| Check | Result |
|---|---|
| `asset_create kind=widget_blueprint` (parent `MCPHUDWidget`) + `widget_edit op=compose` (TextBlock `WaveText`) | created and composed, no issues — this failed before API 4 on UE 5.7 (hidden widget tree) |
| `FieldSourceBindings` = `[{WaveText.Text ← GameState.WaveNumber, IntToText}]` via `SetClassDefaultJson` | set; read back by `GetClassDefaultJson` with the enum by name |
| `MountWidget` in PIE, `start_wave` | the live tree (`DescribeLiveWidgets`) shows `WaveText` visible with text `1`; the game is on wave 1 |
| editor health after compose + compile | healthy (no "widget did not get a GUID" ensure once widgets are registered) |
| HighResShot of that frame | the UMG widget is **not** in it (Aesir's own HUD is canvas-drawn) — R0.2 row 11 corrected; R4.3 routes `ui=true` to the plugin's UI capture |
| compose `slot.position` (40,160) | not applied (text at 0,0) — R4 fix |
