# Architecture

How a tool call travels from an MCP client to the Unreal Editor and back, and where each guarantee lives. The design
record is [`plans/OVERHAUL_PLAN.md`](plans/OVERHAUL_PLAN.md); this page describes the code as built.

## One request, end to end

```
client ──tools/call──▶ app (per-session mcp.Server, middleware: recover, log, session deps, toolsets, SafetyNet)
                        └▶ spec.Spec.handler: validate args (JSON Schema) → resolve op → per-op Required/Rejects
                             → approval gate (destructive/exec when a spec.Gate requires it; gate_policy=require is
                               spec.DenyGate — fail closed — until the cockpit approval UI is wired) → deadline (op Timeout,
                               timeout_s ≤ Max) → Handler
                                 └▶ bridge.Bridge: dispatch(op, base64-JSON args)
                                      ├▶ uexec: remote-execution command → "_mcp2_dispatch(op, args)" → stdout marker
                                      └▶ native (cockpit): framed RPC → MCPCore → _mcp2_dispatch_native → emit_result
                                 ◀── {ok, result | error, code, retryable, details}
                        ◀── envelope: structuredContent (+ text summary, + image content) or {"error": …}
```

- **Arguments are data, never code.** Go sends `op` and base64-encoded JSON; the companion `json.loads` them. No
  Python is assembled from user input (the `python` tool is the one, explicit, Exec-tier exception).
- **Every result is an object; every failure is one envelope** (`internal/tools/envelope`): a closed code set
  (`INVALID_ARGUMENT, NOT_FOUND, CONFLICT, PRECONDITION, PIE_NOT_RUNNING, EDITOR_UNREACHABLE, EDITOR_BUSY,
  OPERATION_FAILED, PYTHON_ERROR, UNKNOWN_OP, UNSUPPORTED, UNSUPPORTED_PLATFORM, TIMEOUT, CANCELLED, INTERNAL`),
  `retryable`, and `outcome` — `unknown` when a mutating call may have run before the connection or deadline was
  lost, so an agent never blindly repeats a non-idempotent change.

## The spec table (`internal/tools`, `internal/tools/spec`)

Each tool is a `spec.Spec`: name, toolset, description, a JSON Schema derived from a Go struct (`spec.SchemaFor`,
nullable types collapsed), `Replaces` (its v1 names) and a list of `OpSpec`s. An op declares its **tier**
(`readonly < ephemeral < mutating < destructive < exec`), `Idempotent`, `Async`, `Timeout`/`Max`, `Required` and
`Rejects` params, the companion ops it `Reaches`, and runtime `Needs` (pie, plugin, navmesh, project, engine).

Everything is derived from that table:
- MCP annotations (from the worst op), the approval gate (per op), the retry policy (read-only and idempotent ops may
  be re-sent after a dropped connection; others report `outcome: unknown`).
- `spec.Lint`: names and vocabulary, ≤ 8 ops, sync ops ≤ 30 s, `world=auto` only on read-only tools, no destructive
  op beside read-only ones, and a declared tier ≥ the worst tier of every companion op it reaches
  (`spec.PyOps`, which classifies every companion op, with argument escalations).
- `toolsets describe`, `docs/tools.md`, `docs/migration-v2.md` (`go generate ./internal/tools`).
- Tests: `TestMigrationAccounting` (all 155 v1 names), `TestToolListBudgets` (45 tools / 36 core; core tools/list
  ≤ 45 KB), `TestEveryOpIsWired` (every op end to end against the emulator; dispatched ops ⊆ `Reaches`).

**Async ops** return a job (`internal/jobs`) unless the caller passes `wait_s` (≤ 25 s), during which progress is
streamed as MCP progress notifications on that call's token. Jobs belong to the **project**, so any later session of
the same project can `job op=status/wait/cancel` them. A job result with `image_path` is attached as image content
when read.

**Toolsets** (`spec.Toolsets`) add or remove tools on the live per-session server (clients get
`notifications/tools/list_changed`); a call to a tool of a disabled toolset is a `PRECONDITION` naming the toolset.

## The bridge and the companion module

`internal/bridge` installs the companion — `internal/bridge/py/[0-9]*.py`, concatenated and embedded at build time —
into the editor's Python as the module `__main__._mcp2` (its own namespace, so a v1 companion can stay resident; a
version sentinel triggers a reinstall after an editor restart). Sections:

| File | Contents |
|---|---|
| `00_prelude` | imports, `_emit` (stdout marker, or the native sink with failure details in `result.details`) |
| `10`–`92` | the op bodies (core, PIE, level/assets, observation/reflection, capture recorder, scenes, viewport, discovery, authoring, UMG, instances, spatial) |
| `95_v2_core` | `_v2_world`, `_resolve_actor` (labels, paths, `@gamestate`/`@pawn`/`@controller`, PIE path translation, CONFLICT), `_resolve_class_v2`, actor ops with undo transactions |
| `96_v2_assets` | `asset_create` (validate everything, then replace), `asset_edit`, `reflect`, widget compose guard, server-owned renders |
| `97_v2_play` | PIE lifecycle, snapshots (World Partition aware), restore, tag-scoped scenes, output-path confinement, PIE-only PolyWorld |
| `98_v2_lifecycle` | `packages_state` (the full dirty set), `quit_editor` |
| `99_dispatch` | `_OPS`, `_mcp2_dispatch` (in-band `{"error"}` and `_V2Error` → coded failures), `_mcp2_dispatch_native` |

Failures carry a companion code (`NOT_IN_PIE`, `CLASS_UNRESOLVED`, `CONFLICT`, …) that the envelope maps onto the
closed set. The companion is tested in CPython against a stub and a stateful fake `unreal` module
(`internal/bridge/py/tests`), including a frozen v1 companion for the handover/rollback contract.

## The command channel (`internal/uexec`)

A Go implementation of Epic's remote-execution protocol: UDP-multicast discovery, then the editor connects back over
TCP. The node holds **one** command connection, so the session guards it: exactly-once dispatch for non-idempotent
ops, a liveness probe before sending, and a connection-loss decision table (write failure → resend; read loss after
a write → `outcome: unknown`; another client taking the slot → `EDITOR_BUSY` until `editor_lifecycle op=reclaim`).
A timed-out or cancelled command taints the channel; reconnecting to the same editor sends `close_connection` first,
because the editor ignores `open_connection` from a node it still believes is connected. A session can exclude nodes
(the daemon's spawner excludes every node known before its launch, and any that answers with another pid).

The companion's install sentinel is its version **and** a digest of its source, so a rebuilt server replaces the
module in an editor that outlived the previous server.

## Topologies

**stdio** (`cmd/unreal-mcp`, default): one server, one editor, discovered by project directory. `-auto-relaunch`
brings a vanished editor back.

**daemon** (`-daemon-addr`): StreamableHTTP; one MCP session per agent, each with its own `mcp.Server`
(`internal/app`). `project op=attach` leases an editor for the session (`internal/daemon`: router, sessions,
`supervisor.Pool` of editor instances, spawner, reattach records, a liveness reaper). Leases survive controlled
restarts (`RestartLease` with a `session.RestartPlan`: graceful stop, build step with no editor, relaunch with the
same identity, map reopened); a crashed editor's lease is dropped and the holder re-attaches. A project with a
running job keeps its editor while the session drains; a new session of the same project adopts it.

**cockpit** (`-cockpit on`, `internal/cockpit`): attaches to the UnrealMCP plugin's MCPCore channel — op results
over a framed socket, a browser control panel for approvals and a live feed. The browser URL carries the human's
access token in its fragment; tools never return it.

## Safety mechanisms

- **Safe shutdown** (`tools/v2_lifecycle.go`): refuse with `PRECONDITION` while anything is unsaved (unless
  `discard_dirty`), stop PIE, re-check, graceful `quit_editor`, kill only after 30 s with nothing unsaved (reported);
  a refusal or cancellation never escalates to a kill. Every launch passes `-AutoDeclinePackageRecovery` so no
  restore-packages modal can block an unattended editor.
- **Editor-aware `git_revert`**: checkpoints only (`umcp/cp/<n>` tags from `git op=checkpoint`), editor closed
  safely when reverted assets are loaded, file revert all-or-nothing from a backup.
- **Undo**: editor actor edits and scene/snapshot changes run inside a named `ScopedEditorTransaction`.
- **Modal dialogs**: `pie op=start` pre-flights Blueprint compile errors through the plugin
  (`PrepareBlueprintsForPIE`), and its wait polls with short pings; if the editor stops answering, a guard inspects
  the editor process's windows (Windows), cancels PIE's Blueprint-errors dialog (`WM_CLOSE`) and reports any other
  dialog that persists. A full build refuses while the project's editor runs but does not answer.
- **Server-owned outputs**: screenshots, captures, renders, thumbnails and audio go under `Saved/MCP` or
  `Saved/Screenshots`; caller-supplied names are validated.

## Package map

| Package | Role |
|---|---|
| `cmd/unreal-mcp` | flags, stdio or daemon wiring, cockpit launcher |
| `app` | builds a per-session MCP server (middleware, toolsets, gate) for both topologies |
| `tools`, `tools/spec`, `tools/envelope` | the tool table, the spec runtime, the error envelope |
| `bridge`, `bridge/bridgetest` | companion install + dispatch; the op emulator used by T1 |
| `uexec`, `uexec/uexectest` | remote-execution protocol; a fake editor on the wire |
| `session` | per-session deps (`Deps`, `RestartPlan`), project keys, project file |
| `daemon`, `supervisor`, `lifecycle` | leases and instances; process control, launch flags, liveness |
| `cockpit`, `cockpit/attach` | MCPCore client, gates, browser server |
| `jobs` | async jobs owned by projects |
| `snapshot`, `scenespec`, `eval`, `visual`, `audit`, `design`, `perf`, `projectmap`, `projectconfig`, `logs`, `build`, `crash`, `headless`, `desktop`, `calibration` | pure Go domain logic behind the tools |
| `archtest` | enforces the import DAG |
| `e2e` | T1 (in-memory MCP → real uexec → fake editor + emulator) and T3 (the built binary) |
