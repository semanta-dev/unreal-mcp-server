# Multi-Concurrent-Project System — Design (v19)

> **Status: gated to A by both a senior game-developer and a CTO** (19-round adversarial
> design review against the real `editorpool`/`bridge`/`uexec`/`build_tools` source). This v19
> also folds in the reviewers' A+ polish (recycled-PID guard at the reattach kill, write-ahead
> intent at every `Launch` site + stable persistence location, Case-E intent cleanup,
> non-disruptive ownership probe).


**Goal.** Let multiple agentic AIs work on *different* Unreal projects concurrently
so agent A building `aesir` and agent B building `poly-world` never see each other's
editor, files, discovery traffic, or failures.

**Revision history.** v1 got the decomposition right but hand-waved (and mis-stated)
the parts that actually collide. v2 grounded it in the real transport/ports/discovery
and led with the transport decision. v3 closed the busy-editor single-flight liveness
gap (§3) and introduced §3.1 (lease continuity across a controlled restart). v4 made
§3.1's re-pin code-correct at the handoff seam (kill by pool PID; `RestartEnd(newPID)`;
hold until async rediscovery rebinds `uexec.Session.nodeID`; explicit `Restarting` flag).
v5 added §3.1's failure path (build failure / unconfirmed kill / re-pin timeout →
`LEASE_LOST`, cold-start-bounded wait, `LastHealthy` on hold-clear). v6 made the failure handling total via an unconditional watchdog (single terminal guard
subsuming build-fail / `Launch`-error / kill-unconfirmed / never-accepting), command-channel
*accepts* polling, and `LeasedBy`-before-`MarkUnhealthy`. v7 split the watchdog deadline (build phase vs
re-pin), blocked holder calls during the hold (`RESTART_IN_PROGRESS`), and persisted
`Restarting`. v8 tried a forward-progress stall timer + Job Object +
idle-sweep exemption. v9 moved liveness to known state (daemon build
queue + PID-death re-pin). v10 made false timing-positives cheap
(`NEEDS_RELAUNCH`, lease kept) and PID-gated the reaper. v11 built the liveness **state machine**
(§3.2): a heartbeat renewing every PID-alive instance plus an explicit expected-dead set
`{Restarting, NeedsRelaunch}` (a PID-alive heartbeat cannot cover a PID-dead-but-lease-held state).
v12 completed it at the **transition** level (atomic `Leased→Restarting`, the `NeedsRelaunch`
relaunch-grace fix window, identity-travels-with-`Launch`), retiring the false-`LEASE_LOST` and
wedge classes. v13 introduced kill-before-teardown for the
expected-dead set, the atomic locked `CrashDetected`, accepting-ready `Lease()`, and the `Starting`
row. v14 made kill-before-teardown truly universal
across all in-session states (`Starting` supervisor included). v15 moved reattach to reconcile against live
discovery. v16 made reattach race-free (write-ahead
spawn intent + process-table∪discovery reconciliation). v17 made reattach kills token-authorized +
reconciliation a barrier. **v18** (this) tightens the ownership test from "has A token" to **"has MY
token"** — a token from another daemon's token-space matches none of this daemon's persisted
intents/records and is therefore FOREIGN and never killed (write-ahead guarantees every self-owned
orphan already matches an intent, so the tightening loses no self-orphan); a discovery-only kill uses
the live `os.getpid()` PID and a probe that can't confirm ownership never kills (safety-first); the
warm-`Idle` accepting-probe re-checks `state==Idle` under the lock (TOCTOU-symmetric to
`CrashDetected`); a stale persisted-but-absent record is `Remove`d; and lease-BINDING alongside a
foreign same-project editor is explicitly scoped to §10. **Invariant: every (state × PID) cell AND
transition, in-session AND across a daemon restart (race-independent), has one defined rule — no
false `LEASE_LOST`, no wedge, no leak, no double-spawn, and no foreign/other-daemon/human editor ever
killed; a routine C++ compile error costs a retry, not the lease.**

---

## 0. The governing decision: transport (this is NOT a footnote)

The server today runs `srv.Run(ctx, &mcp.StdioTransport{})` (`cmd/unreal-mcp/main.go:85`).
**stdio is 1 client : 1 process : 1 session.** Two Claude Code agents each spawn
their *own* `unreal-mcp.exe` over their own stdin/stdout — they cannot share one
in-process `editorpool`. So "multiple AIs through one server" is *impossible* on the
current transport. There are exactly two ways to make it real, and the choice drives
everything below:

- **A — Shared daemon (recommended).** Run ONE long-lived `unreal-mcp` over the
  go-sdk **StreamableHTTP** transport (the only transport that multiplexes concurrent
  sessions — `StreamableHTTPHandler`). Both agents' MCP client configs point at
  `http://127.0.0.1:<port>`. The per-connection `*ServerSession` (`request.GetSession()`)
  is the **tenant key** → lease. The daemon owns editor lifecycle. **`killOrphanSiblings`
  (`main.go:46`) must be removed** — under a daemon there are no rogue siblings, and
  the current logic would have a second launch terminate the first.
- **B — Server-per-agent + out-of-process broker.** Keep stdio (one server per agent)
  but move editor arbitration into a separate always-on broker (lockfile / named-pipe /
  tiny arbiter daemon) that owns editor spawn, InstanceID/port allocation, and lease
  grants. The in-process `editorpool` gives **zero** cross-process safety (two servers =
  two independent pools = double-spawn + port collisions), so without a broker this path
  is unsafe. `killOrphanSiblings` must also be redesigned (siblings are now legitimate).

**Recommendation: Model A.** One daemon, HTTP transport, session-keyed leases. The rest
of this doc assumes A and notes where B differs.

**Session → lease lifecycle (designed, not a risk).** Model A only works if the daemon
reclaims a lease when its agent's session ends. Requirements: (1) run StreamableHTTP in
**stateful** (non-stateless) mode so `GetSession().ID()` is stable across a session's
requests and usable as the lease holder key; (2) observe session close via a supported
signal — an HTTP `DELETE` on the session, plus an **idle-timeout sweep** (no request in
N minutes → `Release` the lease) as the backstop, since go-sdk's internal
`onTransportDeletion` hook is test-only and can't be relied on. On release the daemon
frees the lease but keeps the warm editor for re-lease (§7). A crashed agent that never
sends DELETE is caught by the idle sweep.

**The idle sweep MUST respect an in-progress restart/build.** `build_compile` is async — it
returns a job id immediately (`build_tools.go:41`) — so an agent that fires a multi-minute
rebuild and stops polling has a genuinely *idle session* but is NOT abandoned. The idle sweep
must therefore refuse while the holder has **any running job it started** (keyed on
`jobs.Registry` ownership — restart, full build, *and* `project_ensure_open`, which is itself a
`reg.Start` job with up to a 300 s launch wait, `build_tools.go:199`), not an enumerated
restart/build subset. `Pool.Release` no-ops for such an instance. Otherwise the sweep flips it
to Idle out from under an in-progress job — a healthy-path lease drop, and it could even be
re-leased for the same project mid-rebuild. The daemon knows the agent is waiting on a job it
started; that session is not a crashed idle lease.

---

## 1. What actually collides on one host (corrected isolation table)

The v1 "linchpin" (a per-instance `-MessagingUDPMulticastEndpoint`) was **the wrong
knob** — that configures UdpMessaging (MessageBus), not the PythonScriptPlugin
remote-execution channel uexec uses. Here is what truly needs per-instance identity,
in priority order:

| Rank | Resource | Reality (code) | Per-instance fix |
|---|---|---|---|
| **1** | **Reverse-connect command port** | The command channel is a REVERSE connect: the bridge listens (`defaultCommandAddr = 127.0.0.1:6776`, `config.go:18`) and tells the editor to dial back (`advertisedEndpoint`, `command.go:76`). Two bridges on 6776 → the editor's connect-back races to whichever listener the OS hands it = cross-project channel or silent wedge. | Give each leased bridge `CommandAddr = 127.0.0.1:0` — an **ephemeral, loopback-bound** port (not `:0`, which `listenTCP` would bind on all interfaces via `0.0.0.0` + `SO_REUSEADDR`, `sockopt.go:74`). `advertisedEndpoint` already forces the loopback host and advertises the *actually-bound* port, so this is a one-line, already-supported change. Drop `SO_REUSEADDR` for leased ephemeral listeners — it was only a crutch for the `killOrphanSiblings` fixed-port wedge, now gone. **This is the primary tenant resource.** |
| **2** | **Editor↔server binding** | Discovery multicast group is `239.0.0.1:6766` (`config.go:16`) — Epic's remote-exec is *designed* for many editors + clients on one group, disambiguated by node id + advertised `project_root`. `pickNode`/`nodeMatchesProject` (`node.go:105,129`) already filter by project. | For two **different** projects, no multicast offset is needed at all — filter by project. Pin `bridge→node` by **node id** at lease time (see §3). |
| 3 | Same-project two editors (a NON-goal today) | Two editors on the *same* project can't be told apart by `project_root`. | Only if ever needed: per-instance `-ini:Engine:[/Script/PythonScriptPlugin.PythonScriptPluginSettings]:RemoteExecutionMulticastGroupEndpoint=<group>` **and** point that session's `uexec.Config.MulticastGroup` at the same group. Verify live — this is the real knob, not the Messaging flag. |
| 4 | Filesystem | `Saved/PyMCP/events.ndjson`, `.mcp/{scenarios,snapshots}`, crash dir, log — per project already. | Namespace capture/snapshot output under `.mcp/<instanceID>/` to prevent cross-writes when two leases share a project root (rare). |
| 5 | GPU / VRAM | Shared across all editors on the box. | Part of the capacity model (§7), not isolable — it's a *cap*. |

---

## 2. uexec.Session decomposition (the real refactor cost #1)

`uexec.Session` is built around exactly ONE command channel, ONE `cfg.ProjectDir`,
ONE reverse-connect port, and a single-flight mutex. "N bridges" naively means N
Sessions each opening its own multicast discovery socket — wasteful and collision-prone.

**Split it:** a *shared* discovery layer (one multicast socket, one node table observed
by all leases) + a *per-instance* command connection (its own ephemeral `CommandAddr`,
its own single-flight mutex, pinned to one node id). One discovery feed fans out to N
editors; each bridge owns only its command conn. This is the concrete shape of
"1 bridge per leased editor."

---

## 3. Lease lifecycle (corrected against editorpool semantics)

Two real bugs in the v1 "heartbeat on every call":

- **Keepalive vs reaper race.** `Pool.Heartbeat` recovers `Unhealthy→Idle` (`editorpool.go:133`)
  **and clears `LeasedBy`** (`editorpool.go:135`). If a busy lease goes briefly silent,
  `ReapStale` marks it Unhealthy; the next "heartbeat" then *steals the lease from its owner*.
  → **Separate liveness from ownership:** add `LeaseRenew(id, holder)` that refreshes
  `LastHealthy` only for the matching holder and changes neither state nor `LeasedBy`.
  (Under the standing-PID-heartbeat model, §3.2, there is no transient `Unhealthy→Idle`
  auto-recovery — a live editor is never marked Unhealthy in the first place, and `Unhealthy`
  is terminal-crash only.) A reaped lease returns `LEASE_LOST` to the holder, which must re-attach.
- **No pickNode fallback under lease.** `pickNode` falls back to `nodes[0]` ("first
  discovered", `node.go:141`) and `waitForNode` to `nodes[0]` (`discovery.go:182`) when
  no project match is found before timeout. In multi-project a slow-cold-starting
  poly-world session would attach to aesir's editor → cross-tenant corruption.
  → In leased mode, **pin `bridge→node` by node id at lease time and disable the
  fallback** (return `ErrEditorNotFound`); never re-resolve by project on reconnect.
- **Editor liveness — a standing PID heartbeat for the ALIVE states, plus an explicit
  EXPECTED-DEAD state set for the rest (§3.2 is the complete table).** The command channel is
  single-flight (`client.go:18`; `RunCommand` holds the mutex for the whole call, `client.go:110`),
  so during a long command (up to `CommandTimeout`, default **120 s**, `config.go:24`), a build, a
  Live-Coding compile (a 120 s log-poll with no command on the channel, `build_tools.go:111`), or a
  cold start, no in-band ping runs. A **standing background goroutine renews `LastHealthy` (via
  `LeaseRenew`) for every PID-alive instance — `Idle` (warm pool, §7) OR `Leased`** (`IsAlive`,
  `liveness_windows.go:14`) — so `ReapStale` never marks a *live* editor stale (this fixes both
  the busy-editor false-reap AND the warm-Idle-editor RAM leak). But a heartbeat over PID-ALIVE
  instances **cannot cover an instance whose PID is legitimately DEAD while the lease is held** —
  `Restarting` (editor killed for a rebuild) and `NeedsRelaunch` (build failed / relaunch not yet
  driven). So the honest model is NOT "one mechanism, no exemptions": it is **heartbeat (alive) +
  an explicit expected-dead set `{Restarting, NeedsRelaunch}`** that BOTH `ReapStale` AND the
  crash-detector skip. Crash = PID-dead AND state ∉ expected-dead. See §3.2.
  - *PID-identity guard:* Windows recycles PIDs, so `IsAlive(pid)` alone can false-positive after
    a crash (the OS reassigns the dead editor's PID). Capture the process **start-time/image-path
    at spawn** and verify it before renewing (or require the reverse-connect channel to still ACK),
    so a recycled PID can't mask a crash. TCP `SO_KEEPALIVE` (only `TCP_NODELAY` today,
    `command.go:58`) is an optional secondary signal.
- **InstanceID reuse.** Post-refactor (ephemeral ports + shared discovery group), InstanceID
  is a **filesystem-namespace / `jobs` key**, not a port — so reuse-after-death is about
  namespace collision, not port ownership. Still, free an id for reuse **only after confirmed
  process death** (PID check / kill-then-confirm) so a reaped-but-running editor's namespaced
  output (`.mcp/<id>/`, its jobs) can't be clobbered by a new lease. (`Pool.Heartbeat` clears
  `LeasedBy` at `editorpool.go:135`; the pool is not yet wired into `main.go` — these
  semantics apply once the daemon integrates it.)

### 3.1 Lease continuity across a controlled editor restart (the long-build case)

The single-flight exemption above is necessary but **not sufficient**, because this
codebase's own long operation isn't an in-flight command at all. A full rebuild
(`build_compile` full → `runFullRebuild`) and `editor_restart` **deliberately
SAVE-QUIT-FORCE-KILL the editor** (`build_tools.go:308`), run `Build.bat` as an
out-of-process `jobs.Registry` job for *minutes* (`build.RunFull`, `build_tools.go:170`),
then **relaunch a fresh editor** (`build_tools.go:179`). During that window the editor PID
is dead, no command is in flight, and `LastHealthy` goes stale — so every §3 safeguard
false-negatives and the reaper would kill the lease + fire `LEASE_LOST` during a perfectly
healthy rebuild. Worse, the relaunched editor gets a **new remote-exec node id**, which
collides with the "pin by node id, never re-resolve" rule — the lease could never re-adopt
its own editor.

→ **A controlled restart must not look like a crash — but a *failed* restart must.** The
protocol is sequenced to the real code seams (`lifecycle.Launch` returns only a PID,
`lifecycle.go:59`; the fresh editor's node id appears on multicast discovery only after a
10-60 s cold start; a killed node sweeps out of the node table within `NodeTimeout` = 5 s
default, `config.go:22` / `discovery.go:127`, so 5 s ≪ cold start means a holder-scoped
rediscovery that *excludes the just-killed node id* can't race onto the stale node):

1. **`RestartBegin(id)` — set an explicit hold flag** on the `Instance` (a `Restarting`
   bool), fired at the actual kill site *inside* `runFullRebuild` / `editor_restart` **after**
   `build_compile`'s auto→full classification/escalation (`build_tools.go:84`) — NOT inferred
   by scanning `jobs.Registry` (a `jobs.Job` has no type tag or holder association, and
   `auto` can't be classified at job start, so a Live-Coding job would wrongly exempt a live
   editor). While set, `ReapStale` and out-of-band liveness **ignore** this instance.
2. **Kill by the pool-authoritative PID, and confirm death before relaunch.** Force-kill
   the `Instance.PID` captured at spawn (or §6's persisted PID), **not** an in-band
   `editor_status` query — that returns 0 on any Call error (`build_tools.go:319`), precisely
   the wedged case `editor_restart` exists for (`build_tools.go:206`), which would no-op the
   kill and let `Launch` spawn a *second* same-project editor. **If the old PID is not
   confirmed dead** (`IsAlive == false`) within the kill wait, treat the restart as FAILED
   (go to the failure path) rather than relaunching into a duplicate.
3. **Arm the restart watchdog immediately after the confirmed kill — UNCONDITIONALLY, not
   gated on a successful relaunch.** This is the invariant that makes the design total: once
   the editor is confirmed dead (step 2), a single watchdog owns the outcome and MUST end the
   restart in exactly one of two states — *re-pinned live* (clear the hold) or *terminal
   failure* (clear the hold + `LEASE_LOST`). It is never left `Restarting`. The watchdog uses **two
   different liveness bounds**, because the window it owns spans two phases that CANNOT share
   one wall-clock number:
   Liveness for both phases rests on **KNOWN STATE, never on timing/output inference** — because
   every inferred or wall-clock bound is defeated by concurrency (v6-v9 each merely relocated the
   same concurrency-scaled false-cancel: total-duration → silent-mutex-wait → cold-start-under-load).
   Known state gives concurrency nothing to defeat:
   - **Build phase (kill → `Launch`): the DAEMON owns an observable build queue; do NOT infer
     health from Build.bat output.** Model A's single daemon already owns every build, so it must
     **serialize builds itself in an in-daemon queue** rather than lean on `Build.bat -WaitMutex`
     (`run.go:47`) — whose queue wait is *silent* (UBT prints one "Waiting for another instance…"
     line, then blocks on the OS mutex via `WaitOne()` for the full duration of every build ahead,
     which is exactly why a stall timer over stdout STILL false-cancels a healthy queued build).
     With a daemon-owned queue, a *queued* build is **known-queued** (the daemon put it there) —
     never cancelled, at any concurrency. The daemon runs **≤1 build per engine** (admission-
     controlled, §7), so `-WaitMutex` contention never even arises. Only the one *actively-running*
     build is watched, by a **stall timer over its flowing compile output** (`build.RunFull` streams
     each line to `progress()`, `run.go:66`): a genuinely hung *compile* stops emitting → cancel. A
     queued build inherits no silence because it is not being watched for output at all.
   - **Re-pin phase (armed at `RestartEnd`): terminal only on confirmed PROCESS DEATH, not a fixed
     300 s.** Cold start slows under load (N editors relaunching contend for CPU/RAM/VRAM; §5 notes
     background throttle), so a fixed 300 s (`ensureOpen`, `build_tools.go:266`) would
     false-`LEASE_LOST` a healthy-but-slow relaunch. Instead poll discovery + `editor_status`
     (every 3 s, as `ensureOpen`, `build_tools.go:271`) **while the launched PID is alive**, and
     declare terminal failure only when that PID is **confirmed dead** (the relaunch genuinely
     failed). A slow-but-alive cold start just keeps waiting; a generous absolute ceiling may
     backstop a stuck-but-alive process, but the primary signal is PID death, not a clock.

   **Cancel must kill the whole toolchain tree.** `build.RunFull` runs `cmd.exe /c Build.bat`
   (`run.go:55`); ctx-cancel kills only `cmd.exe`, leaving UBT (dotnet) + `cl.exe`/`link.exe`
   grandchildren compiling and **still holding the `-WaitMutex`**. Spawn the build under a Windows
   **Job Object with `KILL_ON_JOB_CLOSE`** (or `taskkill /T /PID` on cancel) so a stall-cancel tears
   down the whole tree and releases the mutex.

   The watchdog runs `RestartEnd(id, newPID)` (set `Instance.PID = newPID`) when `Launch` returns
   a PID; a running build that hangs (stall timer), a build that fails, or a `Launch` that errors —
   which the job must **propagate** (today `runFullRebuild` swallows it with `return res, nil`,
   `build_tools.go:180`; the watchdog needs `return res, err` so the guard fires) and signal to the
   watchdog **immediately** (a Launch error is instant + deterministic, `lifecycle.go:46`/`:49`/
   `:54`) — all fall through to the terminal guard. Nothing slips past it.
4. **Re-pin: poll rediscovery + command-channel readiness, terminal only on relaunch-PID death
   (armed at `RestartEnd`, not at kill).** A holder-scoped loop — `waitForNode(projectDir)`
   **excluding the just-killed node id**, then `OpenCommand` — polls until the fresh editor is
   both discovered AND its reverse-connect command channel *accepts* (a node appearing on a
   discovery pong does NOT mean it accepts reverse-connects yet — `ensureOpen` polls
   `editor_status` every 3 s for exactly this, `build_tools.go:271`). It keeps polling **while the
   relaunch PID is alive** (per the build-phase note above: PID death, not a 300 s clock, is the
   terminal signal, so a slow-but-alive cold start under load isn't false-dropped; 5 s `NodeTimeout`
   ≪ any cold start means the stale node is long gone). It rebinds the pin — which lives in
   **`uexec.Session.nodeID` (`client.go:21`), not `editorpool.Instance`** (the pool has no
   node-id field). On success, **refresh `LastHealthy = now` and clear `Restarting` in the
   same step** (`ReapStale` is purely `LastHealthy`-based, `editorpool.go:164` — it never
   consults PID, so the out-of-band PID-liveness loop must also write `LastHealthy` via
   `LeaseRenew`).
5. **Nothing but the watchdog touches the pin during the hold.** A holder tool call arriving
   mid-restart must NOT enter `Session.RunCommand` → `reconnectLocked` (`client.go:146`), whose
   5 s `DiscoveryTimeout` + project-match fallback would race the watchdog on `s.mu` and could
   re-pin `s.nodeID` to the still-lingering killed node. While `Restarting`, holder calls return
   a soft **`RESTART_IN_PROGRESS`** (or block on the watchdog); only the watchdog writes
   `uexec.Session.nodeID`.

**Terminal handling — ONE guard, but failure PRESERVES the lease (only a genuine crash loses
it).** The recurring lesson (v6-v10) is that some liveness bounds are IRREDUCIBLE: a *hung*
compile and a *slow-under-contention* compile are both PID-alive and both silent — not
separable by known state; likewise an editor that comes up alive-but-never-accepting vs one
that is merely slow to accept. You cannot make those timing bounds perfect. So the fix is not a
sharper bound — it is to make a false positive **cheap**: the watchdog (step 3) converts *any*
outcome that isn't a re-pinned live channel — build failure (`build_tools.go:171`/`:174`),
`lifecycle.Launch` erroring (`lifecycle.go:46`/`:49`/`:54`, which `runFullRebuild` must propagate
not swallow at `:180`), a kill that can't confirm death (step 2, escalate-then-retry before
declaring so), the running-build stall timer firing, or the re-pin bound elapsing on an
alive-but-not-accepting editor — into a **lease-PRESERVING `NEEDS_RELAUNCH` sub-state**, NOT
`LEASE_LOST`. The lease stays with the holder (editor-less); the holder re-drives it with
`project_ensure_open` (relaunch) under the SAME lease. So a routine C++ compile error — the
*modal* iterate-on-code case — costs the agent nothing but a retry, and a false stall-cancel or
a slow-cold-start ceiling is likewise a retry, never a dropped lease.

**`LEASE_LOST` is reserved for a genuine crash**: an editor whose PID dies *without* a
controlled kill (detected by the standing PID heartbeat, §3), i.e. a real crash the holder can't
recover by relaunch. On that: **capture `LeasedBy` FIRST** (`MarkUnhealthy` clears it,
`editorpool.go:150`), then `MarkUnhealthy` + `LEASE_LOST` + re-attach. The invariant is now:
a restart ALWAYS terminates — into re-pinned-live, or a retryable `NEEDS_RELAUNCH` (lease kept),
or `LEASE_LOST` (only a real crash) — **never a lingering `Restarting`, and never a false lease
drop from an irreducible timing bound.** `Instance` gains `PID` (already present) + a
`Restarting`/`NeedsRelaunch` state; the node id stays in the Session; the standing PID heartbeat
(§3) means no leased+alive editor is ever reaped mid-build/restart.

Cross-tenant safety is unaffected throughout (leases are project-scoped); this closes
single-agent continuity across the agent's own header-change rebuild — happy path AND failure.

The `project_ensure_open`-under-lease retry that recovers a `NeedsRelaunch` lease must get the
**same instrumentation as `RestartEnd`** — today it is an un-pool-aware job (`build_tools.go:244`):
on `Launch` it must immediately set `Instance.PID = newPID` (so the heartbeat renews during the
up-to-300 s cold start), run the holder-scoped re-pin (`waitForNode` excluding the old node id →
rebind `Session.nodeID` → `OpenCommand`), and on success atomically refresh `LastHealthy` + clear
`NeedsRelaunch`. Until `NeedsRelaunch` clears, the instance keeps its expected-dead exemption.

### 3.2 Instance liveness state machine (complete — no undefined cell)

Liveness is governed by (state × PID), so every case has ONE defined rule and there is no cell
for a reviewer to find un-covered:

| State | PID alive | PID dead |
|---|---|---|
| `Starting` | own accepting-wait supervisor (not heartbeat/reaper): accepting → `Idle`/`Leased`; ceiling-while-alive → KILL then `Remove` | spawn failed → `Remove` |
| `Idle` (warm) | heartbeat renews; AND — safe here since no in-flight command — a periodic ACCEPTING-probe kills+`Remove`s a warm editor that fails to ACK for a bounded window (no wedged-alive warm slot). The probe-kill re-acquires the pool lock and confirms `state==Idle` first (if `Lease()` moved it to `Leased`, abort — symmetric to `CrashDetected`'s TOCTOU close) | confirmed-dead → `Pool.Remove` (cleanup; frees RAM) |
| `Leased` | heartbeat renews → not reaped | **crash** → `MarkUnhealthy` + `LEASE_LOST` (capture `LeasedBy` first) |
| `Restarting` | transient (re-pin, or alive-but-not-accepting) → held; teardown KILLS first | **expected** (killed for rebuild) → reaper + crash-detector SKIP; watchdog owns it |
| `NeedsRelaunch` | transient (relaunched, not yet accepting) → held; teardown/recovery KILLS first | **expected** (build failed / not yet relaunched) → reaper + crash-detector SKIP; idle-sweep (not `ReapStale`) reclaims after the relaunch-grace TTL |
| `Unhealthy`/`Stopped` | (not entered by intra-project recovery — that routes through `Restarting`, §6) | terminal (a real crash); `Remove` on confirmed death, `LEASE_LOST` already fired |

So: a live editor (Idle or Leased) is never reaped; an expected-dead editor (`Restarting`/
`NeedsRelaunch`) is never mistaken for a crash; only a `Leased`+dead-and-not-expected editor is a
crash (`LEASE_LOST`). The heartbeat covers the two ALIVE rows; the expected-dead SET
`{Restarting, NeedsRelaunch}` covers the two dead-but-held cells; `LEASE_LOST` fires in exactly one
cell.

**Cell rules aren't enough without transition rules — the seams that matter:**
- **`Leased→Restarting` is a TOCTOU on the crash-detector.** `IsAlive` (`liveness_windows.go:14`)
  is a bare `OpenProcess` done *outside* the pool lock; if the detector reads `state==Leased` under
  the lock, releases it, then probes liveness while `RestartBegin` concurrently flips `Restarting`
  and kills the PID, it acts on a stale `Leased`+fresh-dead reading → false `LEASE_LOST` on the
  modal path's first step. Rule: **`RestartBegin` sets `Restarting` under the pool lock, and the
  kill happens strictly after that write**; the crash-detector, upon finding a dead PID,
  **re-acquires the lock and re-reads `state==Leased`-and-not-expected before deciding `LEASE_LOST`**
  (a double-check). Then `state==Leased` provably implies kill-not-initiated implies PID-alive.
- **The `NeedsRelaunch` *fix window* must be sweep-protected.** The modal loop is: build fails →
  `NeedsRelaunch` → the agent reads diagnostics and edits `.cpp`/`.h` with its OWN filesystem tools
  (ZERO unreal-mcp calls, and the failed build job is `Completed`) → `project_ensure_open`. §0's
  idle-sweep refuses only while a job is *running*, so it would reclaim this genuinely-silent
  session mid-fix and drop the lease — the exact "compile error costs a retry, not the lease" claim,
  failing again. Rule: **entering `NeedsRelaunch` arms a daemon-owned "relaunch-grace" timer** (a
  distinct, generous TTL, NOT the short generic idle-N — a fix can take many minutes) that the
  idle-sweep's job-ownership guard respects; only after the grace expires may the sweep reclaim.
- **PID-identity travels with every (re)spawn.** The recycled-PID guard's captured start-time/
  image-path must be **re-captured at each `lifecycle.Launch`** (as an atomic step of `RestartEnd`/
  `ensure_open`, alongside `PID=newPID`) — else the heartbeat compares the *new* editor's PID
  against the *old* editor's identity → mismatch → a false crash after every successful rebuild.
- **Kill-before-teardown is UNIVERSAL for any ALIVE-BUT-NOT-ACCEPTING instance, in ANY state.**
  The generalized invariant (v13 scoped it too narrowly to `{Restarting, NeedsRelaunch}`): an
  instance can hold a **live PID that never accepts** (startup modal / shader-precompile hang) in
  THREE states — `Starting` (a fresh `project_attach`/§7 pre-warm spawn), `Restarting` (a relaunch
  in the re-pin window), and `NeedsRelaunch` (an `ensure_open` retry). In every case, **any
  teardown, reclaim, relaunch, OR collapse must `IsAlive(Instance.PID) && Kill && confirm-death`
  FIRST** (the §3.1-step-2 primitive): (i) `project_ensure_open` recovery kills the old possibly-
  live-but-stuck editor before `lifecycle.Launch`, or it double-spawns a second same-project editor
  (the untracked first orphans RAM/VRAM *and* answers discovery pongs on the shared group — §1
  rank-1/3 cross-talk); (ii) the `NeedsRelaunch` idle-sweep reclaim kills before `Remove`; (iii) the
  `Restarting` absolute-ceiling backstop kills before collapsing; (iv) `Starting`'s accepting-wait,
  on give-up (never-ACK within a generous ceiling while PID-alive), kills before `Remove`/retry —
  `Lease()` refuses a non-accepting instance, so a naive retry would otherwise double-spawn; (v) the
  §6 daemon-restart collapse **also kills first** (its old "nothing live to re-adopt" premise is
  FALSE in the re-pin/cold-start window, where `Instance.PID` is a fresh LIVE editor that survives
  daemon death by design). Rule of thumb, generalized: **alive-but-not-accepting, in ANY state,
  never implies safe-to-Remove/relaunch/collapse without a kill-first.** `Starting` is governed by
  its own bounded accepting-wait supervisor (symmetric to re-pin: poll accepting WHILE PID-alive up
  to a ceiling; PID-death → `Remove`; accepting → `Idle`/`Leased`; ceiling-while-alive → kill →
  `Remove`), NOT the generic heartbeat/`ReapStale` — so a slow cold start isn't false-reaped and a
  wedged one isn't leaked.
- **`Lease()` hands out only an ACCEPTING-ready instance; the crash-mark is one atomic op.** `Lease()`
  returns only an instance whose command channel has been seen to ACK (not merely PID-alive) — so a
  warm `Idle` editor that is alive-but-cold (not yet accepting) or died-while-idle isn't leased into
  a transient first-call failure or an instant `LEASE_LOST` (a `Starting`/cold instance stays out of
  the lease pool until accepting-ready; §7 pre-warm hides the latency). And the crash-detector's
  double-check must be a **single locked pool method** — `CrashDetected(id, pid)` that, under one
  `p.mu` hold, re-reads `state==Leased`-and-not-expected, marks unhealthy, and returns the prior
  `LeasedBy` — so the "re-read state then `MarkUnhealthy`" is atomic (two separate lock acquisitions
  would reopen the very `Leased→Restarting` TOCTOU the ordering above closes).

```
project_attach(project, session) → Pool.Lease(project, sessionID)
     │  miss? spawn editor (ephemeral CommandAddr, project ini), Register, Lease
     ▼
 [Leased] pinned to node id ── tool calls route to bridge(instance) ──┐
     │  LeaseRenew(id, holder) refreshes liveness (never steals)       │
     ▼                                                                 ▼
 project_release / session close → Release          ping-timeout/crash → MarkUnhealthy
                                                      → LEASE_LOST → holder re-attaches
                                                      → kill+confirm before id reuse
```

---

## 4. The Deps→resolver refactor (real cost #2 — architectural, not clerical)

Handlers capture a concrete `*bridge.Bridge` / `Deps` at registration across all
`register*` factories; `d.*` is read in ~99 sites across 17 files. Per-session routing
needs **per-CALL** resolution, so this is an architectural change:

- Define a resolver contract `func(ctx) (Deps, *bridge.Bridge, error)` with explicit
  `NO_PROJECT_ATTACHED` and `LEASE_LOST` error codes.
- Middleware injects the request's session into ctx; the resolver maps session→lease→
  `(Deps, Bridge)`. Thread a per-lease `*ProjectContext{ProjectDir, InstanceID}` into
  **every internal package that touches disk** (crash, logtail, events, capture,
  scenario, snapshots) — none may read a process global.
- `jobs.Registry` must be scoped per lease (or keyed by InstanceID) so build/headless
  jobs don't leak across projects.

Offline tools (`project_map`, the `.ini` editors, `perf_parse`, `image_compare`,
`scene_digest`-from-file) resolve `Deps` only (no bridge, no lease) → cheap
multi-project parallelism with zero editors.

---

## 5. Capture / PIE reality (no hand-waving)

- **Today, only editor-world `take_screenshot` is backgrounded-safe** (synchronous
  scene capture). It works for N editors regardless of focus.
- **Per-project *PIE* capture is gated on the unbuilt P7 plugin.** UE throttles a
  backgrounded editor's tick/render (`t.MaxFPS`, editor "Use Less CPU when in
  Background"), so two simultaneous PIE sessions with one foreground will starve the
  background one. Spawn per-instance with throttle disabled
  (`-ExecCmds` setting `t.IdleWhenNotForeground 0` / editor perf prefs) and accept that
  reliable concurrent PIE capture ships with P7's game-world SceneCapture, not before.
- Capacity is bounded by **RAM + VRAM + build/cook spikes**, not RAM alone (§7).

---

## 6. Failure containment (scoped honestly)

- **Cross-project isolation is real:** separate bridges, separate goroutines, separate
  command ports → a hung/crashed editor on project A cannot block project B. This is the
  core win and it holds.
- **Intra-project is NOT magically un-wedgeable** (the AUTONOMY plan's own honest limit).
  A stuck project is recovered through the SAME §3.1 machinery (not an ad-hoc kill+respawn that
  leaves the lease pinned to a dead node): `RestartBegin` (set `Restarting` under the pool lock) →
  kill+confirm → respawn → watchdog re-pin (`waitForNode` excluding the dead node id → `OpenCommand`
  → rebind `Session.nodeID`) → refresh `LastHealthy` + clear `Restarting`, **lease preserved**. This
  reuses the controlled-restart transition, so there is no undefined `Unhealthy→respawn→re-pin` cell
  and no contradiction with §3.2 (`editor_events` tail helps observe; recovery is a re-pinned
  restart, not a magic unblock).
- **Only a genuine crash loses the lease** (§3.1). A crash — PID death *without* a controlled
  kill, caught by the standing PID heartbeat (§3) — → `MarkUnhealthy` → `LEASE_LOST` → re-attach.
  A successful `build_compile`-full / `editor_restart` → transparent re-pin, lease preserved. A
  *failed* restart (build error, `Launch` error, re-pin bound elapsed on an alive editor) →
  lease-PRESERVING `NEEDS_RELAUNCH`, the holder retries via `project_ensure_open` under the same
  lease. The reaper never touches a leased+alive editor (PID-gated, §3), so no build/restart/
  Live-Coding window is ever mistaken for a crash.
- **Reattach reconciles against GROUND TRUTH (process table + discovery) with a WRITE-AHEAD spawn
  intent — the replacement for the removed `killOrphanSiblings`, race-free.** Every editor the
  daemon has ever launched carries a **correlation token baked into its launch args**
  (`-MCPInstanceToken=<uuid>`), and that token is **written to disk BEFORE `lifecycle.Launch` — at
  EVERY launch site** (the initial `project_attach`/pre-warm spawn, the §3.1 `RestartEnd` relaunch,
  AND the §3 `project_ensure_open` recovery) — as a write-ahead intent `{project, token}`, then
  upgraded to the full record `{project, node id, command port, group, PID, PID-identity token,
  STATE ENUM}` once it's accepting. (Under "has MY token", a relaunch that skipped the write-ahead
  would classify its own orphan as foreign, so the write-ahead step is mandatory at all three sites,
  not just the first.) Intents/records persist to a **stable singleton-daemon location** (not a
  per-process/PID-keyed file) so the successor daemon reads the prior incarnation's tokens on
  startup — else it would miss them and mis-classify every self-owned orphan as foreign. Write-ahead is the
  crux: the one orphan a plain persist-after-spawn misses — spawned in the `Launch → persist-write`
  window, still in its 10-60 s cold start so NOT yet answering discovery pongs (`5 s NodeTimeout ≪
  cold start`) — is invisible to *both* a persisted-record scan and a discovery snapshot, so a fast
  daemon restart racing the cold start would leak it. On reattach the daemon reconciles against
  **process enumeration** (every `UnrealEditor` process, matched by its `-MCPInstanceToken` /
  project on the command line — this catches a just-`Launch`ed process *before* it is on discovery)
  **∪ live discovery pongs**, against the persisted records + intents. **KILL IS TOKEN-AUTHORIZED —
  only an editor positively confirmed daemon-owned may be killed** (the token is what makes this NOT a
  repeat of `killOrphanSiblings`, which killed by image name regardless of owner):
  - **Re-adoptable** = a persisted record in state ∈ `{Idle, Leased}` with a matching token/identity →
    **re-adopt as warm `Idle`** (the dead HTTP session's lease is freed — all sessions die with the
    daemon; a re-adopted editor rejoins the §7 warm pool).
  - A **token-bearing process** (a `-MCPInstanceToken` on its command line, seen by process
    enumeration) whose token is **one THIS daemon issued** — i.e. the token is present in this
    daemon's persisted intents/records — and matches a `{Restarting, NeedsRelaunch}` / intent-only
    record (write-ahead guarantees every self-owned orphan matches an intent, so there is no
    legitimate self-orphan without a record): **daemon-owned orphan → `Kill && confirm` then
    `Remove`**, killing by the **PID from enumeration** — do NOT try to open its command channel (a
    process-only orphan is process-only *precisely because* it is mid-cold-start and not accepting
    reverse-connects yet). **Recycled-PID guard:** immediately before `Kill(enumPID)`, re-verify that
    PID still bears MY `-MCPInstanceToken` (the process can exit between the enumeration snapshot and
    the kill, and Windows can recycle the PID onto an unrelated — possibly foreign — process); if the
    identity no longer matches → do NOT kill, treat as absent (Case E) — same recycled-PID discipline
    as §3.1-step-2 / the §3 heartbeat guard. The ownership test is **"has MY token," NOT "has A
    token"**: a token from
    another daemon's token-space matches none of this daemon's records → it is FOREIGN → **never
    killed** (this is the fix that keeps "no foreign editor killed" true — mere token *presence* is
    not ownership proof).
  - A **discovery-reachable** editor NOT in this daemon's token-bearing process set → confirm
    ownership LIVE before touching it, but **read-only and non-disruptively**: `get_command_line()`
    is read-only, but the daemon must NOT commandeer an editor already bound to another daemon's
    reverse-connect — so prefer the discovery advert / a passive read and yield immediately; a
    discovery-only editor not in MY enumeration is *very likely foreign* and the probe is a
    last-resort confirmation, not a takeover. If the command line bears a `-MCPInstanceToken` **THIS
    daemon issued**, kill by its **live PID (`os.getpid()` over the same channel)**. **A token this daemon doesn't recognize, no token, or a
    probe that fails to open/answer → FOREIGN or unconfirmable → NEVER kill, log only** (safety-first:
    another daemon's editor and a human's manually-opened editor are both left untouched — the rare
    compound-failure leak, a self-owned editor missed by enumeration AND records AND probe, is
    accepted over ever killing a non-owned editor).
  - **Reconciliation is a startup BARRIER:** the daemon completes per-project reconciliation (or holds
    a per-project reconcile lock) **before serving `project_attach`/`Lease` for that project** — else
    a re-attaching agent's `project_attach` races the reconciler and double-spawns while a
    re-adoptable warm editor is still pending (violating the no-double-spawn invariant).
  - **(Case E)** A **persisted record OR write-ahead intent whose editor is absent everywhere** (not
    in the process table, not on discovery) → the editor is genuinely gone (or a pre-`Launch` intent
    whose spawn died in cold-start); the entry is stale → `Remove` + free any lease (no kill needed,
    nothing is alive; token UUIDs are unique so a removed intent can't false-match a future token).
  Result: no *daemon-owned* live editor in ANY state or spawn-window (persisted, intent-only, or
  mid-cold-start) can outlive the daemon — race-independent — and **no foreign/human editor is ever
  killed.** NOTE (scope): token-auth gives KILL-safety when a human shares a project, but correct
  lease-BINDING alongside a foreign same-project editor is still bounded by §10 — `waitForNode`
  (§3) filters by `project_root`, so with two same-project nodes it could pin onto the foreign one;
  the same "prefer the node bearing MY token" disambiguation should extend to lease-time binding
  (or stays out of scope pending the §1-rank-3 remote-exec group override).

---

## 7. Resource governance (admission control, designed)

Not "floor(RAM/4GB)". A real model:

- A **RAM/token budget the fairness queue actually consults** before granting a lease or
  spawning an editor.
- **Reserve headroom for build/cook/shader spikes** (cl.exe/link.exe × cores, cook, shader
  compilation each spike multi-GB *concurrently* with resident editors) — not just the
  ~4–6 GB resident editor.
- Behavior under pressure is explicit: **queue** (bounded FIFO per project) vs **reject**
  (`BUSY` + retry hint) vs **evict-idle-first**. Measure real editor+build RSS on the
  target workstation to set the caps.
- **A daemon-owned build queue (≤1 build per engine install).** The daemon serializes C++
  builds itself — one running per engine, the rest **observably queued** — instead of letting
  them collide on UBT's host-wide `-WaitMutex` (`run.go:47`). This isn't just RAM governance:
  it's what lets the §3.1 restart watchdog distinguish a *known-queued* build (healthy, waiting
  its turn — never cancelled) from a *hung running* build (stopped emitting compile output —
  cancelled). Build liveness is impossible to get right while queueing is delegated to an opaque
  OS mutex; owning the queue is the prerequisite.

---

## 8. Trust boundary (stated, not overclaimed)

This is **soft isolation among mutually-trusting agents on one workstation**, not a
security sandbox: `execute_python` / `execute_console_command` are unrestricted arbitrary
code execution inside a leased editor, so an agent that wanted to could reach another
project's files. Hard multi-tenant containment would require OS-level separation
(per-tenant user / container / host — the v2 multi-host direction). The isolation
guarantees here are about *correctness under concurrency*, not adversarial security.

---

## 9. Phasing (de-risk in the right order)

0. **Throwaway spike FIRST.** Launch two real editors on one host, each with an ephemeral
   `CommandAddr` and project-filtered discovery; prove ONE server (HTTP daemon) can hold
   two isolated bridges and route correctly. This validates the transport decision AND
   the reverse-connect/discovery isolation *before* any sunk cost. (v1 scheduled the
   100-tool refactor first — backwards.)
1. **Transport + daemon:** StreamableHTTP, session→lease router, remove `killOrphanSiblings`.
2. **uexec.Session split** (shared discovery + per-instance command conn) + ephemeral ports.
3. **Deps→resolver refactor** + per-lease `ProjectContext` threaded through disk-touching packages.
4. **Lease semantics:** `LeaseRenew`, `LEASE_LOST`, node-id pinning, no fallback,
   kill-before-id-reuse, the in-flight-command exemption (`InFlight`/`CommandStartedAt`),
   **and the §3.1 build-hold sub-state + re-pin protocol** so a rebuild/`editor_restart`
   doesn't drop the holder's own lease.
5. **Admission control + reattach persistence.**
6. **Multi-host (v2):** host addressing on `Instance`; unicast discovery per host; the path to hard tenancy.

Steps 0–4 deliver two agents / two projects / one daemon on one host.

## 10. Risks / open questions

- **Daemon ownership & restart** — who supervises the long-lived daemon, and an allowlist of
  which local sessions may attach (soft, per §8). (Session close → lease release is now a
  designed mechanism, §0, not an open question.)
- **Editor cold-start (10–60 s)** — warm pool + pre-attach to hide it; the fairness queue
  must not livelock on cold starts.
- **Concurrent PIE capture** remains gated on P7; until then, multi-project verification
  leans on editor-world capture + non-visual oracles (`scene_digest`, `pie_verify`, logs).
- **Same-project concurrency** (two editors, one project) is explicitly out of scope until
  the remote-exec group override (§1 rank 3) is verified live.
