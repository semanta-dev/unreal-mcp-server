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
3. Optional `<game>/.umcp.json`: `toolsets` enabled at session start (stdio) or at `project op=attach` (daemon),
   `gate_policy` (`off` | `require` — `require` currently fails closed: destructive and exec ops are refused and the
   daemon refuses to attach the project, because no approval surface is wired to tool calls yet), and
   `keep_package_recovery`. `-toolsets`/`UMCP_TOOLSETS` adds toolsets for a stdio session.
4. Optional plugin (`plugin/UnrealMCP`): needed for `pie op=input`, `audio`, `capture source=game_scene`,
   `widget_query op=render`, the `pie op=start` Blueprint pre-flight and the cockpit. Copy it into `<game>/Plugins/`,
   then `build strategy=ubt`. Keep the project's copy current: an older build answers `PRECONDITION` (with
   `details.editor_code: PLUGIN_MISSING`) for the features it lacks, `pie op=start` reports a `blueprint_preflight`
   starting with `unavailable`, and `ignore_blueprint_errors` has no effect.

**Package recovery.** Every editor launch by the server (`editor_lifecycle`, `build`, `git_revert`, crash relaunch,
`-auto-relaunch`) passes `-AutoDeclinePackageRecovery`, so an unattended editor is never blocked by the "restore
packages" dialog after a kill or crash. The cost: autosave recovery after a genuine crash is discarded. Projects also
driven by hand can set `"keep_package_recovery": true`.

## Run

**stdio** (one client, one editor): the client starts `unreal-mcp.exe` with `UMCP_PROJECT_DIR`/`UMCP_ENGINE_DIR`.
The editor must be running with the project open (or call `editor_lifecycle op=ensure_open`).

**daemon** (several agents and projects): `unreal-mcp.exe -daemon-addr 127.0.0.1:6111 -engine <UE_5.7>`, and point
clients at `http://127.0.0.1:6111/` (StreamableHTTP). Each session calls `project op=attach project=<dir>` first;
editors are spawned on demand and kept warm between sessions. A cold start that outlasts the call returns a
retryable `EDITOR_BUSY` — attach again to keep waiting (the start continues; if the session ends meanwhile, the editor
comes up warm and unleased, and the next session of that project adopts it). Cold starts of one project run one at a
time, and a spawn binds only the editor it launched. A project already open in an editor the daemon did not launch
is refused (`PRECONDITION`; Windows, where process command lines are readable): close it first, or drive it with a
stdio server. While a lease restarts, editor calls get a retryable `EDITOR_BUSY`, as do `build`, `git_revert`,
`editor_lifecycle` and `headless`; `job` and read-only offline tools keep working. A session idle for `-session-idle` (30 min) is ended;
its editor drains while a project job still runs. On start, the daemon reconciles its records and kills editors an
earlier daemon left behind.

**cockpit**: `-cockpit on` attaches to the plugin's MCPCore channel and opens the browser control panel (its URL,
with the access token, is printed by the launcher — tools never return it); once it is ready, ops dispatch over the
native framed channel (`-log-level debug` logs `backend=native` per op). Its gate panel is not wired to tool-call
approvals in v2.0: `gate_policy: "require"` refuses gated ops instead of waiting.

**Modal dialogs.** An editor modal (a "save changes?", a Blueprint-errors prompt) holds the game thread, so every
remote command waits on it. `pie op=start` pre-flights Blueprint compile errors (plugin) and, if the editor stops
answering anyway, cancels PIE's "Blueprint Compilation Errors" dialog (Windows); when it times out it lists the
editor process's other windows, never touching them (enable the `desktop` toolset: `desktop_capture op=window` shows
one, `desktop_input` answers it). A timed-out call never wedges the channel: the next call reconnects (and still waits
while a dialog is open). A full `build` refuses while the project's editor runs but does not answer (Windows).

**Project binding.** With `-project`, the server only binds an editor of that project — never another project's
editor, even while its own is relaunching (calls fail with `EDITOR_UNREACHABLE` until it is back). The editor's
project root must be that directory (or contain it): an editor opened from another checkout, worktree or a
`subst`/junction path is not found — point `-project` at the directory the editor opened.

**One Go peer per editor.** The editor's remote-execution node holds a single command connection: two clients on one
editor steal it from each other (`EDITOR_BUSY`; `editor_lifecycle op=reclaim` takes it back explicitly). Give each
concurrent client its own `-command-addr` port, and never point a v1 and a v2 server at the same editor at once.

## Validate

Last live run: [`validation/T4-2026-10-03.md`](validation/T4-2026-10-03.md). Script a run with `cmd/mcpcall`: one line
per call on stdin — `{"tool": "...", "args": {...}, "note": "..."}`, or `{"sleep_s": N}`; `#` lines are comments — and
one JSON result per line on stdout (`-images <dir>` saves returned images; `-url` targets a daemon). Run on scratch
copies with their own multicast group (see `CLAUDE.md`).

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
9. Daemon: two projects, two sessions, no cross-talk; restart and revert keep the lease and reopen the map; attach
   next to a hand-opened editor is refused; a cold start longer than the call resumes on re-attach.
10. Modal dialogs: `pie op=start` on a project with a broken Blueprint → `PRECONDITION` listing it (plugin), or with
    an older plugin the guard cancels the dialog; the next call answers (no wedged channel).
11. Cockpit: with `-cockpit on`, ops log `backend=native`; a failure keeps its details over the native channel.
12. Rollback drill (below), then forward again.

**Tool-selection eval** (plan §3.5) — not a live-editor check: paid API runs of agents choosing tools on mined tasks;
needs explicit approval of the spend after a 5-task pilot.

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
