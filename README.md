# Unreal MCP Server (v2)

An MCP server that lets Claude Code (or any MCP client) drive a live **Unreal Editor 5.7**: read and edit levels
and assets, play the game and watch it, film and score playtests, build C++, and roll mistakes back — through 45
tools generated from one spec table.

```
MCP client ── stdio ─────────▶ unreal-mcp ── UDP/TCP (loopback) ──▶ Unreal Editor (PythonScriptPlugin
           └─ HTTP (daemon) ─▶  (Go, one       remote execution  ──▶  remote execution) + v2 companion module
                                 static binary)  [native channel] ──▶  UnrealMCP plugin (optional, MCPCore)
```

- **Tools**: [`docs/tools.md`](docs/tools.md) (generated) — 36 core tools plus optional toolsets (`daemon`,
  `headless`, `design`, `ui`, `desktop`, `polyworld`).
- **Coming from v1?** [`docs/migration-v2.md`](docs/migration-v2.md) maps all 155 v1 tool names to their v2 calls.
- **How it works**: [`docs/architecture.md`](docs/architecture.md). **Running it**: [`docs/operations.md`](docs/operations.md).

## Quick start

1. Enable Python remote execution in the game project's `Config/DefaultEngine.ini`:
   ```ini
   [/Script/PythonScriptPlugin.PythonScriptPluginSettings]
   bRemoteExecution=True
   ```
2. Build (Windows):
   ```powershell
   .\scripts\bootstrap.ps1   # pinned Go toolchain, once
   .\scripts\build.ps1       # -> dist\unreal-mcp.exe
   ```
3. Register it in the game project's `.mcp.json`:
   ```powershell
   .\scripts\render-mcp-config.ps1 -ProjectDir <game project> -EngineDir <UE_5.7>
   ```
4. Open the project in the editor and start your MCP client. `editor op=status` should answer.

## What an agent gets

| Area | Tools |
|---|---|
| Editor & code | `editor`, `python`, `console`, `level`, `editor_lifecycle`, `build`, `logs` |
| Actors & world | `actor_query`, `actor_edit`, `actor_call`, `reflect`, `world_query`, `viewport` |
| Assets & UI | `asset_query`, `asset_create`, `asset_edit`, `asset_import`, `widget_query` (+ `widget_edit`, toolset `ui`) |
| Play & see | `pie`, `pie_observe`, `pie_wait`, `screenshot`, `capture`, `audio`, `playtest`, `analyze` |
| Build levels | `scene`, `scene_clear`, `snapshot`, `snapshot_restore` |
| Project | `project_map`, `project_config`, `git`, `git_revert`, `job`, `toolsets` |

Every call returns a structured object; every failure is `{"error": {code, message, hint, retryable, outcome,
details}}` with a closed code set. Ops are tiered `readonly` → `ephemeral` → `mutating` → `destructive` → `exec`.
With `gate_policy: "require"`, destructive and exec ops need human approval — in this version no approval surface is
wired to tool calls yet, so `require` **fails closed**: those ops are refused (and the daemon will not attach such a
project). Leave it `off` to allow them.

**Rollback ladder:** `snapshot_restore` (actor transforms) → `scene_clear` (a scene's actors) → `git_revert` (files, to
a `git op=checkpoint`; closes and relaunches the editor safely when the assets are loaded).

## Configuration

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `-project` | `UMCP_PROJECT_DIR` | — | the project directory: editor selection, logs, git, snapshots, project config |
| `-engine` | `UMCP_ENGINE_DIR` | `UE_ENGINE_DIR`, else the Epic-launcher install for the project | editor launch, Build.bat, headless |
| `-daemon-addr` | `UMCP_DAEMON_ADDR` | — (stdio) | run the multi-project daemon (StreamableHTTP) on e.g. `127.0.0.1:6111` |
| `-cockpit` | `UMCP_COCKPIT` | `off` | attach the MCPCore cockpit (native channel + browser control panel) |
| `-command-addr` | `UMCP_COMMAND_ADDR` | `127.0.0.1:6776` | TCP reverse-connect port (unique per concurrent client) |
| `-snippet-mode` | `UMCP_SNIPPET_MODE` | `hotload` | companion delivery: `hotload` \| `ondisk` |
| `-auto-relaunch` | `UMCP_AUTO_RELAUNCH` | `false` | relaunch the editor if it disappears (needs `-project` + engine) |
| `-discovery-timeout`, `-command-timeout` | `UMCP_DISCOVERY_TIMEOUT`, `UMCP_COMMAND_TIMEOUT` | | protocol timeouts |
| `-log-level`, `-log-format` | `UMCP_LOG_LEVEL`, `UMCP_LOG_FORMAT` | `info`, `json` | logs (stderr only — stdout is the MCP stream) |
| `-toolsets` | `UMCP_TOOLSETS` | — | comma-separated toolsets enabled at session start (stdio), e.g. `design,ui` |
| `-session-idle` | | `30m` | daemon: end an idle MCP session |
| `-selftest`, `-version` | | | connect + `editor_status`, exit / print the version |

Per project, `<project>/.umcp.json`:

```json
{ "toolsets": ["design", "ui"], "gate_policy": "require", "keep_package_recovery": false }
```

`toolsets` are enabled at startup (stdio, together with `-toolsets`) or on `project op=attach` (daemon, which takes toolsets only from `.umcp.json`; an invalid file refuses the attach). `keep_package_recovery: true` stops the server from passing `-AutoDeclinePackageRecovery` when it launches the
editor (keep it for projects also edited by hand; see operations.md).

## Developing

```powershell
go test ./...                   # unit, in-memory MCP e2e (T1), binary smoke (T3)
go test -race ./...             # needs a C toolchain (or the Linux CI job)
bash scripts/coverage.sh        # merged coverage gates (repo 75, tools 70, app 80, config 90)
go generate ./internal/tools    # regenerate docs/tools.md + docs/migration-v2.md
.venv\Scripts\python -m pytest internal/bridge/py/tests   # companion contract tests (T2)
go run ./cmd/mcpcall -- dist\unreal-mcp.exe -project <game> < calls.jsonl   # scripted live calls (T4)
```

Tools are added as a `spec.Spec` in `internal/tools/v2_*.go` (ops with tiers, timing, required/rejected params and
the companion ops they reach) plus, when they need the editor, an op in `internal/bridge/py/`. The lint
(`TestV2SpecsLint`), the every-op sweep (`TestEveryOpIsWired`), the byte budget (`TestToolListBudgets`) and the
generated docs check keep the surface honest. See [`CLAUDE.md`](CLAUDE.md).
