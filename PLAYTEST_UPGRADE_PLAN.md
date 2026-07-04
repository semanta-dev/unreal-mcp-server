# Unreal MCP Server — Playtest Capture, High-Level Design & Editor Integration Plan

Produced by a multi-agent design workflow (3 designers → 3 adversarial critics →
synthesizer), grounded in the real code. Extends `GO_REWRITE_PLAN.md`. Serves three goals:

- **(A) Multi-screenshot playtest capture** — a filmstrip/contact-sheet + a synchronized
  timeline of observed state, so an agent can do an in-depth play test and validate the game.
- **(B) Consistent high-level design tools** — a declarative scene-spec, prefabs, lighting
  presets, layout primitives, camera framing, and a design-lint.
- **(C) Tighter editor integration** — viewport/selection control, richer editor state,
  reflection-driven observation (game-agnostic), named undo batches.

`_MCP_BRIDGE_VERSION 6 → 7`. All new ops additive; the 16 frozen parity tool names and
`editor_status`'s byte-compatible output are untouched. Injection-safe base64 dispatch
preserved. `go.mod` stays minimal (montage uses only stdlib `image`/`image/draw`/`image/png`).

## Central technical decision — capture frame source

A `SceneCapture2D` spawned via `EditorActorSubsystem` lives in the **editor** world (matches
the existing `take_screenshot` gotcha), NOT the PIE game world. Therefore:

- **`scene_capture`** (SceneCapture2D → render target → PNG): the confident, backgrounded-safe
  default for **editor/simulate** worlds and design validation (multi-angle contact sheets).
- **`pie_highres`** (`AutomationLibrary.take_high_res_screenshot`, the proven `pie_screenshot`
  path): the correct source for exposure-correct **possessed-PIE** gameplay frames (async, low cadence).

## Hard constraints honored

1. Editor logic lives in `mcp_bridge.py` as `_op_*` in `_OPS`; Go ships base64-JSON args.
2. The command channel is **single-flight**. High-frequency capture is owned by an **in-editor
   tick recorder** (`register_slate_post_tick_callback`) that buffers frames+state to disk with a
   manifest; Go collects everything in **one** `capture_stop`. No Go per-frame polling.
3. Editor is often backgrounded — see frame-source decision above.
4. `unreal.Rotator(a,b,c)` is `(roll,pitch,yaw)`; `rotation_pyr` args are `[pitch,yaw,roll]`;
   UENUM reads as enumerator NAME. Sun pitch must be `< 0` or the level renders black (validated).
5. Every gated phase ships its pure sub-logic as unit-tested Go with no editor.

## Phases (value/risk-ordered: pure & testable first)

| Phase | What | Gated |
|---|---|---|
| **P1** | `internal/montage` — stdlib contact-sheet builder (tile 1:1, mark cells, tiny 5×7 font, timeline merge) | no |
| **P2** | `internal/scenespec` (spec→plan compiler: parse/validate/layout/prefab/env/diff) + `internal/framing` (orbit/frame poses) | no |
| **P3** | extract `internal/predicate` from `pie_tools.go`; add `internal/rubric` (temporal pass/fail over a timeline) | no |
| **P4** | reflection-driven observe — `_reflect_observe`, `_op_reflect_object`, `pie_observe` v2 (drops the wrong `_GS_ALLOWLIST`) | yes |
| **P5** | in-editor recorder `_op_capture_{start,poll,stop,list,poses}` + Go `capture_*`/`scene_contact_sheet` | yes |
| **P6** | declarative realize `_op_scene_{apply,clear,bounds}` + `_op_design_probe`; Go `scene_apply`/`scene_clear`/`env_preset_apply`/`design_check` | yes |
| **P7** | `_op_viewport_{set,get}`/`focus_actors`/`select_actors`/`get_selection`/`editor_state`; Go `viewport_tools.go` | yes |
| **P8** | `playtest_capture` orchestrator (Go-only): open→play→capture→beats→stop→montage+rubric+logs → one image + timeline + verdict | yes |
| **P9** | `_op_transact` — one atomic named undo batch (optional) | yes |

Reflection-observe (P4) is prioritized because the current `_GS_ALLOWLIST` is **provably wrong**
for the aesir game (lists `IntermissionTime`, which does not exist; misses `WaveClearGoldAmount`/
`IntermissionEndTime`) and hardcodes aesir-specific names, so capture quality for any other game is broken.

New Go packages: `internal/{montage,scenespec,framing,predicate,rubric}`.
New Go tool files: `internal/tools/{reflect_tools,capture_tools,scene_tools,viewport_tools,playtest_tools}.go`.

Live-editor validation of the gated phases is left to the user's editor session (the project's
established gate model), since hijacking the live command channel would drop the user's active MCP clients.

_Full synthesized plan text: `.../tasks/wlovlxrhc.output`._
