# Held-out tasks — seal

The 5 held-out tasks of the game-making eval (plan §2, G.7) were written by the game-designer reviewer against the
frozen G interface contract ([`../../plans/GAME_CONTRACT.md`](../../plans/GAME_CONTRACT.md)) and sealed on
2026-10-03. Their plaintext is committed only with the final run; until then it is kept outside the repository and
has not been read by the implementer.

```
15d062052830550dda330011f3bf5682fa5bfc26b7ca2094e29bc1b0b332d571  heldout_tasks.json
```

5 tasks (`"held_out": true`), one per goal G2–G6, all multi-step, aesir ×4 and polyworld ×1; `tooleval -mode
live-lint` (with `-only` over the 5 ids, since a 5-task file cannot meet the per-goal counts) reported OK.

Deviation from the plan's order: the seal lands with G.7, while R1.1–R1.3 server work had already started (in the
same commit series, not merged); none of it touches the task files or the game contract the tasks rely on.
