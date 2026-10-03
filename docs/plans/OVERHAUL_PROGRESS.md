# v2 Overhaul — Progress & Evidence Log

Plan: [`OVERHAUL_PLAN.md`](OVERHAUL_PLAN.md) (r6, unanimous A+). One section per phase: what was done,
commands run, results, deviations, gate verdict.

## P0 — Baseline (2026-10-03)

**Git.** Desktop-tools work committed (`c609af6`), tagged **`v1-final`** (rollback point), branch `overhaul/v2`
created; plan committed (`5cf7c0a`).

**v1 surface (measured, `internal/e2e/v1surface_test.go`).** 155 tool names (152 stdio incl. conditional
`cockpit_url` + 3 daemon `project_*`) frozen in `internal/e2e/testdata/v1_tools.txt`. Stdio `tools/list` result =
**106,701 bytes** (plan estimate 90–110 KB ✓). This is the baseline the ≤ 45 KB core budget is measured against.

**Refs re-derived** (`scripts/refs.ps1`) — every §0 anchor matched:
```
internal/tools/register.go:39  func RegisterAll(s *mcp.Server, d Deps) {
internal/daemonwire/project_tools.go:21,39,47  project_attach / project_release / project_list
internal/snippets/py/mcp_bridge.py:3170  def _mcp_dispatch(op, b64args):
internal/snippets/py/mcp_bridge.py:3183  if isinstance(result, dict) and result.get("error") and "code" not in result:
internal/snippets/py/mcp_bridge.py:1023  def _pick_world(which):
internal/snippets/py/mcp_bridge.py:3029  _OPS = {          (_OPS literal entries: 81)
internal/tools/build_tools.go:234  add(s, "editor_restart",
internal/tools/discovery_tools.go:55  add(s, "project_map",
internal/cockpitbridge/bootstrap.go:91  func (m *MemEpochStore) LastSeq(...)
cmd/unreal-mcp/daemon.go:67,76  shared mcp.NewServer / NewStreamableHTTPHandler(... return srv, nil)
internal/uexec/client.go:160  if errors.Is(err, ErrConnectionLost) && attempt == 0 {   (transparent resend)
internal/uexec/command.go:157,198  write / read errors both → ErrConnectionLost
internal/uexec/config.go:18  defaultCommandAddr = "127.0.0.1:6776"
internal/bridge/install.go:34  if cur != snippets.Version() {
internal/fakeeditor/fakeeditor.go:163,173  open_connection spawns a channel / close_connection no-op
```

**T1 harness (new).**
- `uexec.OpenUnicastDiscovery` — unicast discovery against a fake editor (out-of-package, parallel-safe).
- `internal/bridge/bridgetest` — op-level emulator of the companion: version sentinel eval, hotload bootstrap,
  perf snippet, `_mcp_dispatch(op,b64)` decode + scripted ops + `UNKNOWN_OP`, NameError when uninstalled, raw-Python
  capture; stateful `World` (editor_status, list/get/spawn/delete actor). Lives beside `fakeeditor` until P2 moves the
  wire fake to `uexec/uexectest`.
- `internal/e2e` — MCP in-memory client → `tools.RegisterAll` → real `bridge` → real `uexec` session → fake editor.

**T1 scenarios against v1 (7, all green):** companion installed once across calls; actor spawn→list→delete with
dispatched-op order asserted; op error surfaces `CLASS_UNRESOLVED`; execute_python reaches the editor; no editor →
tool error; editor restart → NameError → reinstall exactly once; editor drops channel after every reply → calls still
succeed, module installed once. Plus `TestV1SurfaceFrozen`.

**Deviation:** none from plan. Note for P4: `TestCallsSurviveChannelDrops` passes today *because* of the transparent
resend that §2.8 case 3 restricts — it will be rewritten into the drop scenarios when exactly-once lands.

## P1 — Hygiene (2026-10-03)

**Deviation (safety): separate worktree.** The consumer projects run this repo's *main checkout*: aesir's
`.mcp.json` launches `server.py` (as `unreal-py`) and both aesir and poly-world launch `dist/unreal-mcp.exe` from it.
Deleting the legacy Python or rebuilding `dist/` there would break live sessions in those projects. The main checkout
was returned to `feat/multi-project-daemon` and all overhaul work moved to a git worktree,
`../unreal-mcp-server-v2` (branch `overhaul/v2`). Consumers are untouched until the P7 cutover.

**Done.**
- 12 historical root docs `git mv`'d to `docs/archive/` + `INDEX.md` (one-line status each); plan moved to
  `docs/plans/`. Root now: README, go.mod/sum, .gitattributes/.gitignore.
- Deleted (recoverable from `v1-final`): `server.py`, `unreal_bridge.py`, `remote_execution.py`, `smoke_test.py`,
  `cmd/abparity`, `cmd/uspike`, `deploy/mcp.ab.json`, `deploy/mcp.cutover.json` (hard-coded user paths).
- `-selftest` absorbs uspike's probes: logs every discovered node + the chosen one, runs the `__main__`-persistence
  probe (reports whether hotload mode is viable), then the editor_status round-trip.
- `deploy/mcp.json.tmpl` + `scripts/render-mcp-config.ps1` (no user paths in the repo).
- `scripts/build.ps1` now runs gofmt / vet / stdout-purity / tests (`-Fast` skips tests) — it previously claimed a gate
  it never ran; Go path derived from go.mod. `scripts/clean.ps1` prunes `dist/` to the exe + newest `prev-*.exe`.
- Go version single-sourced from `go.mod`: CI uses `go-version-file`, `bootstrap.ps1` refuses a mismatched pin.
- `.gitignore`: ruff/pytest caches, codex/agents scratch, logs, plugin Binaries/Intermediate.
- **Python split**: `mcp_bridge.py` (3,207 lines) → 14 section files `internal/snippets/py/NN_*.py`, cut at top-level
  banners; Go embeds `py/[0-9]*.py` and concatenates in filename order. `TestSplitIsByteIdentical` pins the
  pre-split SHA-256 `40fc6ba8…9190`; every section also `py_compile`s standalone; CI compiles each + the concat.
- P0 gate follow-ups: emulator now decodes the hotload payload and installs only a module that defines
  `_mcp_dispatch` + a version sentinel (version taken from the payload; `InstalledSource()` asserted == embedded
  source); version-check counting asserted on channel drops; no-editor test pins the `ErrEditorNotFound` text;
  `refs.ps1` exits non-zero on a missing anchor.

**Evidence.** `go vet ./...` clean; gofmt clean; `go test ./...` all ok; `scripts/build.ps1 -Fast` → all three gates
pass, exe stamped `v1-final-2-g342e5ae` (tags now drive the version); `scripts/refs.ps1` → all anchors found, exit 0.

**Gate P0:** A (MCP/test reviewer; 60 `-race` runs, 0 flakes). Remaining P0 notes scheduled: on-disk install
emulation (P4), raw-result emulator mode for the error-key rule (P5), goroutine-leak check (P3).

## P2 — Packages (2026-10-03)

**Result: 48 → 28 top-level internal packages that link into the binary** (counting rule: `go list ./internal/...` minus the test-only `e2e`/`archtest`, top-level dirs only; 30 dirs total) (+ test helpers `uexec/uexectest`, `bridge/bridgetest`, and
`cockpit/attach`); `manifest` + `affordances` are deleted in P3a when the spec table replaces them (→ 26).
Commits: P2a (leaf merges), P2a-fmt, P2b, P2c, P2d, P2e.

| New package | From |
|---|---|
| `audit` | gametrace, audioaudit, feelaudit, primitiveaudit, renderhealth, utilization, decisionaudit |
| `visual` | lumaudit, stylecohesion, imgdiff, montage, framing |
| `eval` | rubric, predicate, scenario |
| `design` | balance, explore |
| `calibration` | critique, persona |
| `logs` | events, logtail |
| `build` | build, gitutil |
| `snapshot` | digest |
| `uexec/uexectest` | fakeeditor |
| `bridge` (+`py/`, `companion.go`) | snippets |
| `cockpit/attach` | cockpitbridge, cockpitlaunch |
| `session` | `tools.Deps`, `WithDeps`, `InstallDepsMiddleware` (tools keeps `type Deps = session.Deps`) |
| `supervisor` | editorpool, daemon's `Record/LiveEditor/Reconcile/OSLiveness` + `Editor/Spawner` interfaces, daemonwire `spawner.go` (`Spawner` struct → `ProcessSpawner`) + record store, cmd `relaunch.go` + `siblings_*.go` |
| `daemon` | daemon (router, build queue, runtime) + daemonwire (Daemon, project tools, boot reconcile) |

Colliding identifiers were renamed with a package prefix (e.g. `audioaudit.Report → audit.AudioReport`,
`lumaudit.Analyze → visual.AnalyzeLuminance`, `editorpool.New → supervisor.NewPool`); helpers that merely shared a
name but differ (`luma`, `toFloat`, `clamp01`) were renamed, not deduplicated.

**Import DAG enforced** by `internal/archtest` (`go list`); the real graph matches §2.6 except two transitional edges
marked in the test: `cmd/unreal-mcp`'s direct imports (→ `app` in P3) and `tools → affordances` (removed P3a).
Verified it fails on a removed edge (`supervisor → config`).

**Deviations (recorded):**
1. *No throwaway prototype branch.* Each move was done as its own compile- and test-verified commit, and the final
   `go list` edge dump (above) is the evidence the prototype was meant to produce.
2. *`reattach.go` split*: the record store + `KillConfirmed` + `InstanceTokenFlag` moved to `supervisor` (the spawner
   writes the write-ahead record), while the boot barrier (`ReconcileAtStartup`, `reconcileRecords`,
   `PruneDeadRecords`) stays a `*Daemon` method set in `daemon` — it is daemon lifecycle, and moving it would mean
   rewriting its tests for no behavioural gain. Still DAG-compliant (`daemon → supervisor`).
3. *Audit fixtures stay in `audit`*: moving them to `audit/audittest` would create an import cycle (audit's own
   in-package tests use them).

**P1 gate follow-ups done:** companion module now at `internal/bridge/py/` (plan location); selftest removes its
probe global; `refs.ps1` header path fixed; CI Windows build fetches tags (`fetch-depth: 0`) and stamps
`-X version.Version/Commit`.

**Evidence.** `go build ./...`, `go vet ./...`, gofmt clean; `go test ./...` all ok (incl. e2e, archtest);
`scripts/refs.ps1` exit 0.

**Gate P2:** A (CTO reviewer; ran e2e at 9769348 and HEAD — 155 tools, identical 106,701-byte tools/list; `-race`
on daemon/supervisor/archtest). Notes: `pool.go` shows as a new file (rename similarity lost; use
`git log --follow internal/editorpool/editorpool.go` at v1-final for history); stale `editorpool:` error strings and
comments are renamed in P3a; archtest now locates `go` via PATH.

## P3a — Envelope & spec (2026-10-03)

**Done.**
- `internal/tools/envelope`: closed 15-code set; `Error{code,message,hint,retryable,outcome,details}`; `Classify`
  maps `bridge.OpError` (via the plan's Python-code table, original kept in `details.editor_code`; traceback ⇒
  `PYTHON_ERROR`), uexec sentinels (`ErrEditorNotFound` ⇒ retryable `EDITOR_UNREACHABLE`; timeout/connection-loss ⇒
  `outcome:"unknown"` for non-ReadOnly calls), context errors, and passes envelope errors through. `SafetyNet`
  receiving middleware rewrites unstructured `isError` results to `INTERNAL` and turns calls to known-but-disabled
  tools into `PRECONDITION` naming the toolset (truly unknown names stay protocol errors).
- `internal/tools/spec`: `Spec`/`OpSpec` (per-op tier, idempotency, async, timeout/max, required/rejects, reaches);
  registration through raw `Server.AddTool` with in-house validation (`jsonschema-go`, defaults applied) so every
  error path is enveloped; op dispatch with per-op Required/Rejects; `timeout_s` capped at the op's Max; per-call
  `recover()`; annotations from the worst op per the §2.1 table (openWorldHint always explicit); no outputSchema.
  `Lint` enforces R2/R3/R6 and the tier rules (RO-only tools, no destructive op beside RO/Eph ops, declared tier ≥
  worst reached Python op unless its escalating args are Rejected). Resolved schemas cached per Spec.
- `spec/pyops.go` replaces `internal/manifest` (deleted): all 81 ops classified on the 5-tier model with argument
  escalations evaluated on effective values (`apply_level_recipe clean_slate` defaults true ⇒ escalates by
  default). Bijection test vs `bridge.CompanionOps()`. Sweep + hazards: [`P3a_PYOPS_SWEEP.md`](P3a_PYOPS_SWEEP.md).
- v1 surface re-served through the spec layer via `spec.Typed` (`add()` now registers a Spec): same names and input
  schemas and result shapes, v2 annotations (tier = same-named op's worst tier, explicit table otherwise), envelope
  errors. Stdio server installs `SafetyNet`.
- bridge install errors now wrap their cause (`%w`), so an install-time timeout classifies as `TIMEOUT`.
- Stale `editorpool:` error strings/aliases/comments renamed (P2 gate note).

**Evidence.**
- `testdata/tools_list.v1-envelope.golden.json` committed and asserted byte-for-byte (`TestV1EnvelopeGolden`); the
  155-name list is unchanged (`TestV1SurfaceFrozen`). Stdio tools/list via spec = **100,362 bytes** (was 106,701 —
  output schemas dropped, annotations added).
- Envelope on every error path, end-to-end (`internal/e2e/envelope_test.go`): schema violation ⇒ `INVALID_ARGUMENT`
  and the editor is never reached; editor `UNKNOWN_OP`; editor op error ⇒ `OPERATION_FAILED`; `CLASS_UNRESOLVED` ⇒
  `NOT_FOUND` with `details.editor_code`; no editor ⇒ retryable `EDITOR_UNREACHABLE`; slow mutating op ⇒
  `TIMEOUT`/`outcome:unknown`/non-retryable. Unit: panic ⇒ `INTERNAL`; unknown op/missing/rejected params;
  `timeout_s=60` capped at a 200 ms op Max; annotation mapping per tier; every Python code mapping; Classify matrix;
  SafetyNet rewrite + disabled-tool hint; Lint catches each rule and accepts the Rejects escape hatch.
- `BenchmarkServerConstruction` (50 specs): **16 µs/op** (target < 2 ms; was 2.03 ms before caching resolved schemas).
- `go vet ./...`, gofmt, `go test ./...` (incl. archtest with the new `tools/envelope`, `tools/spec` edges) green.

**Deviations.** `affordances` is kept until P5e: it backs the v1 `affordances` tool, which stays on the surface until
`toolsets describe` replaces it (P3 must not change the v1 surface). Archtest allows `tools/envelope → uexec` (needed
to map protocol errors; plan listed `session, bridge`). The daemon's three `project_*` tools still register via
`mcp.AddTool` until P3b moves them behind `session.ProjectManager`.

**Gate P3a, round 1: A- (MCP/test).** Blocker: the spec layer applied `timeout_s` as a ctx deadline to v1 tools that
implement `timeout_s` themselves (`pie_wait_until` would return `TIMEOUT` instead of the domain negative
`met:false`). Fixed: the spec layer bounds a call only when the spec declares timing; a caller's `timeout_s` is
honoured only up to a declared Max (ignored when none), so v1 adapter specs leave it to their handlers — regression
test `TestV1TimeoutSStaysWithHandler` (+ unit `TestUndeclaredTimingIgnoresTimeoutS`). Also fixed: `"arguments": null`
→ empty object (was a recovered panic); any non-ReadOnly op (incl. Ephemeral) reports `outcome:"unknown"` and
non-retryable on timeout/cancel; `IsEnveloped` now requires a structured `error.code` in the closed set. Noted: an
install-phase timeout is reported `outcome:"unknown"` even though the op was never sent (safe direction). Also
landed: v1 registration now collects specs (`tools.Specs(d)`), the base for per-session toolsets (P3b).

**Gate P3a, round 2: A (pass)** — MCP/test reviewer verified the fixes; notes folded into P3b (single deps path;
timeout_s-shortening unit test added).

## P3b — Sessions, jobs, toolsets, gate (2026-10-03)

**Done.**
- `internal/app` — the single construction path: `app.NewServer` (stdio: one server + `session.State`) and
  `app.Handler` (daemon: a fresh server per new StreamableHTTP session, `SessionTimeout` = `-session-idle`, default
  30 m). The session ID is bound at `InitializedHandler`; `ss.Wait()` → `State.Teardown()`. Middleware order:
  recover → logging → per-request context (state, toolsets, per-session deps) → `envelope.SafetyNet(toolsets.Disabled)`.
  Both `cmd` entry points now build through `app`.
- `session.State` (teardown hooks, run once, late hooks run immediately), canonical `ProjectKey` (abs, symlinks,
  `.uproject`→dir, case-folded on Windows; lives in `lifecycle` so the pool can use it), `.umcp.json` loader
  (`toolsets`, `gate_policy`, `keep_package_recovery`), `ProjectManager` interface on `session.Deps`.
- `spec.Toolsets` — per-session enable/disable via `AddTool`/`RemoveTools` (→ `tools/list_changed`), core cannot be
  disabled, unknown toolset rejected, `Disabled()` feeds the SafetyNet hint. v1 tools are all `core`; the daemon's
  `project_*` tools moved into `tools` under toolset `daemon` (registered only when a `ProjectManager` is wired);
  `project_attach` applies the project's `.umcp.json` toolsets.
- Gate (`spec.Gate`): Destructive/Exec ops under policy `require` wait for approval *before* their deadline starts;
  sync wait ≤ min(gate_timeout, 25 s) (configurable lower for tests), then a `pending_approval` job
  (`executed:false`, "NOT executed — awaiting approval") that runs the op on approval, fails `PRECONDITION
  {reason}` on deny/timeout, and is cancelled + severed at session teardown. Cockpit adapter wiring is P4.
- Async ops: handlers return `Result.Job`; the spec layer returns `{job_id,state}` or waits ≤ `wait_s` (cap 25 s)
  streaming `notifications/progress` on the call's own token (stops before the response). `jobs`: `WaitFor`,
  owner tags + `CancelOwned`, `List`.
- Daemon: jobs owned by **project** (`ProjectJobs`), not lease; one end-of-session path `EndSession` (DELETE, idle
  expiry, sweeper via registered closer, `project_release`) with the **drain** rule (lease held while a project job
  runs; `TickDrains` releases); same-project attach **adopts** a draining lease (`Pool.Transfer`/`Router.Transfer`,
  never via Idle); pool matches projects by canonical key (fixes a latent double-spawn for differently spelled
  paths). `daemon.NewWithSpawner` + `supervisor.NewEditorHandle` for custom spawners/tests.
- Dynamic tier lint hook in the T1 harness (observed Python ops ⊆ `OpSpec.Reaches`; v1 specs declare none).

**Evidence (all green; `-race` ×2 on e2e/daemon/spec/jobs/session/app).**
- Daemon over real HTTP with fake editors (`internal/e2e/daemon_test.go`): two sessions × two projects, no
  cross-talk; client **vanishes without DELETE** (dead transport + dropped connections) → lease released only after
  the 300 ms idle expiry (asserted ≥ 250 ms); sweeper ends a session that holds its SSE stream open; **vanish
  mid-job** → lease drains, another project cannot lease it, a new session with a differently-spelled path adopts
  the same instance without spawning and sees the job via `job_status`; `project_attach` with `.umcp.json
  {"toolsets":["design"]}` → `list_changed`, the disabled tool becomes callable; **200-session churn** → goroutines
  back to baseline.
- Unit: gate approve-in-window / denied / pending→approved (job runs once) / pending→denied / pending→timeout (never
  runs) / teardown severs; async `wait_s` with progress notifications; toolsets enable/disable/list_changed/core
  protection/unknown; session state/teardown/project key/project file; daemon drain/adopt (case-folded)/sweeper
  closer/warm release/prune; jobs WaitFor/CancelOwned/List.
- v1 surface unchanged (`TestV1SurfaceFrozen` 155 names, `TestV1EnvelopeGolden` byte-identical); archtest updated
  for `app`, `session → lifecycle`, `tools/spec → jobs`.

**Gate P3b, round 1: B+ (fail)** — MCP/test reviewer. Fixed:
1. *Sweeper test flaky (21/30 fails without `-race`)*: a zero-TTL sweep compared `lastSeen.Before(now)`, and Windows'
   coarse monotonic clock put both on the same tick. The sweep now treats `lastSeen ≤ cutoff` as idle (same semantics
   for real TTLs). Proven: `-count=50` without `-race` → 50/50.
2. *Teardown cancelled approved, running ops*: the pending-approval job is owned by `approval:<session>` only while it
   awaits a decision; on approval it clears its owner (`Job.SetOwner("")`, owner now mutex-guarded), so a session that
   ends mid-run lets the op finish and the lease drains. Test: approve → teardown mid-run → op completes once.
3. *Progress after the response*: an approved op runs on a **detached** copy of the call (no progress token, no
   `wait_s`); an approved **async** op's pending job waits for the inner job and carries its final result. Test:
   zero progress notifications after the `pending_approval` response; job result = inner job's `succeeded` view.
NB fixes: sessions that never send `notifications/initialized` are bound on their first request (single `sync.Once`
bind shared with the InitializedHandler); `Toolsets.Apply` validates every name before changing anything and keeps
the startup toolsets (`base`); drains are keyed by **session** (two drains on one project no longer overwrite) and
released promptly by a per-drain watcher (`TickDrains` stays the backstop) — tests for both; the drain poll interval
is a per-daemon field (a package var raced across tests under `-race`).

## P4 — Supervisor & connection semantics (in progress)

**P4a: exactly-once dispatch + connection-loss decision table (done, in the same commit as the P3b fixes).**
- `uexec`: `RetryPolicy` carried in the context (`RetryNone` default, `RetryIdempotent`); `ErrOutcomeUnknown` (wraps
  `ErrConnectionLost`) for a read-side loss after a successful write; `ErrChannelStolen` + `Session.Stolen/Reclaim`;
  the decision table in `RunCommand`: (0) liveness probe before sending — retained `bufio.Reader`, `Peek(1)` with a
  3 ms deadline, whitespace consumed, any other unsolicited byte = desync; (1) self-taint as before; (2) failed write
  re-sent once; (3) read loss after write → `ErrOutcomeUnknown`, re-sent once only under `RetryIdempotent`; (4) a peer
  close within 10 s of reconnecting from one, while the same node still answers discovery → stolen, fail fast, no
  reconnect until `Reclaim`. Injected clock for the theft window.
- **Deviation from the plan (found while testing):** the probe is skipped on a recently-used channel only for
  idempotent commands. A non-idempotent command always probes (≈3 ms), because an editor that closes the channel
  right after its last reply would otherwise swallow the next mutating command with an unknown outcome — the old
  `TestReconnectOnEditorDroppedConnection` caught exactly this. Also: a *failed reconnect* while the node still answers
  is reported as `EDITOR_BUSY`, not theft — UE also refuses connect-back while its game thread is busy.
- Spec layer sets `RetryIdempotent` for ReadOnly/idempotent ops; the bridge marks its install/version/perf commands
  idempotent; envelope maps `ErrChannelStolen` → `EDITOR_BUSY {reason: channel_stolen}`.
- Fake editor: `SingleSlot` (a new open_connection closes the existing channels, as UE does), `DropReplyIf` (execute,
  then close without replying), `CloseChannels` (limit `CloseAfterReplies` to the first N channels), `Connections()`.
- Tests: mutating command with a dropped reply executes **exactly once** and returns `ErrOutcomeUnknown`, then the
  session recovers; idempotent command is re-sent (runs twice, succeeds); probe detects a peer FIN, consumes trailing
  whitespace, flags unsolicited data without consuming it; recent-channel skip applies to idempotent only; two clients
  on a single-slot editor → the victim reports `ErrChannelStolen` on the re-steal, makes **no further connections**
  until `Reclaim`, which reconnects exactly once; peer closes outside the theft window reconnect normally.
- `TestCallsSurviveChannelDrops` (P0 baseline) rewritten to a one-off drop: an editor that drops after *every* reply
  now reads as channel theft and fails fast (visible + recoverable) instead of silently reconnecting on every command.

**Gate P3b, round 2: A (pass).** Early read on P4a raised 4 issues, all fixed in P4b below.

**P4b: remaining supervisor & connection work (done).**
- *P4a review fixes*: theft now requires a **fresh pong** received after the close (active ping, ≤ 1 s) — a node that
  just crashed stays in the cached table for `NodeTimeout`, which could falsely mark a session stolen; a stolen
  session **clears itself** once that editor node leaves discovery (quit/crash/restart); a probe **desync** (unsolicited
  bytes) reconnects but does not count toward theft (`probeResult` = alive/dead/desync); a bare `ErrConnectionLost`
  (connect/write failed — nothing sent) now classifies `outcome:"none"`, retryable, while `ErrOutcomeUnknown` keeps
  `unknown`; the probe's skip window uses the session's injected clock; the gated job clears its session owner inside
  the approval branch and never starts if teardown raced the decision.
- **`_mcp2` companion namespacing**: the companion now executes in its **own module object** (`__main__._mcp2`,
  `types.ModuleType`; on-disk mode binds the imported module the same way) and only `_mcp2_dispatch`,
  `_mcp2_dispatch_native`, `_MCP2_BRIDGE_VERSION` (= 1) are bound into `__main__`. Verified in real CPython with a
  stub `unreal`: no helper leaks into `__main__`, v1 names untouched, unknown op → `UNKNOWN_OP` envelope.
  **Plan finding:** the C++ plugin hard-codes `__main__._mcp_dispatch_native` (`MCPCockpitServer.cpp:218`) — the one
  name v2 cannot namespace without a plugin rebuild. `Bridge.ClaimNative` points it at the v2 companion **only when v2
  attaches the cockpit** and zeroes v1's `_MCP_BRIDGE_VERSION`, so a rollback to v1 reinstalls v1 instead of calling a
  redirected entry point. `attach.Bootstrap` claims before selecting the native backend (tested).
- Emulator: v2 names, module install, **on-disk install emulation** (reads the written `mcp_bridge.py`; P0 gate
  note), native-claim counter. Split byte-identity test retired (the module is now edited, as planned).
- **Every editor launch passes `-AutoDeclinePackageRecovery`** (`lifecycle.launchArgs`), unless the project's
  `.umcp.json` sets `keep_package_recovery` (project-file loader moved to `lifecycle`; `session` re-exports).
- **Cockpit**: bootstrap retry backs off 3 s → 60 s; MCPCore absence is cached per command-channel generation (no
  game-thread probe every 3 s); `MemEpochStore` really tracks the resume seq per (project, epoch) and the launcher
  advances it when a session drops. `-cockpit off|on` (default **off** until P7: the plugin accepts one Go peer);
  daemon mode attaches a launcher per leased editor via `Daemon.OnEditorReady` when on.
- **Config**: `Validate()` (snippet mode, ondisk⇒project, auto-relaunch⇒project+engine, log level/format, timeouts,
  session-idle, cockpit) — exits 2 on bad config; engine dir resolves flag → `UMCP_ENGINE_DIR` → `UE_ENGINE_DIR` →
  Epic launcher `LauncherInstalled.dat` (project's `EngineAssociation` first, else newest, numeric version compare);
  the hard-coded `D:/Unreal/Engine/UE_5.7` default is gone. `LoadFrom(flagset,args)` makes it testable: coverage
  **94.2%** (plan target 90%).
- **Unicast discovery**: a non-multicast `-group` address pings that endpoint directly (binary tests; remote editors).
- **T3 binary smoke** (`internal/e2e/binary_test.go`): `go build -cover`; stdio over `mcp.CommandTransport` against a
  fake editor → 152 tools, spawn→list round-trip, stdin EOF ⇒ exit 0 in < 5 s, `covcounters.*` present (stdout purity
  implicit — any stray stdout write breaks the transport); daemon over HTTP: two concurrent sessions, daemon toolset
  present, `project_list` works, unattached `editor_status` fails cleanly. Both pass.
- `scripts/refs.ps1` now verifies the §0 baseline against the `v1-final` tag (the overhaul changes those lines).

**Deviation: stdio is not literally "a pool of size 1".** In stdio the server attaches to a user-launched editor that
carries no daemon token, so a lease pool adds no safety; what the plan's unification protects — launch flags,
relaunch, orphan reaping, liveness, record store — is shared in `supervisor`/`lifecycle` by both topologies.

**Evidence.** `go vet ./...` clean; gofmt clean; `go test ./...` all ok; `-race -short ./internal/...` all ok;
uexec theft/probe/exactly-once tests stable at `-count=10`; refs exit 0.

**Gate P4, round 1: A- (CTO).** Fixed:
1. *On-disk install shared v1's file + module name* (`Intermediate/PyMCP/mcp_bridge.py`, `import mcp_bridge`), so a
   handover would reload one module object under the other server. v2 now uses `Intermediate/PyMCP2/mcp2_bridge.py`
   imported as `mcp2_bridge`; T2 `test_ondisk_modules_are_distinct` imports both and reloads v2 — distinct objects.
2. *Rollback only half tested* → **T2 python contract suite** (`internal/bridge/py/tests`, pytest + stub `unreal`) runs
   the **real v1 companion** (frozen byte-exact from `v1-final` as a fixture) and the v2 companion in one `__main__`:
   v2 install leaves v1's dispatch, native entry, `_OPS` and sentinel identical, both answer; after `ClaimNative` the
   plugin entry reaches v2 and v1's sentinel is 0; a v1 rollback reinstalls, reclaims its names, serves — and v2 still
   serves. Plus dispatch contract tests: unknown op, the §2.7 item-6 error-key rule (a coded in-band error is now
   `ok:false`; v1 passed it as `ok:true`), coded `_V2Error` with details and no traceback, Python exceptions keep a
   traceback, every `_OPS` entry defined once, and the conftest bootstrap pinned to `install.go`'s.
NB fixes: `OnEditorReady` is keyed by editor **bridge** (a controlled restart's new bridge gets its own hook, the old
one's context ends) and is retried if the first resolve fails. Accepted NB for later: end-to-end crash→relaunch T1
(before P7); T3 `toolsets enable` → list_changed lands with the `toolsets` tool (P5e).
Also landed: **ruff is blocking in CI** (companion linted as the concatenated module per `pyproject.toml`; baseline of
19 findings fixed — `E402` ignored by design for the sectioned module) and pytest runs in CI.

## P5a — editor / python / console / level / actor_* (+§2.7 items 1-4)

**Landed.** Seven v2 tools from one spec table (`internal/tools/v2_core.go`): `editor` (status|ping|health),
`python` (run|recipe; `clean_slate` defaults **false** and escalates to Destructive), `console` (world editor|pie,
default editor), `level` (open|save_all|set_world_gamemode), `actor_query` (list|get|find, default editor, `auto`
allowed — RO), `actor_edit` (spawn|delete|transform|set_properties, **world required**, per-op tiers via pyops),
`actor_call` (once or `until` polling; returns met/result/calls). 20 v1 tools retired (the bijection test accounts
for every one via `Replaces`). Python (`95_v2_core.py`): `_V2Error` coded errors; `_v2_world` (editor|pie|auto,
`game` alias, unknown ⇒ `INVALID_ARGUMENT`) — item 1; `_resolve_actor` label/path/`@gamestate`/`@pawn` with PIE
path translation (`UEDPIE_<n>_`) and `CONFLICT` + candidates — items 2 and (part of) 9; `_resolve_class_v2` over
loaded modules + AssetRegistry `*_C` with `CONFLICT` — item 3; transform/set_properties generalized over both
worlds — item 4 (the undo transaction landed in the gate fixes below). `editor_status` now carries camera, selection,
actor_count, recorders (absorbing v1 `editor_state`).

**Parity gap caught by the tests and closed:** v1's text tools surfaced the editor's captured Warning/Error lines
(e.g. "Package X failed to save"). `Bridge.CallLog` returns them and every v2 op reports them as `editor_log` —
on success in the result, on failure in `error.details.editor_log` (`TestConsoleSurfacesEditorLog`,
`TestSaveAllFailureKeepsEditorLog`).

**Tests.** T1 e2e rewritten to v2 (world matrix editor/pie/auto, `CONFLICT` on duplicate labels, `actor_call until`,
python run + recipe, editor health) against the stateful `bridgetest` world (editor + PIE worlds); T2 pytest
`test_v2_core.py` (world picker, resolver ambiguity, PIE path translation, class resolver); `TestMigrationAccounting`
(155 v1 names: registered, replaced exactly once, or in `droppedV1`) and `TestV2SpecsLint`; surface golden renamed
`tools_list.golden.json` and regenerated (stdio: **139 tools**, from 152).

**Evidence.** `go vet ./...` clean; gofmt clean; `go test ./...` all ok; `-race` over tools/e2e/bridge/daemon/uexec
ok; pytest 17 passed; ruff (as CI: concatenated module + tests) clean; every part `py_compile`s.

**Gate P5a, round 1: B- (GameDev/MCP reviewer).** No test ran the Python op bodies: T1 runs the Go emulator and
pytest ran only the resolvers. That hid a real bug. Fixed:
1. *The class filter crashed on every call.* `UClass.is_child_of` is not reflected in 5.7; it is now
   `unreal.MathLibrary.class_is_child_of`.
2. *Editor edits were not undoable.* `modify()` without a transaction records nothing. Every editor
   spawn/delete/transform/set_properties now runs in one `ScopedEditorTransaction` with `modify()` (`_undoable`). PIE
   edits use neither.
3. *The matrix and the op bodies were under-tested.* **`tests/fakeunreal.py`** is a stateful fake of the `unreal` API
   that raises on anything it doesn't model (it deliberately lacks `is_child_of`). `test_v2_ops.py` runs the real op
   bodies through `_mcp2_dispatch` and covers:
   - all 8 actor_edit cells, and the transactions;
   - `@pawn`/`@gamestate`;
   - actor_call, including unknown function ⇒ `NOT_FOUND` and editor ⇒ `UNSUPPORTED`;
   - every class-resolver branch: project module, `Module.Class`, plugin module, Blueprint by short name and `_C`,
     CONFLICT, unresolved;
   - PIE path translation.

   T1 adds the 4 missing matrix cells, actor_call editor `UNSUPPORTED` and `@pawn`.
4. *console dropped Info output.* `Bridge.CallLog` now returns every captured line. console returns them as `output`
   and in its text summary; other tools keep Warning/Error as `editor_log`.

Non-blocking findings, also fixed:
- short-name lookup uses `find_object` (no "Failed to find" log noise) and accepts `Module.Class` for plugin modules;
- the `until` deadline margin scales (≤ 1 s, a fifth of the window) and `until` echoes `world`;
- new lint rule: **world=auto only on ReadOnly tools**;
- native dispatch keeps a failure's `details`: MCPCore forwards only `result`, so in native mode the companion sends
  them as `result.details`;
- set_properties with every property failing is `INVALID_ARGUMENT`;
- `_edit_world` also rejects `auto` in Python;
- a missing `static_mesh` on spawn is `NOT_FOUND`;
- the stale `clean_slate` test comment is fixed and the escalation is pinned.

Kept as designed: PIE `delete` stays Destructive (one tier per op; documented). `_pick_world` users migrate in P5c.

## P5b — viewport / asset_* / reflect / project_* / widget_*

**Landed** in `internal/tools/v2_assets.go` and `internal/bridge/py/96_v2_assets.py`:

| Tool | Ops | Notes |
|---|---|---|
| `viewport` | get, set, focus, select, selection | |
| `asset_query` | list, info, search, deps, tags, thumbnail | thumbnail returns the PNG as image content |
| `asset_create` | create, replace | |
| `asset_edit` | set_defaults, add_component, assign_subclass | |
| `asset_import` | files, reimport, datatable | |
| `reflect` | object, class, enum | |
| `project_config` | — | offline `.ini` |
| `project_map` | project, level | project is offline; level is live |
| `widget_query` | tree, describe, render | render returns the PNG |
| `widget_edit` | compose, prune, compile | toolset `ui` |

35 v1 tools are retired: 19 replaced here plus the 16 HUD tools that were never implemented. The stdio surface is now
**97 tools** (from 139).

**Deviations from the §2.3 table.** Each is strictly safer or more uniform:
- *`asset_create` has ops `create | replace`.* The table showed only a `kind:` shape. The P3a hazard asked for
  "`CONFLICT` unless overwrite (Destructive escalation)", but the gate and the annotations work per **op**. An
  escalating flag would therefore gate either every create or none. `create` (Mutating) returns `CONFLICT` when dest
  exists. `replace` (Destructive, gated) deletes the existing asset first, so UE's interactive overwrite prompt never
  appears.
- *`datatable_import` is `asset_import op=datatable`, not part of `asset_edit`.* It replaces every row (Destructive),
  and keeping it out of `asset_edit` keeps that tool's annotation non-destructive.
- *The discriminator is `op` everywhere.* The table used `target_kind:` for `reflect` and `scope:` for `project_map`.
  With `op`, per-op `Required`/`Rejects` validation applies (R2).
- *`asset_query` and `widget_query` are Ephemeral, not ReadOnly.* `thumbnail` and `render` write server-owned PNGs and
  spawn transient actors (P3a policy). Every other op in both tools is ReadOnly and safe to retry.
- *`widget_tree mode=restore` is dropped.* The op never implemented it (`NOT_IMPLEMENTED`).

Behaviour fixes riding along:
- viewport and select resolve actors strictly and return paths (v1 silently ignored unknown labels);
- viewport rotation is `rotation` [pitch,yaw,roll] (was `rotation_pyr`), and `viewport set` rejects `console`;
- `widget_render` writes only under `Saved/MCP/WidgetRenders` (P3a path hazard);
- an additive compose may not orphan an authored root: that is a `CONFLICT`; use `prune`;
- class arguments go through the v2 resolver;
- asset_info and thumbnail errors are coded (`NOT_FOUND`, `UNSUPPORTED`);
- `asset_import files` checks that the files exist before calling the editor;
- per-kind required params are validated server-side.

**Tests.**
- pytest (35 passing) covers:
  - asset_create conflict, replace, and dest/kind validation;
  - the material parent check;
  - the compose root guard;
  - strict selection;
  - viewport having no console passthrough.
- T1 (`v2_assets_test.go`) covers:
  - create → CONFLICT → replace, with each op's tier pinned;
  - asset_import validation before the editor is called;
  - viewport select, selection, an unknown actor, and console being rejected;
  - reflect echoing world, plus per-op Rejects;
  - project_config and project_map offline with no editor, where `level` gives EDITOR_UNREACHABLE;
  - PRECONDITION when no project is set;
  - widget_edit compose rejecting `remove` (with the ui toolset enabled).
- The emulator gained an asset store, selection, reflect, actor_call and `@pawn`/`@gamestate`.

**Evidence.**
- `go vet ./...` clean; gofmt clean.
- `go test ./...` all pass; `-race` on tools/e2e/bridge passes.
- pytest: 35 passed. ruff in the CI form: clean.
- Golden regenerated (97 tools).

**Gate P5a-fixes + P5b, round 2: P5a-fixes A-, P5b B.** Fixed:
1. *`asset_create op=replace` deleted the asset before validating its inputs.* A mistyped class would cost the
   user the asset. Each kind is now a *preparer* that resolves every class, parent material, texture parameter and
   row struct and returns the creation step. The delete happens only after all of them succeed. pytest pins
   `deleted == []` for a bad class, a bad parent and a bad row_struct.
2. *A failed `static_mesh` spawn left the actor behind.* An editor transaction commits on an exception. The mesh is
   now loaded before the spawn, and any failure after the spawn destroys the actor before re-raising. pytest asserts
   the level is unchanged.
3. *Image paths were likely relative.* `Paths.project_saved_dir()` is FPaths-relative to Engine/Binaries. All
   server-owned output now goes through `_saved_mcp_dir` (`convert_relative_path_to_full`). If the Go side cannot
   read a PNG it says so in `image_error` instead of silently returning no image.

Non-blocking findings, also fixed:
- widget renders get a timestamped name, so render → edit → render keeps the "before" image;
- `@controller` alias (v1 reflect target `playercontroller`);
- thumbnail reports the maps it `dirtied`;
- stale pyops notes fixed and the dead `viewport_set` console escalation removed;
- `asset_import files` passes absolute paths to the editor.

Kept: `asset_query search` takes one `folder`. T4 checklist addition: replace → create at the same path in one tick
(GC of the deleted object).

## P5c — pie / pie_observe / pie_wait / world_query / snapshot* / screenshot / capture / scene* / audio

**Landed** in `internal/tools/v2_play.go`, `internal/bridge/py/97_v2_play.py` and `internal/snapshot/store.go`. Eleven
tools, listed below; `pie_observe` and `pie_wait` are single-op.

| Tool | Ops | Notes |
|---|---|---|
| `pie` | start, stop, input | start/stop poll `editor_ping` until the state really flips; TIMEOUT has a hint |
| `pie_observe` | — | + `pawn` and `missing` |
| `pie_wait` | — | |
| `world_query` | line_trace, sphere_overlap, nav_path, project_point, instances_count, instances_list | |
| `snapshot` | take, diff, list, digest | |
| `snapshot_restore` | — | |
| `screenshot` | viewport, pie, orbit | |
| `capture` | start, status, stop, read, clear | |
| `scene` | apply, check, preview, env_preset | |
| `scene_clear` | all, prune | |
| `audio` | capture_start, capture_stop, play | |

31 v1 tools are retired (`scene_clear` keeps its name). The stdio surface is **77 tools**.

**§2.5 snapshot store.** Snapshots are Go-owned files at `Saved/MCP/snapshots/<name>.json`, written atomically.
Names are validated with no separators, so a name cannot become a path.
- `snapshot_actors` (§2.7 item 5) returns path, label, class, tags and the full transform.
- *World Partition* (§2.7 item 10): the snapshot also records the actors World Partition knows about
  (`WorldPartitionBlueprintLibrary.get_actor_descs`) but had not loaded. `Diff` (`internal/snapshot`, unit-tested)
  matches by **object path**, so a relabel is not an add/remove and a reused label is not "unchanged". An actor
  whose counterpart sat in an unloaded cell is `unknown`, never `removed`/`added`. Rotation differences wrap at
  360°, and changes under the tolerance are not moves.
- `snapshot_restore` restores transforms matched by path, as **one undo step** (`_transaction` + `modify`). It
  reports `not_restored {added, removed}`; v1 matched by label and silently picked one of two "Twin"s.
- The in-memory `level_snapshot` token mechanism and the label-keyed `scene_snapshot` files are deleted.

**Hazards closed (P3a list):**
- *scene_apply label collision.* `scene apply` matches only actors tagged `mcp_scene:<id>`. A hand-placed actor that
  shares a spec label is never adopted or overwritten: the scene actor is spawned beside it, with a warning.
- *prune split out* (§2.7 item 11). `scene apply` never deletes; `scene_clear op=prune` (Destructive) is its own
  transaction, plus `dry_run`.
- *Output paths.* Capture sessions and snapshot names are validated in Go and in Python. Screenshot filenames must be
  bare `.png` names. Audio writes to `Saved/MCP/audio`. `capture clear` needs exactly one of `session` / `all=true`
  and only touches `Saved/MCP/capture` plus `Saved/Screenshots/mcp_*`.
- *Capture actors dirty the level.* Screenshots, captures, orbit and thumbnails report the map packages they
  dirtied (`dirtied`).
- *`_pick_world` silent fallback* (§2.7 item 1, everywhere now). An unknown world is `BAD_VALUE`, `pie` is the
  canonical name, and instance queries understand `auto`.

**Deviations, each justified:**
- `world_query` op names are the ones v1 actually implemented: `line_trace`, `sphere_overlap`, `nav_path`,
  `project_point` (the table's raycast/los/overlap were placeholders).
- `screenshot op=orbit` is editor-world only. Its capture actors are spawned through the editor subsystem, so the
  v1 `world` parameter never worked for PIE.
- `screenshot`/`capture` are Ephemeral, not RO: they spawn capture actors and write files.
- `capture clear` requires an explicit `all=true` to delete everything.
- `pie_wait` keeps polling through `NOT_IN_PIE` (PIE starting up) and fails fast on any other non-retryable error.

**Tests.**
- Go unit (`internal/snapshot`): path matching, relabel vs reuse, tolerance and wrap, WP unknown, store round trip,
  name validation.
- pytest (47 passing) covers:
  - strict `_pick_world`; idempotent pie start/stop; `pie_observe` NOT_IN_PIE and `missing`;
  - `snapshot_actors`, including the WP unloaded list;
  - restore by path for duplicate labels, as one undo step;
  - scene apply never adopting a hand-placed actor; tag-scoped prune;
  - confined output names; the absolute saved dir; audio being PIE-only.
- T1 (`v2_play_test.go`) covers:
  - pie start/stop waiting for the state flip;
  - `pie_wait` met:false without PIE, met:true in PIE, and a bad predicate;
  - snapshot take → move + spawn → diff (moved A, added C) → restore → diff clean → list;
  - snapshot guards (path-like name, missing name, no project);
  - `capture clear` confinement (a file outside the dir survives);
  - scene_clear prune with dry run and the keep set; scene apply rejecting prune and a bad spec;
  - preview offline.
- The v1 `timeout_s` test now pins that `pie_wait timeout_s:1` answers `met:false`, not TIMEOUT.

**Noted for P5d:** `eval.ParsePredicate` accepts `a >>> 1` (it parses as `>`). The predicate grammar should reject
unknown operators. The playtest/scenario tools still dispatch `pie_exec`, which stays registered until P5d moves
their beats to `actor_call`.

**Evidence.**
- `go vet ./...` clean; gofmt clean.
- `go test ./...` all pass; `-race` on tools/e2e/bridge/snapshot passes.
- pytest: 47 passed. ruff in the CI form: clean.
- Golden regenerated (77 tools).

**Gate P5b-fixes + P5c, round 1: P5b-fixes A, P5c B+.** Blocking fix:
1. *A `class_filter` snapshot diffed against an unfiltered "now".* Every filtered-out actor came back as added; under
   World Partition, loaded actors of other classes came back as unknown. Fixed:
   - `snapshot.File` records `class_filter`;
   - `diff` re-takes "current" with the same filter, and refuses to compare two snapshots taken with different
     filters (INVALID_ARGUMENT);
   - `unloaded` is recorded only for unfiltered snapshots.

   Covered by T1 and pytest.

Non-blocking findings, also fixed:
- `pie_wait` returns `pie_running`, ends at once with met:false when PIE stops after having run, and says when PIE
  never ran.
- pie start/stop surfaces the last ping error (e.g. EDITOR_UNREACHABLE) plus any crash dump, instead of a generic
  TIMEOUT.
- `snapshot_restore` reports actors in unloaded WP cells as `not_restored.unknown`, never removed, and restores
  parents before attached children.
- screenshot op=pie finds HighResShot's real file (platform subfolder, suffix).
- `capture clear session` matches only that session's frames (not `a_b`'s).
- scene apply gives a scene actor whose spec label collides with a hand-placed actor the label `<label> (scene)` and
  keys scene actors by an `mcp_label:` tag, so the user's label stays unambiguous.
- `pie_observe player` restores v1's pawn player index.

Playtest's fixed post-start sleep is addressed in P5d. T4 checklist addition: HighResShot output location in 5.7.

## P5d — build / job / logs / playtest / analyze / git / git_revert / editor_lifecycle / headless

**Landed** in `internal/tools/v2_lifecycle.go`, `internal/tools/v2_ops.go` and `internal/bridge/py/98_v2_lifecycle.py`:

| Tool | Ops | Notes |
|---|---|---|
| `build` | — | async; strategy auto / livecoding / ubt |
| `job` | status, wait, cancel, list | |
| `logs` | mark, tail, since, events | |
| `playtest` | run | async |
| `analyze` | rubric, perf, image_diff, scenarios | |
| `git` | status, diff, log, checkpoint | |
| `git_revert` | — | async, checkpoint-only |
| `editor_lifecycle` | ensure_open, restart, reclaim | ensure_open and restart are async |
| `headless` | commandlet, exec, tests | async; toolset `headless` |

30 v1 tools are retired. The stdio surface is **64 tools**; the 19 still on v1 are P5e's design / desktop /
polyworld / cockpit / affordances.

**§2.5 safe shutdown** (`prepareShutdown` + `closeEditorSafely`). One routine is shared by
`editor_lifecycle restart`, `build`'s full rebuild and `git_revert`:
1. Query the **full** dirty set (`packages_state`: content and map packages, including never-saved new assets).
2. Any dirty package means `PRECONDITION {dirty}`, unless `discard_dirty`.
3. Stop PIE.
4. **Re-check** the dirty set immediately before quitting.
5. If nothing is dirty: graceful `quit_editor`. The op itself refuses while anything is dirty. The channel drops by
   design, so success is the PID exiting; after 30 s the editor is killed and `kill_fallback:true` is reported with
   the dirty set at that time.
6. If dirty and `discard_dirty`: kill, since a graceful quit would raise the save-changes modal. `discarded_dirty`
   is reported.

Relaunch passes the remembered map (`-AutoDeclinePackageRecovery` is on every launch since P4). In daemon mode the
controlled restart (`RestartLease`: lease preserved, same token) closes and relaunches; it runs after the same
dirty check, so its kill loses nothing. The v1 restart (`quit` + unconditional force-kill) and the v1 full-rebuild
close are gone.

**§2.5 editor-aware `git_revert`.**
- *Targets.* Only `umcp/cp/<n>` checkpoints. Anything else is PRECONDITION; a missing checkpoint is NOT_FOUND.
- *What changes.* The changed paths come from `git diff --name-status` against the checkpoint (working tree +
  index; Saved/Intermediate/DDC excluded). Files added since are deleted; everything else is checked out. Untracked
  files are kept and listed.
- *Editor check.* `Content/**` and `Plugins/*/Content/**` map to package names. If the editor has any of those
  loaded, the full dirty set must be clean (or `discard_dirty`), checked **synchronously**, so an unsaved package is
  an immediate PRECONDITION. The editor is then closed safely, and the revert runs as the build step with no editor
  up, followed by a relaunch on the same map. If none are loaded, the editor stays up and they are reported as
  `possibly_stale`.
- *All-or-nothing.* Every changed path is backed up to `Saved/MCP/revert-backup/<ts>` first; on any failure the
  backup is restored.
- *Result and options.* `rebuild_required` is set when Source/ or `*.Build.cs`/`*.Target.cs` changed. `dry_run`
  reports the plan.
- *A guard the tests surfaced:* `git reset -q --` with an empty path list would reset the whole index, so it never
  runs empty.

**`git checkpoint`** commits (hooks are never bypassed) and creates the annotated tag `umcp/cp/<n+1>`. With nothing
to commit it tags HEAD (`committed:false`).

**Playtest.** One orchestration replaces v1's two copies (`playtest_capture` and `scenario_run`). Fixes along the
way:
- PIE start is awaited via `editor_ping` (v1 slept a fixed 1.5 s and could capture the editor world);
- `slomo` and console beats go to `world:"pie"` (v1 sent them to the editor context, where they did nothing to the
  running game);
- exec beats use `actor_call` (with `@gamestate`/`@pawn`) and setup uses `actor_edit set_properties` in PIE;
- beat failures are collected into `beat_errors` instead of being swallowed;
- the capture world is explicit.

Since `run` is an async job, its contact sheet is written next to the frames. The new spec-layer convention: **a job
result with `image_path` gets that PNG attached as image content** by `job status/wait` and by `wait_s`.

**Other fixes:**
- `eval.ParsePredicate` is strict: a dotted path, one operator, a non-empty value. `a >>> 1` and `a >= ` are
  rejected.
- `analyze rubric` takes a JSON-tagged timeline. The eval types have no tags, so the v1 tool's schema could never
  accept the documented `{index, t_world, state}` shape.
- The companion `PRECONDITION` code maps to PRECONDITION (it was falling through to OPERATION_FAILED).
- `Bridge.Reclaim` backs `editor_lifecycle reclaim`.

**Deviations, each justified:**
- `playtest list_scenarios` → `analyze op=scenarios`: the lint forbids a read-only op beside the Exec `run`, and
  the plan's own table conflicted with its lint rule.
- `editor_lifecycle reclaim` is Mutating, not Ephemeral: it takes the channel from whichever client holds it, and
  it may sit beside the Destructive restart.
- `quit_editor` is Mutating: it refuses with anything unsaved, so it cannot lose work; `build` can reach it without
  being Destructive.
- `build strategy` keeps `auto`, so the git-diff classifier still works.
- The safe-shutdown routine lives in `tools`, not `supervisor.Shutdown`: it needs the companion ops. The supervisor
  part (the daemon's controlled restart) is reused unchanged.

**Tests.**
- Unit (`lifecycle_test.go`, stubbed process control) covers:
  - unsaved work refused with no editor contact;
  - graceful quit that stops PIE first and relaunches on the remembered map;
  - kill fallback reported;
  - `discard_dirty` killing without a graceful quit (no modal), with the discarded packages listed;
  - a package dirtied between the checks aborting before the quit;
  - reclaim without a channel.
- T1 (`v2_ops_test.go`, real git + emulator) covers:
  - git_revert dry run, then revert with the editor up and assets not loaded: files back to the checkpoint, the new
    asset deleted, the backup holding the pre-revert version, `possibly_stale` listing the level;
  - loaded + unrelated unsaved asset giving a synchronous PRECONDITION with no file touched;
  - non-checkpoint targets refused, a missing checkpoint NOT_FOUND, an offline revert with no editor;
  - the job tool;
  - playtest run → PASS, with the contact sheet arriving as image content through the job;
  - an invalid scenario rejected;
  - analyze rubric and scenarios offline.
- The git tool test now pins `umcp/cp/1`, the empty checkpoint `umcp/cp/2` (committed:false) and log checkpoints.
- Predicate strictness has its own tests.

**Evidence.**
- `go vet ./...` clean; gofmt clean.
- `go test ./...` all pass; `-race` on tools/e2e/bridge/snapshot/eval passes.
- pytest: 49 passed. ruff in the CI form: clean.
- Golden regenerated (64 tools).

**T4 additions:**
- `quit_editor` with a clean editor exits without any modal;
- restart and git_revert with `discard_dirty` and the open map dirty finish unattended with no modal after relaunch;
- `get_dirty_content_packages` includes never-saved new assets in 5.7.

**Gate P5c-fixes + P5d, round 1: P5c-fixes A-, P5d B+.** Fixed:
1. *A refused graceful quit became a kill.* If a package turned dirty after the re-check, `quit_editor` refused, and
   `closeEditorSafely` treated that as "expected while exiting", waited out the 30 s and killed: the very loss §2.5
   prevents. Now (`quitGracefully`):
   - a refusal aborts with PRECONDITION and the dirty list, and nothing is killed;
   - before any fallback kill the dirty set is re-queried, and an editor that still answers with unsaved work is
     left alone.

   Unit-tested, using a scripted-runner error hook.
2. *git_revert in a project nested inside the repository.* `diff --name-status` printed repo-relative paths, but
   files are removed and checked out relative to the project. Now `--relative -z`, which also keeps non-ASCII names
   unquoted. T1 runs a revert of `My Game/` inside a repo with a non-ASCII, space-containing untracked asset.
3. *restore's `unknown` could never fill.* Python now computes the WP-known-but-unloaded set **at restore time**.
   The test models a far actor that exists but is not loaded, plus a genuinely gone one.

Daemon mode (the rejected "its kill loses nothing" claim): `Deps.Restart` takes a `session.RestartPlan{Stop, Build,
Map}`. `RestartLease` now:
- marks the lease Restarting, then runs the graceful `Stop`; a refusal aborts with the lease back on the running
  editor (`Router.AbortRestart`) and nothing killed;
- kills only what is left, runs `Build`, then **always relaunches** (a failed build or revert step no longer
  strands the session without an editor) and returns the step's error after;
- reopens `Map`.

Daemon tests cover the build-failure relaunch and the refused-stop abort.

Non-blocking findings, also fixed:
- checkpoint tag numbering retries on "already exists" (concurrent checkpoints);
- `checkpoint paths` commits only those paths;
- git_revert says that history is not rewritten, and relaunches on the default map when the open map was added
  since the checkpoint;
- `job status/wait` reports `image_error` when the playtest image is unreadable;
- playtest setup failures land in `beat_errors`, and a PIE that dies on start or `capture_start` is diagnosed from
  the crash dump.

**Gate P5d after fixes: A-** (no blocking findings). Non-blocking findings, fixed:
- *Cancellation never escalates to a kill.*
  - `waitExit` watches the job context.
  - A cancelled graceful wait aborts with CANCELLED and never takes the kill fallback.
  - Once the editor is down, relaunch and re-pin run on a detached context, in stdio and in the daemon's
    `RestartLease`, so a cancelled restart never strands the session without an editor.
  - Unit-tested: `TestCancelledShutdownNeverKills`.
- `untracked_kept` stays a proper list, so names with spaces survive.
- The nested-project test also covers a file committed after the checkpoint (deleted).
- `editor_lifecycle` documents RESTART_IN_PROGRESS during the shutdown.
- `git checkpoint` commits only the paths it staged.
- Tag allocation retries by checking `rev-parse`, not git's English error text.

## P5e — toolsets / project / design / desktop / polyworld; the v1 adapter is gone

**Landed** in `internal/tools/v2_extras.go`:

| Tool | Toolset | Ops / shape | Notes |
|---|---|---|---|
| `toolsets` | core | list, enable, disable, describe | |
| `project` | daemon | attach, list, release | |
| `design_audit` | design | `kind` + one `input` object | 11 kinds; strict per-kind decode, unknown fields are INVALID_ARGUMENT; R2 caps op enums at 8, so `kind` is a param |
| `design_explore` | design | sweep, explore | the design package gained snake_case JSON tags (plan §0 PascalCase leak) |
| `desktop_capture` | desktop | list_windows, screen, window | read-only: `focus` moved to `desktop_input`; non-Windows ⇒ UNSUPPORTED_PLATFORM |
| `desktop_input` | desktop | focus, mouse, keys, type | Exec |
| `polyworld` | polyworld | status, build, select, road | PIE only |
| `polyworld_demolish` | polyworld | — | PIE only |

**Surface: 45 tools, 36 core.** All 155 v1 names are accounted for (`TestMigrationAccounting`).

**The v1 adapter is deleted:**
- `spec.Typed`, `add`/`addIn`, `registrar`, `v1Tier`/`v1ToolTiers`, `structHandler`/`textHandler`;
- the `affordances` package;
- `register*` for design, desktop, company, cockpit, project and headless.

`register.go` is now just `Specs(d)` over the v2 spec families. The leftover helpers were renamed after what they are
(`montage.go`, `images.go`, `git_support.go`, `playtest_support.go`, `visual_support.go`, `scene_support.go`,
`headless_support.go`, `design_inputs.go`).

**Hazards closed:**
- *company `world=auto`.* The companion's `company_*` ops are wrapped with `_pie_only`: NOT_IN_PIE, never the editor
  level. pytest covers it.
- *cockpit token.* `toolsets describe` returns the cockpit URL with its token fragment stripped. The token lets
  whoever holds it approve gates, so the agent must never see it. The human gets the tokened URL from the launcher.

**`toolsets describe` ⇒ per-op `needs`.** A new `OpSpec.Needs` (pie, plugin, navmesh, project, engine) is annotated
on every op that has such a precondition. `describe` returns tier, async and needs per op (editor is implied unless
the tool is offline), plus the rollback ladder and the cockpit status. This replaces the v1 affordances manifest,
which was keyed by retired names.

**Budgets** (`TestToolListBudgets`):
- 45 tools / 36 core;
- core tools/list **44,803 B ≤ 45,000**; all toolsets **58,559 B ≤ 75,000**.

How the budget was met:
1. Mechanical savings: no duplicate title in annotations; no `op` description when the enum lists the ops; nullable
   `["null", T]` collapsed to `T` (`dropNull`, which also makes validation stricter).
2. Descriptions trimmed, never tools: ~30 tool descriptions and ~20 property descriptions.
3. `scene`'s nested Checks/Layout schemas became documented objects, decoded strictly server-side.

**Tests:**
- **Every (tool, op) runs end to end** (`TestEveryOpIsWired`, §2.4). 121 cells against the emulator with generated
  arguments, a real git repo with a checkpoint, a log file and PNG fixtures. Each call must come back enveloped,
  never INTERNAL and never UNKNOWN_OP; via the harness, every dispatched Python op must be declared in `Reaches`.
  It immediately found a real bug: git's error text was only stderr, while some failures print on stdout;
  `build.Run` now reports both.
- **Both dispatch backends** (`TestBothBackends`): lifecycle, CONFLICT details (native carries them in
  `result.details`), UNKNOWN_OP and a retryable EDITOR_BUSY behave identically over uexec and native. The emulator is
  now a `bridge.NativeDispatcher`; this is the `bridgetest → bridge` edge the plan's DAG already allows.
- **Crash → relaunch** (`TestDaemonEditorCrashThenReattach`): a leased editor dies, and the reaper (fake liveness)
  drops the lease. The holder gets PRECONDITION with the re-attach hint (stale `project_attach` hints fixed), and
  re-attaching spawns a fresh editor that answers.
- **Toolsets:**
  - a disabled toolset's tool gives PRECONDITION with a hint;
  - enable/disable change the session's tool list, and core cannot be disabled;
  - describe reports needs;
  - list_changed reaches the client in T1 **and over the real binary in T3** (stdio).
- Design audits drive the real audit and balance code through the tools; the desktop coord-hint logic is unit-tested
  on `shotData`.
- The live test (`-tags live`) is rewritten for v2: actor_edit spawn/delete and actor_query get/find.

**Counts and coverage.**
- T1 + T3: 59 scenario functions, plus 121 every-op cells and 8 two-backend cells.
- Fault scenarios (≥ 14):
  - vanished client, sweeper with a held stream, vanish mid-job, session churn;
  - Python exception, mutating timeout ⇒ outcome unknown, editor not running;
  - companion reinstall after an editor restart, channel drops;
  - unknown op ×2 backends, EDITOR_BUSY ×2;
  - editor crash ⇒ re-attach, PIE stopping during a wait;
  - refused git_revert with unsaved work, refused/cancelled/hung shutdowns (unit).
- Merged coverage, now gated in CI by `scripts/coverage.sh`:

  | Scope | Coverage | Floor |
  |---|---|---|
  | repo | 77.9 % | 75 |
  | tools | 70.5 % | 70 |
  | app | 100 % | 80 |
  | config | 95.1 % | 90 |

**Evidence.**
- `go vet ./...` (also `-tags live`) clean; gofmt clean.
- `go test ./...` all pass; `-race ./internal/...` passes.
- pytest: 50 passed. ruff in the CI form: clean. archtest passes.
- Golden regenerated.

**Not run: the §3.5 tool-selection eval.** It needs `ANTHROPIC_API_KEY` and real API spend, and the user asked to
approve that before it runs. The harness design is in the plan; it is the first item to take up with the user, along
with P7.

**Gate P5e, round 1: B+.** Blocking: *`polyworld`'s description was a copy of `build`'s.* The description-trim
script matched the op name `build` inside polyworld's op list. Restored. A new lint rule (descriptions must be
non-empty and unique) would have caught it, and pins it now.

Non-blocking findings, fixed:
- *The every-op sweep tolerated any non-INTERNAL error.* It now pins its 8 expected error cells:
  - build/headless need an engine (PRECONDITION);
  - job lookups of a fake id and an unknown UFUNCTION (NOT_FOUND).

  Every other cell must succeed, and every async cell's job must reach its expected final state. It runs over
  **both backends** (2 × 121 cells).
- *Strict schemas reject `null`.* Top-level `null` arguments are now treated as omitted before validation, for
  clients that send null for omitted optionals.
- Class syntax (Module.Class, short names, CONFLICT) is back on `actor_edit class`.
- *Coverage margin.* Helper unit tests raise tools to 72.6 % (repo 78.3 %).

The §3.5 tool-selection eval still needs: the user's spend approval after a 5-task pilot, the `cmd/tooleval` harness
with a stateful world model, the mined task corpus, and a v1 arm served from `v1-final`. The free fallback (replaying
transcript tool calls against the v2 schemas, metric (b)) needs the aesir/poly-world session transcripts. Both go to
the user together with P7.

## P6 — docs

- **Generated** (`go generate ./internal/tools` → `internal/tools/gendocs`; `TestGeneratedDocsAreCurrent` fails CI on
  drift):
  - `docs/tools.md`: every tool, its ops with tier, async, required params and needs, and its params; plus tiers,
    needs and the rollback ladder;
  - `docs/migration-v2.md`: what changed everywhere, then all 155 v1 names → their exact v2 call, from
    `internal/tools/migration.go` `V1Calls`. `TestMigrationAccounting` checks each call's tool against `Replaces`.
- **Written:**
  - `README.md`: quick start, tool map, configuration, `.umcp.json`, developing;
  - `docs/architecture.md`: request flow, spec table, companion sections, command channel, topologies, safety
    mechanisms, package map;
  - `docs/operations.md`: build, deploy, package recovery, run modes, one Go peer per editor, validation, the
    **T4 live checklist**, the plugin rebuild drill, the **rollback runbook**;
  - `CHANGELOG.md` (v2.0.0);
  - `CLAUDE.md`: commands, rules for tools/companion ops/tests, UE 5.7 Python gotchas;
  - `plugin/UnrealMCP/README.md`: v2 tool names. Writing it surfaced that `capture camera_mode=player` (the plugin's
    `game_scene` POV) was missing from the enum; restored.
- `deploy/mcp.json.tmpl` is unchanged: stdio with project and engine env. The daemon is documented in operations.md.

**Evidence.**
- `go vet ./...` (+ `-tags live`) clean; gofmt clean.
- `go test ./...` all pass; `-race ./internal/...` passes.
- Coverage gates: repo 78.3, tools 72.6, app 100, config 95.1.
- pytest: 50 passed. ruff in the CI form: clean.
- Budgets: core 44,882 B, all 58,688 B. Docs and golden regenerated.

**Autonomous scope complete (P0–P6).** Stopping here, as agreed with the user, before:
- **P7**: live T4 on aesir-wave-defense and poly-world, deploy, consumer-repo `.mcp.json`/prompt updates, the
  rollback drill;
- **the paid §3.5 eval**.

Both need the user.

**Gate P5e-fixes + P6, round 1: P5e A, P6 B+.** Blocking: *the docs described an approval gate and stdio
`.umcp.json` toolsets that nothing wired.* `app.Options.Gate` was never set, so `gate_policy: "require"` silently
allowed everything. Fixed in the code, not just the docs:
- `require` now **fails closed**. The new `spec.DenyGate` refuses destructive and exec ops with an explanation
  (`spec.NoApprovalSurface`). The daemon refuses to attach a `require` project and releases the lease. Wiring the
  cockpit's approval UI to tool calls is P7 work.
- `app.StartupPolicy` applies `.umcp.json` toolsets plus the new `-toolsets`/`UMCP_TOOLSETS` flag at stdio startup.
- T1 (`policy_test.go`):
  - a mutating op passes, but a destructive delete and a python run are refused and the delete never runs;
  - toolsets from the file and the flag are on;
  - an invalid policy is rejected at startup;
  - the daemon refuses a `require` project.

Docs say exactly this (README, operations, architecture, CHANGELOG).

Non-blocking notes, also fixed:
- migration rows name their toolset when it is not core, with an enable note;
- the `spawn_actor` row is complete;
- the stale cockpit comment in main.go is fixed;
- the paid eval sits under its own heading in operations.md;
- the package-count deviation is recorded (29 top-level vs the plan's 28: `calibration` and `version` stayed separate
  leaf packages).
