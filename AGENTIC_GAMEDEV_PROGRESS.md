# AGENTIC GAME-DEV PLAN — EXECUTION PROGRESS

Tracks autonomous implementation of `AGENTIC_GAMEDEV_PLAN.md`.
Driver: Claude Code (Opus 4.8) orchestrating **codex-dispatch** implementers + a
three-reviewer **agent gate** (game-developer + CTO + 10x-engineer) that must each
grade a batch **≥ A−** before it is accepted. Iterate until all three agree.

## Environment (validated 2026-07-05)
- Module `github.com/jdziat/unreal-mcp-server`, Go 1.26.4. Baseline `go build ./...` clean.
- codex CLI `codex-cli 0.142.5` at `…/OpenAI/Codex/bin/codex`.
- Engine **UE 5.7** at `D:\Unreal\Engine\UE_5.7` (`UnrealEditor.exe` + `Build.bat` present).
- Game projects: `~/code/games/poly-world/PolyWorld` and `~/code/games/aesir-wave-defense`,
  both with the `UnrealMCP` plugin deployed under `Plugins/UnrealMCP/`.
- **Real fixtures:** `poly-world/PolyWorld/Saved/PolySlice/` — 9 overview PNGs +
  `slice_timeline.jsonl`. Confirmed real defect: `static:0` every frame (black render),
  `primaryLane:-1` throughout (metronome — the LANE decision never flips).
- CI gates: `gofmt`, `go vet`, stdout-purity (no `fmt.Print*`/`os.Stdout` in non-test
  `internal/`), `go test -race ./...`.

## Scope (locked with user)
1. **Pure-Go substrate — fully validated by `go test`** (the plan's self-validating wedge):
   all Phase-0 Layer-A audits, the Phase-0.6 `balance_sweep` struct harness + fenced-corner
   gate, the Phase-5 `design_explore` MAP-Elites core (planted-regime unit test), and tool
   registration. Falsifiable tests must FAIL on defect fixtures, PASS on fixed fixtures.
2. **Live-editor spikes — launched from `~/code/games` as needed** to validate against the
   REAL PolyWorld/Aesir projects (Phase-0 audits vs real captures; plugin C++ spikes).
3. **Deferred (needs humans, cannot validate here):** visual/persona calibration corpora,
   senior scaffold-authoring labor, pillar sign-off, blind A/B. Stubbed with clear markers.

## Shared contract
`internal/gametrace` — the trace/frame/event/config types every audit consumes, plus a
fixtures helper that loads the real PolySlice data and synthesizes defect/fixed variants.
Authored by the orchestrator so all codex workers build against ONE schema.

## Batches & gate status

| Batch | Packages / deliverables | Impl | `go test` | gamedev | CTO | 10x | Accepted |
|---|---|---|---|---|---|---|---|
| 0 audits + contract | `gametrace`, `lumaudit`, `primitiveaudit`, `stylecohesion`, `utilization`, `decisionaudit`, `feelaudit`, `renderhealth`, `audioaudit` | ✅ codex | ✅ -race | A+ | A- | A | ✅ (re-gate after snap/novelty/anchor fixes) |
| Real-fixture (in 0) | lumaudit + renderhealth assert REAL PolyWorld defects (FracBlack 0.986; static:0) | ✅ | ✅ | — | — | — | ✅ folded into Batch 0 |
| 0.6 balance | `internal/balance` (two-axis struct sim + full-sweep gate + fencing) | ✅ codex | ✅ -race | A- | A- | A- | ✅ |
| 5 explore | `internal/explore` (MAP-Elites + planted-regime unit test) | ✅ codex | ✅ -race | A- | A- | A- | ✅ |
| Registration | `internal/tools/design_tools.go` (13 tools) + `register.go` wire | ✅ codex | ✅ | A | A+ | A- | ✅ |
| asset_thumbnail (§3.1) | **SHIPPED tool** — `_op_asset_thumbnail` bridge op + `asset_thumbnail` Go tool + register wire | ✅ | ✅ live e2e | A | A+ | A | ✅ |
| **audio submix tap (§6.3)** | **SHIPPED** — plugin C++ `ISubmixBufferListener` → RMS envelope; `audio_capture_start/stop` + `play_test_sound` ops/tools; feeds `audio_audit` | ✅ | ✅ live PIE e2e | 🔧 | 🔧 | 🔧 | — |
| **input_inject (§6.2)** | **Already implemented** as `pie_input` (MCPControlSubsystem `CreateSimulated`+`InputKey`, routes to Enhanced Input); added validated `pawn_state` observation tool | ✅ pre-existing | ⚠ see note | — | — | — | code-verified |
| **retarget_setup (§3.4)** | batch-retarget MECHANISM validated in Python (`duplicate_and_retarget` produced a real retargeted anim); IK-rig AUTHORING (the flaky part) remains | ⚠ partial | ✅ mechanism | — | — | — | see note |

### Next-phase plan — the C++/PIE editor spikes (distinct from the shipped Python-drivable work)
All three need net-new UE C++ and/or PIE-with-audio/input validation — a heavier, version-sensitive
phase. The live loop is proven, so the harness is ready; the work itself is:
- **audio tap:** add an `ISubmixBufferListener` (UE-5.7 shape: `OnNewSubmixBuffer(submix, float*, numSamples,
  numChannels, sampleRate, audioClock)` + `GetListenerName`) registered via the main audio device on the
  master submix; thread-safe ring buffer → RMS/peak envelope JSONL. Expose `StartAudioCapture`/`StopAudioCapture`
  UFUNCTIONs on `MCPCaptureSubsystem` (mirrors the SceneCapture pattern), call from a Python bridge op. Rebuild the
  plugin; validate in an **Aesir** PIE session (it has a weapon fire sound) → non-zero RMS on the fire event, fed to
  the existing `audio_audit`. Best test project: aesir-wave-defense.
- **input_inject:** native op on `MCPControlSubsystem` routing `UPlayerInput::InputKey`/axis into live PIE; validate a
  scripted key sequence appears in a 60fps `verb_response` burst.
- **retarget:** drive IK Rig + IK Retargeter authoring from Python (present in `unreal.` APIs); validate the retargeted
  pawn's idle→walk→attack filmstrip through `in_motion_audit` (no foot-slide/snap). Plan flags this the flakiest.

### Deferred non-blocking nits (accepted batches, for a later polish pass)
- explore: `repurpose` operator is a no-op eating ~1/3 of the eval budget — drop from the draw or bias away (all 3 reviewers).
- explore: Gate-cache the plan implies is absent (`evaluate` runs a full sweep per candidate; runtime still 0.11s) — memoize on scaffold identity (CTO).
- balance: `axisLiveness` pools combat+economy into one ≥85% threshold — a per-axis floor would stop one axis free-riding (gamedev+10x).
- balance: `Simulate` lacks input guards for HP≤0/Spread≤0/BaseDamage≤0 (unreachable via `project`, but latent for external callers) (10x).
- explore fuel gate uses ≥2 donors vs the plan's L≥3 (≥2 *beyond* target) — reconcile the comment (gamedev).

### Live editor
- UE 5.7 at `D:\Unreal\Engine\UE_5.7`; PolyWorld + Aesir have the UnrealMCP plugin deployed. Go server `dist/unreal-mcp.exe` built.
- Real-fixture validation DONE against actual shipped PolyWorld capture (lumaudit FracBlack 0.986, render_health static:0) — ground-truth Phase-0 falsifier holds without a live editor.
- Baseline `PolyWorldEditor` build **succeeded** — UnrealMCP compiles against UE 5.7.
- **Live editor launched** headlessly from `~/code/games` (`UnrealEditor-Cmd -run=pythonscript`): booted UE 5.7, loaded the real `/Game/PolyWorld/Maps/Play` (6 actors). `MCPCapture` is a Runtime-phase module so its class isn't force-loaded in a bare pythonscript commandlet (expected UE behavior, not a defect).
- `render_health` validated against the **real shipped `DefaultEngine.ini`**: `BootsToGame=false` (real `GameDefaultMap=/Engine/Maps/Templates/OpenWorld`, the RC7 defect) + `StaticRendered=false` (58 real capture frames, all `static:0`).

## asset_thumbnail — SHIPPED as an MCP tool, validated live end-to-end ✅
Formalized from the spike into a real tool: `_op_asset_thumbnail` in the Python companion
(`mcp_bridge.py`, registered in `_OPS`) + the `asset_thumbnail` Go bridge-tool
(`internal/tools/perception_tools.go`, wired in `register.go`). Builds clean, `go test
./internal/tools/` passes, `py_compile` clean, A/B parity preserved (additive).
**Validated through the COMPLETE MCP path** (`dist/unreal-mcp.exe` → live UE 5.7 editor →
op): live facts for `SM_bank_002` (953 tris / 1783 verts / M_Material / bounds) + a rendered
PNG that passes our own `luminance_report` (non-degenerate). Op supplies its own key+fill
lighting (independent of the editor's map), polls the async `export_to_disk`, and cleans up all
spawned actors. **Known limit (honest):** lighting via spawned lights in an arbitrary map is
functional-but-basic (mesh recognizable, front faces can be shadowed); studio-quality
thumbnails are the plan's ThumbnailManager C++ isolated-preview-scene path (a noted upgrade).
Reference renders in `docs/spikes/`.

## (spike origin) asset_thumbnail — VALIDATED LIVE, END-TO-END ✅
Driven through **option 1**: the MCP server (`dist/unreal-mcp.exe -selftest`) connected to a
live GUI editor via UE Python remote execution (`bRemoteExecution=True`, multicast
239.0.0.1:6766), and `remote_execution.py` sent code straight into the booted editor — a fast
loop with no per-attempt reboots.
- **Hard facts (live, real assets):** `SM_bank_001` → 871 tris / 1661 verts / 1 LOD /
  slot "M_Material" / extent [359,374,323]; `SM_bank_002` → 953 tris / 1783 verts.
- **Render (live):** spawn mesh → 3/4-framed `SceneCapture2D` → `RenderingLibrary.
  create_render_target2d(..., RTF_RGBA8)` → `TextureRenderTarget2D.export_to_disk(PNG)` →
  **289 KB PNG of the low-poly bank building** (`docs/spikes/asset_thumbnail_SM_bank_001.png`).
- **Self-validating closure:** the rendered thumbnail passed our own `luminance_report`
  (MeanLuma 0.268, FracBlack 0.281, FracBlown 0.000, non-degenerate=true) — perception
  primitive validated by measurement primitive. The plan's §3.1 acceptance ("non-blank
  image + hard facts") is met on real data.
- **Plan-validating finding:** UE 5.7 Python does NOT expose `KismetRenderingLibrary`; the
  correct calls are `unreal.RenderingLibrary` + `TextureRenderTarget2D.export_to_disk`, and a
  render target MUST be `RTF_RGBA8` for PNG (float formats export EXR-only). This confirms the
  plan's rationale that the production `asset_thumbnail` render belongs in the plugin's C++
  capture path (`MCPCaptureSubsystem` already has SceneCapture + `MCPSavePNG`), with the
  Python path proven as the reference. Remaining spikes (audio tap / input_inject / retarget)
  are the same shape: drivable via this exact live-editor remote-exec loop.

## audio submix tap (§6.3, RC9) — SHIPPED, validated live end-to-end ✅
Real net-new **plugin C++**: `FMCPSubmixListener : ISubmixBufferListener` (UE-5.7 shape) registered on
the main submix via `FAudioDevice::RegisterSubmixBufferListener(TSharedRef, GetMainSubmixObject())`,
reducing each PCM buffer to `{t,rms,peak}` under a lock (audio render thread → game thread). Exposed as
`StartAudioCapture`/`StopAudioCapture` UFUNCTIONs on `UMCPCaptureSubsystem`, driven by
`audio_capture_start`/`audio_capture_stop` bridge ops + Go tools; `play_test_sound` triggers a cue.
- **Compiled** against UE 5.7 (fixed a real C4458 param-shadow error caught by the build).
- **Validated in a live Aesir PIE session** through the full MCP path: 213 PCM buffers captured;
  a played gunshot cue gave **max_rms 0.326** (non-silent); the envelope JSONL fed to **`audio_audit`
  → Pass=true, NotSilent=true, EventsCovered=1/1**. The measurement primitive certifies the real capture.
- **Gotcha recorded:** the `UnrealMCP` plugin exists as SEPARATE COPIES in the repo (`plugin/UnrealMCP`)
  and in each game project (`<game>/Plugins/UnrealMCP`); repo C++ edits must be synced into the game copy
  before building (I synced Aesir; PolyWorld's copy is NOT synced). A deploy/sync step is missing.
- Set `[Audio] UnfocusedVolumeMultiplier=1.0` in Aesir's DefaultEngine.ini so PIE audio isn't muted when
  the editor is unfocused (needed for headless-ish validation).

## input_inject (§6.2, RC8) — ALREADY IMPLEMENTED (pre-existing) + observation tool added
The draft plan treated `input_inject` as unbuilt, but `MCPControlSubsystem` **already provides
runtime frame-level key injection**: `InjectKeyByName`/`TapKey`/`HoldKey`/`ReleaseAll` via
`FInputKeyEventArgs::CreateSimulated(...) → PC->InputKey(...)`, exposed as the `pie_input` MCP tool.
The code comment confirms it drives "legacy AXIS (WASD) + action + Enhanced Input." So this spike is
**code-verified, not net-new work.**
- Added a genuinely-useful, **validated** observation tool: `pawn_state` (`_op_pawn_state` + Go tool) —
  reads the live player pawn's loc/velocity/speed. Confirmed live in Aesir PIE (read [300,0,98] → moving).
  This is the frame-level observable behind `verb_response`.
- **Clean movement validation is BLOCKED by the baseline projects, not the plugin:** in Aesir PIE,
  `pie_input` was rejected with "no player controller" (the control subsystem's PIE world had no
  resolvable PC), and the observed pawn auto-moves at 600 uu/s unprompted (Aesir game logic) — confounding
  a movement-delta check. PolyWorld is a tycoon with no character. So neither baseline game has a cleanly
  possessed WASD-movement pawn in its default PIE. A clean end-to-end validation needs a purpose-built
  test map (possessed character + a known movement binding); the injection + `pawn_state` observation
  primitives are both in place for it.

## retarget_setup (§3.4, RC6) — mechanism validated live; IK-rig authoring is the remaining flaky part
The plan ranks this the research-risk, most-likely-to-fail spike. Findings (live, Aesir):
- **The retarget tool-path EXISTS in Python** (`IKRetargetBatchOperation.duplicate_and_retarget`,
  `IKRetargeterController`) — contra RC6's "skeleton mismatch / no retarget has NO tool path at all."
- **Batch retarget VALIDATED end-to-end:** ran `duplicate_and_retarget([AS_walk], SK_body, SK_body,
  RTG_UndeadDraugr)` → produced a real retargeted anim asset `/Game/AS_walk_RTValidate` (confirmed
  exists=True; test asset then deleted). API quirks discovered: it wants `AssetData` (not loaded objects),
  and `get_asset_by_object_path` wants a path string.
- **What's NOT done (the valuable, flaky part):** a CROSS-skeleton retarget (making a bought character with
  a foreign skeleton usable) needs a target IK Rig for a *different* skeleton, then a retargeter linking
  source→target. Aesir has only ONE IK rig/retargeter, both same-skeleton (Draugr→Draugr), so there's no
  cross-skeleton pair. Authoring a target IK rig + retarget chains programmatically is exactly the
  "sparse, poorly-documented, commonly-soft-fails" surface the plan flags — it needs authored test content
  (e.g. a Mannequin IK rig) and is the genuine research bet, not attempted here. The mechanism to *run* a
  retarget is proven; the mechanism to *author* the rig for a foreign skeleton remains the open risk.

## Other net-new editor-C++ spikes — scoped, founded, NOT built
`asset_thumbnail` (ThumbnailManager + SceneCapture fallback), audio submix-buffer tap, `input_inject` (frame-level runtime input), `retarget_setup` — each is a separate editor-C++ subsystem needing implementation + a plugin rebuild + a live-PIE validation loop. Foundation is proven (plugin compiles; editor launches from the game projects); building and live-validating all four is a distinct workstream beyond this session. The pure-Go substrate does NOT depend on them — every audit runs headless over captured artifacts.

## Overall status
**Pure-Go substrate: COMPLETE and fully gated (all batches ≥ A−), validated by `go test -race` and against real shipped PolyWorld artifacts.** Human-corpus workstreams (visual/persona calibration, senior scaffold authoring, pillar sign-off, blind A/B) and net-new editor-C++ spikes remain, as scoped with the user.

Legend: ⏳ pending · 🔧 in progress · ✅ done · ❌ failed/blocked.
