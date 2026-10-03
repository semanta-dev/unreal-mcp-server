# Multi-Project System — Implementation Status

Tracks the implementation of `MULTI_PROJECT_SYSTEM.md` (the v19 design, gated to A by
a senior game-developer + a CTO). Everything below compiles and `go test ./...` is
green (24 packages). Phases follow the design's §9.

## ✅ Implemented, tested, and agent-gated — the daemon CORE (the novel, hard parts)

| Piece | Package | Realizes | Gate |
|---|---|---|---|
| **Liveness state machine** | `internal/editorpool` | §3 / §3.1 / §3.2 — the full (state×PID) machine: `Starting/Idle/Leased/Restarting/NeedsRelaunch/Unhealthy/Stopped`; standing PID-gated heartbeat (`RenewAlive`); atomic `CrashDetected` + `RemoveIfIdle` (TOCTOU-closed); expected-dead set; controlled-restart transitions (`RestartBegin/End/RepinSucceeded/RestartFailed`); recycled-PID identity guard; `NeedsKillBeforeTeardown` | **race A**; fidelity/coverage fixed (`RemoveIfIdle`, tests) |
| **uexec Session split** | `internal/uexec` (`discovery_shared.go`, `client.go`) | §2 — one shared `Discovery` (single multicast socket + node table + self-id) reused by N per-instance `Session`s via `NewOnDiscovery` (each with its own ephemeral `127.0.0.1:0` command channel + single-flight mutex). Additive: single-project path unchanged | **A-**; ephemeral-port default fixed |
| **Session→lease router** | `internal/daemon` (`router.go`) | §0 / §3 — `Attach` (lease-or-spawn, warm reuse = double-spawn guard, 1:1, idempotent, post-spawn double-check); `Resolve` (`NO_PROJECT_ATTACHED` / `LEASE_LOST` / `RESTART_IN_PROGRESS`, LeasedBy-checked); `Release` (kill-before-teardown for expected-dead); `Teardown`; `ReconcileReaped` | **B+ → fixes applied** (Release-kills-expected-dead, Resolve-RESTART_IN_PROGRESS, editors-reconcile) |
| **Reattach reconciliation** | `internal/daemon` (`reconcile.go`) | §6 — token-authorized: adopt re-adoptable, kill MY-token orphan (incl. the Launch-window cold-start orphan), never touch a foreign/human editor, remove-stale | **B+ → dedup fixed** (token-in-both-sources → one action) |
| **Build queue** | `internal/daemon` (`buildqueue.go`) | §7 — observable per-engine serializer (≤1 running/engine; queued = known-healthy so the stall-timer never false-cancels it); parallel across engines | comprehensive §7 unit tests |
| **Liveness loop** | `internal/daemon` (`runtime.go`) | §3 — the standing heartbeat/reaper tick: renew alive → reap dead → reconcile bridges → deliver LEASE_LOST | tested |
| **OS adapter** | `internal/daemon` (`adapters.go`) + `internal/lifecycle` (`ProcessIdentity`) | production `editorpool.Liveness` (real `IsAlive` + creation-time identity) | tested |

## ✅ Production integration — ASSEMBLED and boot-validated

The daemon is now fully wired and RUNS. `unreal-mcp -daemon-addr 127.0.0.1:PORT`
boots the StreamableHTTP endpoint; a boot smoke confirmed: shared discovery on the
correct multicast group (239.0.0.1:6766), MCP `initialize` handshake, **103 tools
registered** (full surface + `project_attach`/`project_release`/`project_list`),
per-session tool routing installed.

| Piece | Package | Realizes |
|---|---|---|
| **StreamableHTTP daemon** | `cmd/unreal-mcp/daemon.go` + `main.go` (`-daemon-addr`) | §0 — one process, `NewStreamableHTTPHandler`, a `ServerSession` per agent; does NOT `killOrphanSiblings` (siblings are the point); graceful shutdown + idle-session lease sweep |
| **Spawner bring-up** | `internal/daemonwire/spawner.go` | §6 — write-ahead intent → `lifecycle.Launch` with `-MCPInstanceToken` + ephemeral `127.0.0.1:0` → per-instance `uexec.NewOnDiscovery` session → wait-accepting (PID-gated) → `bridge.Bridge` `Editor`; kill-on-giveup |
| **Daemon assembly** | `internal/daemonwire/daemon.go` + `project_tools.go` | shared Discovery + Pool + Router + Runtime; the per-session `DepsResolver` (`request.GetSession().ID()` → lease → Bridge/Project/Jobs); the `project_*` tools; per-lease jobs registries |
| **Per-session Deps (MP3)** | `internal/tools/deps_context.go` + ~100 handler conversions | §4 — `structHandler`/`textHandler` resolve the bridge from ctx; custom handlers use `resolveDeps(ctx,d)` / `bridgeFromCtx(ctx,b)` with a fallback to the registration value, so the **single-project stdio path is byte-identical** (all 24 packages still green) |
| **OS liveness + identity** | `internal/daemon/adapters.go` + `internal/lifecycle` | production `Liveness` (real `IsAlive` + creation-time `ProcessIdentity` recycled-PID guard) |

## 🔒 Daemon-assembly gate — found + fixed (2026-07-03)

An adversarial gate on the assembly caught bugs a single-editor smoke can't (with one
node, the wrong-node fallback is coincidentally correct). All fixed + unit-tested:

- **BLOCKER — cross-tenant bind.** Under lease, `waitForNode` fell back to the first
  discovered node; during project B's cold start the only node is project A's editor,
  so B would bind A's editor (isolation break) + orphan B's real one. **Fix:**
  `uexec.Config.StrictNode` — a leased session's node wait returns `ErrEditorNotFound`
  instead of `nodes[0]` and keeps polling for its OWN node (applies to `WaitForNode`
  + `reconnectLocked`); the Spawner sets it. Single-project stdio keeps the fallback.
  Test: `uexec/strictnode_test.go`.
- **MAJOR — idle sweep vs running jobs.** The sweep reclaimed a lease on inactivity
  alone; a long async `build_compile` produces no tool calls, so it could yank the
  editor mid-build. **Fix:** `jobs.Registry.HasRunning()`; the sweep refuses while the
  lease has a running job. Test: `daemonwire/sweep_test.go`.
- **MAJOR — sweep/touch TOCTOU.** **Fix:** re-verify staleness under the lock
  immediately before `Release` (a concurrent touch rescues the session).
- **MAJOR — per-session jobs.** `build_tools` used the global `d.Jobs`. **Fix:** all
  job handlers now resolve `rd.Jobs` (per-lease).
- **MINOR — leaseJobs leak.** **Fix:** `PruneLeaseJobs` drops registries for removed
  instances (maintenance ticker). Test: `daemonwire/sweep_test.go`.

## ⏳ Remaining — LIVE validation only (MP0 / MP6, requires real editors)

Everything is implemented, unit-tested (24 pkgs green), and boot-validated. The one
remaining step needs actual UE editors (a human-run smoke, not a unit test):

- **Two-editor spike**: `-daemon-addr` up; agent A `project_attach` project X, agent B
  `project_attach` project Y; confirm each routes to its own editor (no cross-talk),
  `project_list` shows both, a crash in one delivers `LEASE_LOST` only to its holder,
  and a rebuild in one preserves the other's lease. Then re-run the P2–P7 tool smokes
  through the daemon on a leased session.
### Reattach executor — implemented + gate-hardened (§6 boot barrier)

`daemonwire/reattach.go` persists a `{token, project, pid, identity}` record per spawn
(write-ahead before Launch via an atomic temp+rename, upgraded after). `ReconcileAtStartup`
runs synchronously BEFORE serving `project_attach`. Ground truth is **records ∪ the live
process table**: `lifecycle.EnumerateTokenProcesses` reads each `UnrealEditor` process's
command line (via the PEB, `enum_windows.go`) to find those bearing a `-MCPInstanceToken`
this daemon minted. Via the tested `daemon.Reconcile` primitive it KILLS every
daemon-owned survivor of a crashed daemon and removes the records.

A **reattach gate** (blocker + edge facets) drove these fixes, all tested:
- **Process enumeration** (was the blocker) — closes the Launch→persist window: an editor
  already running but whose pid wasn't recorded is found by its live command-line token
  (pid 300 in `TestReconcileKillsOnlyEnumeratedOrphans`). Inherently immune to pid
  recycling (a recycled pid won't bear the token); a foreign daemon's token is ignored.
- **Kill-confirm + keep-on-failure** — `killConfirmed` re-verifies the pid still bears MY
  token, kills, polls `IsAlive`; on an unconfirmed kill the record is KEPT for a retry
  (`TestReconcileKeepsRecordOnKillFailure`), never miscounted.
- **Serialized atomic store** — `recordStore` mutex + temp+rename write + corrupt-file
  removal; `pruneDead` can't race a concurrent spawn's upgrade.
- **Stable state dir** — records live under `os.UserConfigDir()/unreal-mcp-daemon/records`,
  not `os.TempDir()` (temp cleaners could wipe them out from under running editors).

### §3.1 controlled restart — now wired into the daemon

A full C++ rebuild relaunches the editor, which under the daemon would break the
session's lease and orphan the new editor. `daemonwire/daemon.go` `RestartLease`
(exposed to tools via `Deps.Restart`) now routes daemon-mode relaunches through the
pool's §3.1 machine: `BeginRestart` (Leased→**Restarting**, so the holder gets
`RESTART_IN_PROGRESS` not `LEASE_LOST`, and the reaper skips the expected-dead state)
→ close+kill the old editor (releases the module DLL before Build.bat) → run the build
with no editor up → relaunch with the **same instance token** (reattach-tracked) →
`EndRestart` re-pins the lease onto the new editor. `build_tools.go` `runFullRebuild` +
`editor_restart` use it when `d.Restart != nil`; **single-project stdio is unchanged**
(`Restart == nil` → the legacy close+relaunch of a fresh process). A build/relaunch
failure tears the lease down cleanly (holder re-attaches). A gate caught + fixed a
blocker here: the old editor's kill is async, so `RestartLease` now **confirms death**
(`confirmDead` poll) before rebuilding — if it won't die it aborts (no DLL-lock rebuild
failure, no same-token duplicate/orphan); plus the new editor is force-killed if
`EndRestart` fails, and a post-build relaunch failure preserves the build diagnostics.
Tests: `router_test.go` (`TestControlledRestartPreservesLeaseAndRepins`),
`restart_test.go` (incl. `...AbortsIfOldEditorWontDie`, `...BuildFailureDropsLease`).

Remaining §6/daemon gaps (documented, entangled with unshipped work):
- Warm-ADOPTION across a restart (vs kill-all) needs the editor to advertise its token on
  discovery for unambiguous node matching (a plugin change).
- Per-lease-generation jobs + a server-push `LEASE_LOST` channel (holder already learns on
  its next call) — low impact.

## Design invariants the core upholds (verified by unit tests)

- **No false `LEASE_LOST`** across a controlled restart (incl. the minutes-long dead
  window), the `Leased→Restarting` TOCTOU, or a busy-but-alive editor.
- **A crash loses the lease; a failed rebuild does not** (→ retryable `NeedsRelaunch`).
- **No leak / no double-spawn**: warm reuse, kill-before-teardown for expected-dead,
  token-authorized reattach that never kills a foreign editor.
- **PID-gated reaper**: a live editor (Idle warm or Leased, whatever it's doing) is
  never reaped; only a genuinely dead process is.
