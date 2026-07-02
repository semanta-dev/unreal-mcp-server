# Unreal MCP Server (Go)

MCP (Model Context Protocol) server that exposes a live Unreal Editor session as
tools, letting Claude Code (or any MCP client) drive the editor: run Python,
inspect and edit levels, take screenshots, control play sessions, orchestrate
C++ builds, observe PIE, and checkpoint with git.

This is the **Go rewrite** of the original Python server. See
[`GO_REWRITE_PLAN.md`](GO_REWRITE_PLAN.md) for the design (A+-graded by the
CTO/GameDev agent gates) and [`PROGRESS.md`](PROGRESS.md) for build status. The
original Python server (`server.py`, `unreal_bridge.py`, `remote_execution.py`)
is kept as the rollback path.

## How it works

```
Claude Code ── stdio ──> unreal-mcp.exe ── UDP/TCP (loopback) ──> Unreal Editor
                           │                                       (PythonScriptPlugin
  internal/uexec  protocol port  ─────────┘                        remote execution)
  internal/bridge companion-module dispatch (mcp_bridge.py, hot-loaded)
  internal/tools  39 MCP tools
```

- `internal/uexec` — Go port of Epic's remote-execution wire protocol (UDP-multicast
  discovery + TCP reverse-connect command channel).
- `internal/bridge` + `internal/snippets` — hot-loads a companion Python module
  (`mcp_bridge.py`) into the editor's `__main__` and dispatches structured ops with
  base64-JSON args (Go ships data, never hand-built Python — injection-safe).
- `internal/tools` — the MCP tools over the official `modelcontextprotocol/go-sdk`.

Remote execution must be enabled in the project's `Config/DefaultEngine.ini` under
`[/Script/PythonScriptPlugin.PythonScriptPluginSettings]` (`bRemoteExecution=True`),
and the editor must be running with the project open for tools (other than listing) to work.

## Build

```powershell
.\scripts\bootstrap.ps1     # installs a pinned Go toolchain (once)
.\scripts\build.ps1         # -> dist/unreal-mcp.exe (static, no runtime/venv)
```

## Run / configure

Registered in the consuming project's `.mcp.json` (see `deploy/`). Key flags/env:

| Env | Flag | Default | Purpose |
|---|---|---|---|
| `UMCP_PROJECT_DIR` | `-project` | (req. for screenshots/logs/git/build) | project dir + node selection |
| `UMCP_ENGINE_DIR` | `-engine` | `D:/Unreal/Engine/UE_5.7` | Build.bat / editor launch |
| `UMCP_COMMAND_ADDR` | `-command-addr` | `127.0.0.1:6776` | TCP reverse-connect port (unique per concurrent client) |
| `UMCP_SNIPPET_MODE` | `-snippet-mode` | `hotload` | companion delivery: `hotload`\|`ondisk` |
| `UMCP_AUTO_RELAUNCH` | `-auto-relaunch` | `false` | relaunch the editor if it disappears |
| | `-selftest` | | connect + editor_status round-trip, exit non-zero on failure |
| | `-version` | | print version and exit |

## Tools (39)

**Parity (16, frozen names):** `editor_status`, `execute_python`, `execute_console_command`,
`open_level`, `list_actors`, `get_actor`, `spawn_actor`, `delete_actor`, `set_actor_transform`,
`list_assets`, `import_assets`, `save_all`, `take_screenshot`, `start_play`, `stop_play`,
`live_coding_compile`.

**Build/lifecycle:** `build_compile` (auto livecoding-vs-full + diagnostics, async job),
`job_status`, `job_cancel`, `project_ensure_open`, `editor_restart`.

**PIE/verify:** `pie_observe`, `pie_wait_until` (deterministic predicate wait), `pie_exec`, `pie_screenshot`.

**Logs:** `logs_mark`, `logs_tail`, `logs_since`.

**Git:** `git_status`, `git_diff`, `git_checkpoint`, `git_revert_to`, `git_log`.

**Authoring:** `apply_level_recipe`, `level_snapshot`, `level_diff`, `asset_info`, `asset_reimport`,
`create_material_instance`.

## Testing

```powershell
go test ./...                                   # unit + in-memory MCP + git-vs-temp-repo
go test -tags integration ./internal/uexec/     # real loopback multicast (Windows)
.\dist\uspike.exe -project <aesir-dir>          # live protocol gate (editor must be open)
.\dist\abparity.exe                             # A/B parity vs the Python server (both registered)
```

`go test -race` needs a C toolchain (mingw locally, or the Linux CI job runs it).

## Migration

`deploy/mcp.ab.json` runs the Go server alongside Python (Go on `:6777`) for the A/B parity diff;
`deploy/mcp.cutover.json` makes Go the default `unreal` with Python kept as `unreal-py` for one-line
rollback.

## Gotchas learned the hard way (encoded in `mcp_bridge.py`)

- `unreal.Rotator(a, b, c)` is **(roll, pitch, yaw)**, not (pitch, yaw, roll). A sun with positive
  pitch points *up* and the level renders pitch-black.
- The editor throttles when backgrounded; the server disables `bThrottleCPUWhenNotForeground` via the
  `EditorPerformanceSettings` CDO on connect (UE 5.7 hides the settings type from Python).
- `take_screenshot` uses an on-demand SceneCapture2D so it renders even when backgrounded (editor world
  only, NOT during PIE — use `pie_screenshot`/HighResShot during play).
- UENUM properties (e.g. wave state) are read as their enumerator **name** (UPPER_SNAKE_CASE) so
  `pie_wait_until` string predicates match.
