# Tool-selection eval — 2026-10-03

Plan §3.5: does consolidating 155 v1 tools into 45 v2 tools hurt an agent's tool use? Harness: `cmd/tooleval`;
tasks: `cmd/tooleval/tasks.json`; full numbers: [`full-merged.md`](full-merged.md).

**Scope.** This measures tool *selection and argument validity* against a stateful op emulator: whether an agent
picks the right v2 tool and fills it correctly. It does **not** measure whether the tools can make or judge a game —
no live editor, no real game, no gameplay outcome. That is the game-making eval of the remediation plan
([`../../plans/REMEDIATION_PLAN.md`](../../plans/REMEDIATION_PLAN.md) §2), run live on scratch projects.

## Result

| surface | runs | correct first tool | valid first args | end-to-end success (95% CI) | python calls/run |
|---|---|---|---|---|---|
| v1 (152 tools) | 189 | 97% | 100% | — | 0.02 |
| v2 (36 core + toolsets) | 189 | 97% | 99% | 100% (98–100) | 0.13 |

**Pass.** v2's first-call accuracy and argument validity are within the plan's 2-point margin of v1, and v2 agents
finish every task end to end. Cost: about $12 (prices as assumed in the report: $3/$15 per MTok).

## Setup

- **Model:** requests went through the user's Claude router (`/v1/smart-router/claude`), which picks the model by
  prompt complexity; every turn was served by `claude-sonnet-5` (recorded per turn in the results).
- **Tasks:** 63 — 48 derived from observed use (the surviving poly-world session's 58 Unreal calls, the PolyWorld
  tester agent's workflow, the documented workflows), 15 held out; 7 need an optional toolset, 9 are multi-step.
  Each task has a reference v2 solution that `-mode dry` replays; all 63 are solvable.
- **Runs:** 63 tasks × 3 runs × both surfaces. First call (right tool, schema-valid arguments) is scored on both;
  end-to-end success (world state, calls made, final answer) on v2, as a real agent loop (≤ 12 turns) against the
  stateful op emulator. The desktop tools are stubbed so nothing touches this machine's screen.

## Deviations from the plan

- **v1 is scored on the first call only.** The v1 adapter and its companion ops were deleted in P5e, so v1 cannot run
  end to end; its tools are the recorded v1 `tools/list` (`cmd/tooleval/testdata/v1_tools_list.json`).
- **One model, not two** — the router served Sonnet 5 for every request.
- **Fewer mined tasks** — only one session transcript survives, so 48 tasks are derived from observed use rather than
  45 mined one-to-one.
- **Python in the emulator does nothing** (it echoes), so a task solved only through raw Python fails end to end;
  the Python-fallback rate is reported separately.

## What the eval changed

1. **Optional-toolset discoverability (redesigned, re-run).** First pass: `heldout-luminance` 0/3 — agents never
   found `design_audit` in the `design` toolset and fell back to raw Python; `heldout-widget-text` found the `ui`
   toolset only on its last turn. The `toolsets` description now lists every optional toolset's tools, and `python`'s
   says to check for a dedicated tool first (core tools/list stays within its 45 KB budget). Re-run: both 3/3.
2. **Project-relative paths** (`analyze`, `playtest`, `design_audit`) resolved against the server's working
   directory; they now resolve against the project.
3. **Emulator fidelity:** PIE edits leaked into the editor level (shared property maps); fixed.

## Process notes

- 66 of the first pass's v2 runs ended on a router error (`signed thinking history has no retained native target`)
  when a long conversation moved to another target; the harness now drops the old target's thinking blocks and
  retries, and those runs were re-run (results files 2 and 3 replace them in the merge).
- Scoring adjustments, applied to both surfaces and re-scored from the recorded calls: `company-capital` succeeds on
  the correct answer (agents read the capital by reflection instead of `polyworld`); `record-gameplay` and
  `heldout-orbit` accept checking the editor / locating the target as a first call.
- Small v2 friction seen but not changed: agents sometimes pass `op` to `pie_observe` (which has none), the main cause
  of the 1-point argument-validity gap.

## Files

`pilot-*.jsonl|md` — the pilots; `full-20261003T221347Z` — first full pass; `full-20261003T223205Z` — re-run of the
router-errored runs; `full-20261003T223825Z` — re-run of the redesigned tasks; `full-merged.md` — the merged report
(later files replace earlier runs). Reproduce: `go run ./cmd/tooleval -mode dry`, then
`ANTHROPIC_BASE_URL=… ANTHROPIC_AUTH_TOKEN=… go run ./cmd/tooleval -mode full -models <model>`.

## Re-run after the remediation plan — 2026-10-04

The plan's G7 check (R0.1 for the `world` toolset move, R0.4's 12 new cases, and the final re-run): 75 tasks (the 63
above + 12 for `game`, `game_command`, `data_*`, `undo`, `world`) × 3 runs × both surfaces, Sonnet 5 through the
router (1 176 turns, all Sonnet 5). Report: [`full-merged-20261004.md`](full-merged-20261004.md) (the full pass
`full-20261004T141911Z` and two re-runs of the tasks changed below, which replace their runs). Cost $16.83 + $0.21.

| surface | tasks | correct first tool (raw / orientation-adjusted) | valid first args | end-to-end |
|---|---|---|---|---|
| v1 | 63 original | 96% / — | 100% | — |
| v2 | 63 original | 96% / **98%** | **100%** | 100% |
| v2 | 12 new | 81% / **97%** | 97% | 100% |
| v2 | all 75 | 94% / **98%** | **99.6%** | **100%** (98–100) |

**Orientation-adjusted** applies the earlier pass's adjustment ("accept checking the editor as a first call") as one
rule to every task, scored from the recorded calls: an `editor` status check first is accepted for a task that needs
PIE, and the read of the same data (`data_query` before `data_edit`, `actor_query` before `actor_edit`, `game` before
`game_command`) before a write. The raw numbers stay in the report. On the adjusted numbers G7 holds (≥ 97 %
first tool, ≥ 99 % valid args); on the raw ones the first-tool rate is 94 %. The v1 surface was not given the
new tools, so its 53 % on the new tasks is not a comparison.

**What the re-run changed:**
1. The 12 new cases' reference solutions were stale (R3's `asset` / `points`; enabling the optional toolsets) and
   the emulator had no `game_api` and no data ops: fixed, all 75 replay in `-mode dry`.
2. `world-scene-preview`: agents computed the grid themselves (1/3) — the `toolsets` catalogue now says `scene`
   previews a layout's placements: 3/3.
3. `game-recent-events`: agents read `logs op=events` (the editor log's events) as the game's and stopped (1/3) —
   that result now says whose events they are and points at `game op=events`; the `pie_observe` and `toolsets`
   descriptions name the game's own API for gameplay events: 3/3. (Core text paid for by shorter `@ref` lists.)
