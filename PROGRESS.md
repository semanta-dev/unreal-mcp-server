# Go Rewrite — Progress

Tracks execution of `GO_REWRITE_PLAN.md`. The Python server (`server.py` +
`unreal_bridge.py` + `remote_execution.py`) is **untouched** and remains the
default in `aesir-wave-defense/.mcp.json` (rollback path) until Phase 5.

Go toolchain: `%LOCALAPPDATA%\go-sdk\go1.26.4\bin\go.exe` (also on user PATH after
`scripts/bootstrap.ps1`). Build: `scripts/build.ps1`. `go test -race` needs a C
toolchain (mingw) not installed locally — runs in CI.

## Status by phase

| Phase | Status | Notes |
|---|---|---|
| **P0 Bootstrap** | ✅ done | pinned Go 1.26.4, module, static exe, `-version` |
| **P1 Protocol spike** | ✅ done (live gate pending editor) | `internal/uexec` + `cmd/uspike` + `internal/fakeeditor`; loopback multicast validated (`-tags integration`) |
| **P2 Bridge** | ✅ done | `internal/bridge` + `internal/snippets` (companion `mcp_bridge.py` v6); default `hotload` |
| **P3 Parity tools** | ✅ done, **gate A−/A− (2 rounds)** | 16 tools over official `modelcontextprotocol/go-sdk` v1.6.1; stdio server; stdout-pure |
| **P4 A/B parity** | ✅ harness ready (live) | `cmd/abparity` + `deploy/mcp.ab.json` |
| **P5 Cutover** | ✅ config ready (live) | `deploy/mcp.cutover.json` (Go on :6777, Python kept as `unreal-py` rollback) |
| **P6 Reliability** | ✅ done | reconnect/taint/timeout (uexec), `internal/jobs`, MCP recover/log middleware, throttle-off on connect, `internal/lifecycle`, AUTO_RELAUNCH watcher |
| **P7 Build orchestration** | ✅ done (live orchestration gated) | `internal/build` (diag parser + strategy classifier, unit-tested) + `internal/logtail`; `build_compile`/`job_status`/`job_cancel`/`project_ensure_open`/`editor_restart` |
| **P8 PIE + logs** | ✅ done (editor ops gated) | `pie_observe`/`pie_wait_until` (predicate unit-tested)/`pie_exec`/`pie_screenshot`; `logs_mark`/`logs_tail`/`logs_since` (logtail unit-tested) |
| **P9 Authoring + git** | ✅ done | git suite **tested against a real temp repo**; `apply_level_recipe`/`level_snapshot`/`level_diff`/`asset_info`/`asset_reimport`/`create_material_instance` (editor ops gated) |

**Tool surface: 39** (16 frozen parity + 23 e2e additions). All `go test ./...`
green; `go vet` + `gofmt` clean; `mcp_bridge.py` passes `py_compile` on 3.11.

## What "gated" means

Code that can only be exercised against a **live editor** (build_compile full
rebuild, PIE ops, authoring ops, the A/B diff) is implemented and its pure
sub-logic is unit-tested (diagnostic parser, strategy classifier, predicate
evaluator, log filters, git flow, jobs, lifecycle liveness), but the end-to-end
editor path awaits a run with the editor open — the same gate as P1's `uspike`.

## Intentional parity divergences (for the P4 A/B harness)

- `editor_status` adds `reachable`, `bridge_version`, `editor_pid`, `snippet_mode`
  (sanctioned additive fields — text/structure is a superset of Python's).
- `spawn_actor` returns **unrounded** location (matches Python `server.py`); `list_actors`
  rounds to 0.1 (matches Python); `get_actor` unrounded (matches Python).
- RPC text tools (`open_level`, `save_all`, `delete_actor`, `set_actor_transform`,
  `start_play`, `stop_play`) now surface captured editor `[Warning]`/`[Error]` output
  (like Python `format_output`) in addition to their structured `message` — order may
  differ from Python (warnings appended after the message); compare content, not byte order.
- `execute_console_command` is a PROTO raw-path tool returning `FormatOutput` (Python parity).

## Gate history

- **P3 gate** (2 rounds, A−/A−): fixed console-command output loss, list_actors object→array, and RPC
  text tools dropping captured `[Warning]`/`[Error]` (via `bridge.CallText`).
- **Final P6–P9 gate** (GameDev B+, CTO A, 0 CTO blockers): fixed two editor-side Python defects —
  `pie_observe` now coerces UENUM fields to their enumerator **name** (`_jsonable`) so `json.dumps`
  never fails the whole observe and `pie_wait_until` predicates match (enum names are UPPER_SNAKE_CASE
  in UE, e.g. `WaveState == 'IN_PROGRESS'`); `apply_level_recipe` now captures the recipe's `MISSING:`
  prints and exceptions into `missing_meshes[]`/`errors[]` and won't save a half-applied recipe.
  `_emit` also gained a `default=_jsonable` safety net. Bumped `mcp_bridge.py` to v6.

## Package map

```
cmd/unreal-mcp   stdio MCP server (P3) + AUTO_RELAUNCH + -selftest
cmd/uspike       P1 protocol go/no-go harness (run with editor open)
cmd/abparity     P4 A/B parity harness (spawns both servers, diffs)
internal/uexec        remote-exec protocol port (P1)
internal/bridge       semantic layer + companion-module install (P2)
internal/snippets     go:embed mcp_bridge.py v5 (single source of truth for version)
internal/tools        16 parity tools + build/pie/log/git/authoring/lifecycle tools + middleware
internal/build        Build.bat runner, MSVC/UHT/LNK diag parser, strategy classifier
internal/logtail      Saved/Logs incremental reader + severity/category filters
internal/jobs         async job registry (streamed progress, cancel)
internal/lifecycle    editor launch / PID liveness / uproject + target resolution
internal/gitutil      git command runner
internal/config       flags > env > defaults
internal/version      -ldflags build stamps
deploy/               mcp.ab.json (P4), mcp.cutover.json (P5)
```

## Live validation — DONE (2026-07-02, editor open, UE 5.7.4, L_Arena)

Validated against the running `aesir-wave-defense` editor:

- **P1** `uspike` PASS — loopback multicast discovery, project-aware node selection, TCP reverse-connect,
  engine version, **`__main__` persists = true** (confirms `hotload` default).
- **P2/P3** `unreal-mcp -selftest` OK — companion module v6 hot-loaded, sentinel + throttle-off, `editor_status`
  dispatched → real state (`current_level: L_Arena`, `editor_pid`, `bridge_version: 6`).
- **P4** `abparity` **5/5 parity-equal** — `editor_status` (superset), `list_actors` (48 actors), `list_assets`
  (superset), `execute_python` (text match), `take_screenshot` (**byte-identical**, 112860B).
- **Write path + CallText** (`-tags live` test) — `spawn_actor`→`get_actor`→`delete_actor` round-trip on a
  real PointLight, incl. the "Deleted …" text-tool message.

**Two real bugs the live editor exposed and I fixed:**
1. Companion-module install was mis-parsed as a *file path* by UE's ExecuteFile mode (the module's
   `mcp_bridge.py` header comment contains a `.py` token). Fixed by wrapping the source in a **base64
   bootstrap** (base64 output can't contain `.`), so the heuristic never triggers. (`internal/bridge/install.go`)
2. When the editor **drops** the command channel (e.g. a second client connects), the read error was
   classified `ErrProtocol` (not retried) instead of `ErrConnectionLost` → reconnect-once never fired.
   Fixed: only genuine JSON parse errors are protocol faults; every socket error is a lost connection.
   (`internal/uexec/command.go`, + `TestReconnectOnEditorDroppedConnection`)

## Remaining optional live steps (disruptive — do when ready)

1. Swap `aesir-wave-defense/.mcp.json` to `deploy/mcp.cutover.json` — P5 cutover (Python kept as `unreal-py` rollback).
2. Exercise `build_compile` (closes/reopens the editor on a full rebuild), `pie_observe`/`pie_wait_until`
   (needs PIE running), and `apply_level_recipe` in a real session.
