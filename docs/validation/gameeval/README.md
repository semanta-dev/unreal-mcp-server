# Game-making eval

The acceptance test of the remediation plan ([`../../plans/REMEDIATION_PLAN.md`](../../plans/REMEDIATION_PLAN.md) §2):
agents do multi-step game tasks through a real server, against scratch copies of aesir-wave-defense and poly-world in
a live editor, and are judged only mechanically. Unlike the [tool-selection eval](../tooleval/README.md), this
measures whether the tools can make and judge a game.

## Tasks

[`tasks.json`](tasks.json): a Python `prelude` shared by every probe, and the tasks. Each task has a goal (G1–G6), a
project, a prompt, optional harness `setup` calls (run after the reset, before the agent; `python` setup code gets the
prelude — e.g. to record a baseline value the checks compare against), and checks — each exactly one of:

- **probe**: Python the harness runs after the agent stops; its last `RESULT {json}` line is matched against
  `expect` (`eq ne gt gte lt lte contains exists` on a dot path);
- **called**: the agent made a successful call to `tool` (`op`), at least `min` times, after a successful `after`
  call (so "a playtest ran *after* the edit" is checkable);
- **answer**: the final answer contains a number within `tolerance` of a probed value, or matches `regex`.

`no_python: true` fails a run that called the `python` tool at all (G2). There is no LLM judge.

The 20 plan tasks name phase G's game interfaces (`DT_Waves`, `DA_AesirTuning`, `C_DamageFalloff`, `IA_Dash` in
`IMC_Aesir`, `UAesirAgentSubsystem`; poly-world's `UPolyWorldAgentSubsystem`); those names are frozen into the G
interface contract at G.7. The **5 held-out tasks** are written by the game-designer reviewer at G.7 and sealed then:
their file's SHA-256 is committed before R1 starts, the plaintext only with the final run.

`go run ./cmd/tooleval -mode live-lint` checks the file (structure, every goal G2–G6 ≥ 3 tasks, ≥ 8 multi-step); CI
runs it.

## Running

```bash
# credentials as environment variables only (never in a file in the repo)
ANTHROPIC_BASE_URL=… ANTHROPIC_AUTH_TOKEN=… go run ./cmd/tooleval -mode live -label "baseline v2.0.2" \
  -server dist/v2.0.2/unreal-mcp.exe -models claude-sonnet-5 -runs 3 -out docs/validation/gameeval/baseline
```

Each run: a fresh stdio server for the task's project → `git_revert` to the project's baseline checkpoint (made at
start, or `-checkpoints`) → `ensure_open` → setup → the agent loop (≤ `-live-turns` 40) → probes. A run whose cost
passes `-cost-cap` ($1.50) or whose turns run out **fails**; once the eval has spent `-eval-cap` ($150) the remaining
runs are aborted (and fail). A task passes when ≥ 2 of its 3 runs pass. Pass bar (final): ≥ 20/25 tasks, each of
G2–G6 ≥ 2 of its tasks, ≤ 0.1 Python calls per run. The served model is recorded per turn and each turn is priced
at the model that served it (runs before 2026-10-04 19:00Z priced the whole run at its most expensive served model).

Paid runs other than pilots of ≤ 3 tasks need the user's approval each time.

## Results

- `pilot/` — R0.4's 3-task harness pilot on v2.0.2.
- `baseline/` — v2.0.2 on the post-G game copies (G.7).
- `final/` — after R6.

### Final record pass 1 — 2026-10-04 (`final/live-20261004T213016Z.*`)

Server and harness at `97368d0`; per-run cost cap raised to $4 (eval cap $300) by the user's decision (the plan
says $1.50; the router serves Opus on some turns, at 5× the price). **16/25 tasks pass; pass bar NOT MET**: G6 has
1 passing task (needs 2) and python calls are 0.55/run (needs ≤ 0.1). $56.49; served sonnet-5-5 1131 turns, opus-5-5 12.

| goal | tasks | pass | failing |
|---|---|---|---|
| G1 | 1 | 1 | |
| G2 | 6 | 5 | ho_pw_cheapest_research |
| G3 | 4 | 3 | ho_aesir_dash_skirmish |
| G4 | 6 | 3 | aesir_player_damage, aesir_wave2_more, ho_aesir_endgame_waves |
| G5 | 4 | 3 | ho_aesir_hud_score |
| G6 | 4 | 1 | aesir_feel_audit, aesir_ttk, aesir_batch_variance |

What the failures were (held-out tasks are not analysed beyond their check names):

- aesir_player_damage, aesir_ttk: agents measured by playing (pie start / aim / fire, game events) instead of a
  `playtest op=run`, which the checks require.
- aesir_batch_variance: no seeded run cleared wave 1 — the scenarios never aimed (playtest input steps can).
- aesir_wave2_more: proving wave 2 needs wave 1 cleared; the run that passed did it, two hit the turn/cost limits.
- aesir_feel_audit: 2 of 3 answered the audit's median latency (0 ms) and the probe disagreed; under investigation.
- ho_aesir_hud_score: its `bound` / `hud` checks have the shape of the two plan-task checks found unpassable this
  day (a CDO `bindings` property; live_tree `text` at the top level); the held-out file is sealed and was not changed.
- python: 41 calls — reading `playtest.json` and writing scenario files (playtest evidence: `analyze op=events`
  exists), reading data assets, editing C++ sources (no source-writing tool exists).

Harness and task fixes made on the way here (each applies to any later run, baseline included): the reset stops PIE
and removes files agents created (untracked) with an editor restart and a UBT rebuild when sources changed — earlier
passes ran with earlier runs' widgets and with agent C++ compiled into the Aesir module; each turn is priced at the
model that served it; a rolling prompt-cache breakpoint; the system prompt says the work is checked afterwards; task
fixes for aesir_start_wave (opening intermission), aesir_hud_enemies (prompt + a probe that could not pass),
pw_road_connect (no existing roads to connect to), aesir_hud_wave (a check that could not pass) and aesir_aim_kill
(pie op=aim counts). Earlier, superseded passes are kept uncommitted in `final/diagnostic/` (not a record).

### Final record pass 2 — 2026-10-04 (`final/live-20261004T234623Z.*`, stopped at 58 of 75 runs)

Server and harness at `2a08dad` (pass 1's fixes plus: the feel audit's per-channel delays and medians — it reported
only the first response on any channel, which a shot's sound makes 0 ms; playtest results naming `analyze op=events`
/ `op=rubric` for their `playtest_path` and, when the player hit nothing, the aim input step; probe values recorded in
the results). Same caps ($4/run). Stopped by the user's decision to release on "the tools work" (below) with
batch_variance (1 run), broken_beat and four held-out Aesir tasks not run. $45.17, python 0.48 calls/run.

Of the 19 tasks that completed, **15 pass**; of the 18 plan tasks that completed, 15 pass — aesir_player_damage 0/3
(the agents measured by playing after the edit; one ran a `playtest` before it and read it with `analyze op=events`),
aesir_ttk 1/3 (one run excluded an outlier kill the task's average includes: 0.35 s answered, 0.96 s by the task's
definition), aesir_wave2_more 1/3 (cost and turn limits before wave 2 was proved). aesir_feel_audit went 0/3 → 2/3
with the audit fix. The held-out ho_pw_cheapest_research failed 3/3 as in every pass.

### Release decision

The plan's pass bar (≥ 20/25, each of G2–G6 ≥ 2, python ≤ 0.1/run) was **not met** in either record pass (16/25 in
pass 1; pass 2 incomplete). v2.1.0 is released on the user's criterion that the tools work: every tool added or
fixed here was validated live against the scratch editors without an LLM (`cmd/mcpcall` scripts) and by the test
suites; the remaining eval failures are agent choices (manual play instead of `playtest`, an outlier excluded), cost
limits under the router's model choice, and held-out tasks whose checks could not be examined. The v2.0.2 baseline
(1–2/25) was not re-run under the final harness, so the baseline/final comparison mixes harness versions.

The held-out plaintext is committed with this result: [`heldout_tasks.json`](heldout_tasks.json) (SHA-256 `15d06205…`,
as sealed in [`HELDOUT.md`](HELDOUT.md)). ho_aesir_hud_score's `bound` / `hud` checks read a CDO `bindings` property
and a top-level live_tree `text`, the two shapes found unpassable in the plan tasks; they were not changed.
