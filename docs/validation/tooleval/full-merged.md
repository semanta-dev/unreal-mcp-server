# Tool-selection eval — full (merged) (2026-10-03 22:40Z)

63 tasks × 3 run(s) × 1 model(s) × 2 surfaces. First call scored on both surfaces; end-to-end success on v2 only (stateful emulator).

| model | surface | runs | correct first tool | valid first args | end-to-end success (95% CI) | python calls/run | errors | cost USD |
|---|---|---|---|---|---|---|---|---|
| claude-sonnet-5 | v1 | 189 | 97% | 100% | — | 0.02 | 0 | 3.07 |
| claude-sonnet-5 | v2 | 189 | 97% | 99% | 100% (98–100) | 0.13 | 0 | 9.01 |

**Models that actually served the turns** (a router may substitute):

- requested `claude-sonnet-5` → map[claude-sonnet-5:921]

Total cost: **$12.07** (prices: input/output per MTok as passed in -prices; cache reads 10%, writes 125%).

## Per task (v2 end-to-end successes / runs; first-tool hits v1 vs v2)

| task | v2 success | v1 first | v2 first |
|---|---|---|---|
| bp-component | 3/3 | 3/3 | 3/3 |
| bp-defaults | 3/3 | 3/3 | 3/3 |
| cleanup-probes | 3/3 | 3/3 | 3/3 |
| company-capital | 3/3 | 3/3 | 1/3 |
| console-stat | 3/3 | 3/3 | 3/3 |
| count-meshes | 3/3 | 3/3 | 3/3 |
| count-pie-class | 3/3 | 3/3 | 3/3 |
| create-bp | 3/3 | 3/3 | 3/3 |
| create-widget | 3/3 | 3/3 | 3/3 |
| default-gamemode | 3/3 | 3/3 | 3/3 |
| editor-status | 3/3 | 3/3 | 3/3 |
| full-rebuild | 3/3 | 3/3 | 2/3 |
| gameplay-tag | 3/3 | 3/3 | 3/3 |
| git-checkpoint | 3/3 | 3/3 | 3/3 |
| git-revert | 3/3 | 3/3 | 3/3 |
| git-status | 3/3 | 3/3 | 3/3 |
| heldout-demolish | 3/3 | 3/3 | 3/3 |
| heldout-duplicate-label | 3/3 | 3/3 | 3/3 |
| heldout-find-hurt | 3/3 | 3/3 | 3/3 |
| heldout-headless-tests | 3/3 | 3/3 | 3/3 |
| heldout-luminance | 3/3 | 3/3 | 3/3 |
| heldout-move-and-look | 3/3 | 3/3 | 3/3 |
| heldout-orbit | 3/3 | 3/3 | 3/3 |
| heldout-python | 3/3 | 3/3 | 3/3 |
| heldout-rename-tag | 3/3 | 0/3 | 3/3 |
| heldout-render-widget | 3/3 | 3/3 | 3/3 |
| heldout-road | 3/3 | 3/3 | 3/3 |
| heldout-shot-and-stop | 3/3 | 3/3 | 3/3 |
| heldout-snapshot-diff | 3/3 | 3/3 | 3/3 |
| heldout-spawn-verify | 3/3 | 3/3 | 3/3 |
| heldout-widget-text | 3/3 | 3/3 | 2/3 |
| image-diff | 3/3 | 3/3 | 3/3 |
| input-action | 3/3 | 3/3 | 3/3 |
| level-gamemode | 3/3 | 3/3 | 3/3 |
| line-trace | 3/3 | 3/3 | 3/3 |
| list-blueprints | 3/3 | 3/3 | 3/3 |
| list-scenarios | 3/3 | 3/3 | 3/3 |
| live-coding | 3/3 | 3/3 | 3/3 |
| livecoding-log | 3/3 | 3/3 | 3/3 |
| log-tail | 3/3 | 3/3 | 3/3 |
| mesh-thumbnail | 3/3 | 3/3 | 3/3 |
| nav-path | 3/3 | 3/3 | 3/3 |
| open-level | 3/3 | 3/3 | 3/3 |
| perf-parse | 3/3 | 3/3 | 3/3 |
| pie-call-gamestate | 3/3 | 3/3 | 3/3 |
| pie-jump | 3/3 | 2/3 | 2/3 |
| pie-screenshot | 3/3 | 3/3 | 3/3 |
| pie-set-health | 3/3 | 2/3 | 3/3 |
| pie-wait-wave | 3/3 | 3/3 | 3/3 |
| raise-wall | 3/3 | 3/3 | 3/3 |
| record-gameplay | 3/3 | 3/3 | 3/3 |
| reflect-class | 3/3 | 3/3 | 3/3 |
| replace-bp | 3/3 | 3/3 | 3/3 |
| restart-editor | 3/3 | 3/3 | 3/3 |
| run-scenario | 3/3 | 3/3 | 3/3 |
| save-all | 3/3 | 3/3 | 3/3 |
| select-frame | 3/3 | 3/3 | 3/3 |
| snapshot-roundtrip | 3/3 | 3/3 | 3/3 |
| snapshot-then-move | 3/3 | 3/3 | 3/3 |
| stop-play | 3/3 | 3/3 | 3/3 |
| topdown-shot | 3/3 | 3/3 | 3/3 |
| viewport-move | 3/3 | 3/3 | 3/3 |
| window-capture | 3/3 | 3/3 | 3/3 |
