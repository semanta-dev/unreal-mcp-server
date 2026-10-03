# Operations

Build, deploy, run, validate and roll back the server. The design behind each step is in
[`architecture.md`](architecture.md).

## Build

```powershell
.\scripts\bootstrap.ps1          # installs the pinned Go toolchain once
.\scripts\build.ps1              # -> dist\unreal-mcp.exe (version stamped from git tags)
.\dist\unreal-mcp.exe -version
```

CI (`.github/workflows/ci.yml`) runs on every push: `go vet`, gofmt, the stdout-purity gate, `go test -race`, the
merged coverage gates (`scripts/coverage.sh`), companion compile + ruff + pytest, and a Windows build with the real
loopback multicast integration test.

## Deploy into a game project

1. In the game project, enable Python remote execution (`Config/DefaultEngine.ini`):
   `[/Script/PythonScriptPlugin.PythonScriptPluginSettings] bRemoteExecution=True`.
2. Render `.mcp.json`:
   `.\scripts\render-mcp-config.ps1 -ProjectDir <game> -EngineDir <UE_5.7>` (from `deploy/mcp.json.tmpl`).
3. Optional `<game>/.umcp.json`: `toolsets` enabled at session start, `gate_policy` (`off` | `require`),
   `keep_package_recovery`.
4. Optional plugin (`plugin/UnrealMCP`): needed for `pie op=input`, `audio`, `capture source=game_scene`,
   `widget_query op=render` and the cockpit. Copy it into `<game>/Plugins/`, then `build strategy=ubt`.

**Package recovery.** Every editor launch by the server (`editor_lifecycle`, `build`, `git_revert`, crash relaunch,
`-auto-relaunch`) passes `-AutoDeclinePackageRecovery`, so an unattended editor is never blocked by the "restore
packages" dialog after a kill or crash. The cost: autosave recovery after a genuine crash is discarded. Projects also
driven by hand can set `"keep_package_recovery": true`.

## Run

**stdio** (one client, one editor): the client starts `unreal-mcp.exe` with `UMCP_PROJECT_DIR`/`UMCP_ENGINE_DIR`.
The editor must be running with the project open (or call `editor_lifecycle op=ensure_open`).

**daemon** (several agents and projects): `unreal-mcp.exe -daemon-addr 127.0.0.1:6111 -engine <UE_5.7>`, and point
clients at `http://127.0.0.1:6111/` (StreamableHTTP). Each session calls `project op=attach project=<dir>` first;
editors are spawned on demand and kept warm between sessions. A session idle for `-session-idle` (30 min) is ended;
its editor drains while a project job still runs.

**cockpit**: `-cockpit on` attaches to the plugin's MCPCore channel and opens the browser control panel (its URL,
with the access token, is printed by the launcher — tools never return it). With `gate_policy: "require"`,
destructive and exec ops wait there for approval.

**One Go peer per editor.** The editor's remote-execution node holds a single command connection: two clients on one
editor steal it from each other (`EDITOR_BUSY`; `editor_lifecycle op=reclaim` takes it back explicitly). Give each
concurrent client its own `-command-addr` port, and never point a v1 and a v2 server at the same editor at once.

## Validate

```powershell
.\dist\unreal-mcp.exe -selftest -project <game>   # discovery, companion install, editor_status
go test -tags live -run TestLiveSpawnGetDelete ./internal/tools/   # spawn/get/delete against a running editor
```

**Live checklist (T4)** — run on each target project (aesir-wave-defense, poly-world) before a release. It covers what
the emulator cannot:
1. `editor op=health`; `actor_query`/`actor_edit` in editor and PIE worlds, including `@pawn` and PIE path translation;
   a duplicated label returns CONFLICT; class short names resolve (and are CONFLICT when ambiguous).
2. Undo: `actor_edit` and `snapshot_restore` are one Ctrl+Z step each.
3. `asset_create op=replace` over an existing asset (delete, then create at the same path in the same tick).
4. `screenshot op=viewport|pie|orbit`, `asset_query op=thumbnail`, `widget_query op=render` return images (absolute
   paths); HighResShot's real output location; `capture start/stop` with each source.
5. Snapshots under World Partition: an actor in an unloaded cell is `unknown`, never removed.
6. Safe shutdown: `editor_lifecycle op=restart` with nothing dirty quits gracefully (no modal); with an unrelated
   unsaved asset → PRECONDITION listing it; `discard_dirty` with the open map dirty completes unattended; after
   relaunch `desktop_capture op=list_windows` shows no restore / save-changes / crash-reporter window. Same after a
   forced crash and relaunch. `get_dirty_content_packages` includes a never-saved new asset.
7. `git_revert`: checkpoint → change + save → revert → `level op=open` → the file hash equals the checkpoint; an asset
   open in an editor tab during the revert; a nested project directory.
8. `playtest op=run` with a saved scenario: verdict, contact sheet, crash diagnosis on a deliberately broken map.
9. Daemon: two projects, two sessions, no cross-talk; restart and revert keep the lease and reopen the map.
10. The tool-selection eval (plan §3.5) — paid API runs; needs explicit approval.

## Plugin rebuild drill

After changing `plugin/UnrealMCP/Source`: `build strategy=ubt` (closes the editor safely, runs Build.bat, relaunches
on the same map), then `editor op=health expect_version=<companion version>` and the T4 items that use the plugin.

## Rollback runbook

The v1 server is tagged `v1-final` (its companion namespaces itself in `__main__`; v2 lives in `__main__._mcp2`, so
both can be resident during a switch).

1. Stop every v2 client (stdio servers exit on stdin EOF; stop the daemon process).
2. Check out `v1-final`, build it, and point the project's `.mcp.json` back at the v1 binary (`deploy/mcp.json.tmpl`
   at that tag).
3. Start the client. v1 reinstalls its companion and reclaims the plugin's native entry point
   (`_mcp_dispatch_native`), which v2 had pointed at itself; v2's module stays resident but unused.
4. Verify with v1's `editor_status`. Nothing in the project's content depends on the server version: there is no
   data migration to undo. Checkpoint tags (`umcp/cp/*`) are plain git tags and stay valid.

To go forward again, repeat with the v2 build; v2's `ClaimNative` takes the native entry point back when the cockpit
attaches.
