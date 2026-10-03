# Working on unreal-mcp

Go MCP server for the Unreal Editor (v2). Read [`docs/architecture.md`](docs/architecture.md) first.

## Commands

```powershell
go vet ./... ; go test ./...                      # everything that needs no editor
go test -race ./...                               # needs a C toolchain (CI runs it on Linux)
bash scripts/coverage.sh                          # merged coverage gates
go generate ./internal/tools                      # after any tool/spec change: docs/tools.md, docs/migration-v2.md
UMCP_UPDATE_GOLDEN=1 go test ./internal/e2e -run SurfaceGolden   # after a reviewed tools/list change
.venv\Scripts\python -m pytest internal/bridge/py/tests
# ruff as CI runs it: the companion is linted as ONE concatenated module
cat internal/bridge/py/[0-9]*.py > /tmp/mcp2_bridge.py; ruff check --config pyproject.toml /tmp/mcp2_bridge.py
```

## Rules

- **Never write to stdout** in serving code: stdout is the MCP stream. Log to stderr (`slog`). CI greps for it.
- **Adding or changing a tool** happens in the spec table (`internal/tools/v2_*.go`):
  - give every op a tier, timing (sync ops ≤ 30 s, otherwise `Async`), `Required`/`Rejects`, `Reaches` (the companion
    ops it dispatches) and `Needs`;
  - keep the param vocabulary (`op`, `world`, `actor`, `class`, `location`/`rotation`/`scale`, `path`/`json`,
    `wait_s`);
  - a destructive or exec op may not share a tool with read-only/ephemeral ops — split it.

  `TestV2SpecsLint`, `TestEveryOpIsWired`, `TestToolListBudgets` and `TestGeneratedDocsAreCurrent` enforce this.
- **Companion ops** live in `internal/bridge/py/` (concatenated into one module):
  - register them in `99_dispatch.py` `_OPS` and classify them in `internal/tools/spec/pyops.go` (the tier lint and
    `TestPyOpsBijection` check both);
  - fail with `raise _V2Error(CODE, message, **details)` — never a silent fallback, never first-match;
  - wrap editor edits in `_transaction`/`_undoable`.
- **Test the op body**, not just the Go side: `internal/bridge/py/tests/fakeunreal.py` is a stateful fake `unreal`
  that raises on anything it does not model (UE 5.7's Python surface is narrower than the C++ API — e.g.
  `UClass.is_child_of` does not exist; use `MathLibrary.class_is_child_of`).
- **Errors and timeouts**: never retry a mutating call on `outcome: unknown`; a cancelled or refused shutdown never
  escalates to a kill.
- Breaking changes to tool names or arguments update `internal/tools/migration.go` (`V1Calls`) when they touch a v1
  mapping.

## Gotchas (UE 5.7 Python)

- `unreal.Rotator(a, b, c)` is **(roll, pitch, yaw)**; v2 vectors are `[pitch, yaw, roll]` (`_pyr_to_rotator`).
- `Paths.project_saved_dir()` is relative to Engine/Binaries — use `_saved_mcp_dir` (absolute) for anything Go reads.
- A `SceneCapture2D` spawned via `EditorActorSubsystem` renders the editor world, not possessed PIE (use
  `capture source=pie_highres` or the plugin's `game_scene`).
- Exiting a `ScopedEditorTransaction` on an exception commits it — clean up partial work before re-raising.
- Reflected property keys are snake_case (`gamestate.wave_number`); pin exact names with `properties`.
- Enums drop their `E` prefix in Python: `EMCPCaptureCamera` is `unreal.MCPCaptureCamera`.
- `ARFilter` fields are read-only on instances: build it in one constructor call (`unreal.ARFilter(**kw)`).
- `PlayerController.console_command` is not exposed: use `SystemLibrary.execute_console_command(world, cmd, pc)`.
- A Static-mobility root ignores `set_actor_location` in a game world (no error): check that a move landed.
- HighResShot writes a relative name under `Paths.screen_shot_dir()` (`Saved/Screenshots/WindowsEditor/`).
- `Blueprint.Status` and `bDisplayCompilePIEWarning` are invisible to Python; PIE's "Blueprint Compilation Errors"
  modal blocks the game thread (and every remote command) — the plugin's `PrepareBlueprintsForPIE` is the pre-flight.
- Remote execution: the editor ignores `open_connection` from a node+endpoint it believes is still connected (it does
  not notice a channel the client dropped) — the client sends `close_connection` before reconnecting.

## Live validation

`docs/operations.md` has the T4 checklist; `cmd/mcpcall` drives a real server from a JSONL script (stdio or `-url`
for the daemon). Run destructive checks on a scratch copy of a project with its own multicast group
(`RemoteExecutionMulticastGroupEndpoint` + `-group`) so no other client's editor is touched. The last run is
[`docs/validation/`](docs/validation/).
