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
