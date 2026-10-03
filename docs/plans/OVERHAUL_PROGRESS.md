# v2 Overhaul — Progress & Evidence Log

Plan: [`OVERHAUL_PLAN.md`](../OVERHAUL_PLAN.md) (r6, unanimous A+). One section per phase: what was done,
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
