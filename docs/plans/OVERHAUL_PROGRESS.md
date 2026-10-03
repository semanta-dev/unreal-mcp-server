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
