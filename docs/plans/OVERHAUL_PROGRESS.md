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
