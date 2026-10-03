# Changelog

## v2.0.0 — unreleased

A ground-up overhaul (plan: [`docs/plans/OVERHAUL_PLAN.md`](docs/plans/OVERHAUL_PLAN.md); phase-by-phase evidence:
[`docs/plans/OVERHAUL_PROGRESS.md`](docs/plans/OVERHAUL_PROGRESS.md)). **Breaking**: tool names and arguments changed;
see [`docs/migration-v2.md`](docs/migration-v2.md) for all 155 v1 names.

### Tool surface
- 155 v1 tools → **45** (36 core + optional toolsets `daemon`, `headless`, `design`, `ui`, `desktop`, `polyworld`),
  generated from one spec table; core tools/list ≤ 45 KB (was ~100 KB).
- One argument vocabulary: `op`, `world` (`editor|pie|auto`, explicit for edits), `actor` (label, object path,
  `@gamestate`, `@pawn`, `@controller`), `class` (paths or short names), `[x, y, z]` vectors.
- One result/error contract: objects only; `{"error": {code, message, hint, retryable, outcome, details}}` with a closed
  code set and `outcome: unknown` for mutating calls that may have run.
- Per-op safety tiers drive MCP annotations and the approval gate; `gate_policy: "require"` fails closed (gated ops
  are refused) until the cockpit approval UI is wired to tool calls. `toolsets op=describe` reports each op's tier and
  needs. `-toolsets` / `UMCP_TOOLSETS` and `.umcp.json` toolsets apply at stdio startup.
- 16 advertised-but-unimplemented HUD tools removed.

### New capabilities
- `actor_edit` transform in PIE and set_properties in the editor; editor edits are undoable.
- Snapshots keyed by object path, World Partition aware, with `snapshot_restore` as one undo step.
- `git op=checkpoint` tags `umcp/cp/<n>`; `git_revert` restores files to a checkpoint, closing and relaunching the
  editor safely when the assets are loaded, all-or-nothing.
- Safe editor shutdown (dirty-set check, PIE stop, re-check, graceful quit, reported kill fallback) for restarts,
  full builds and reverts; `-AutoDeclinePackageRecovery` on every launch.
- One playtest orchestration (`playtest op=run`, async) with collected beat errors and crash diagnosis; job results
  can carry images.
- Async jobs with `wait_s` and streamed progress, owned by the project.

### Fixed hazards (v1 behaviour)
- `apply_level_recipe` wiped the level by default (`clean_slate` now defaults false).
- `*_create` could hit Unreal's interactive overwrite prompt; `asset_create` returns CONFLICT, `op=replace` validates
  every input before deleting.
- `scene_apply` overwrote any actor whose label matched; scenes now match only their own tagged actors, and pruning is
  a separate destructive op.
- Actor lookups silently used the first label match; ambiguity is now CONFLICT with the candidates.
- `world` values were silently coerced; unknown worlds are errors. `company_*` fell back to the editor level when PIE
  was off; PolyWorld ops are PIE-only.
- Output paths (screenshots, captures, renders, audio) were caller-controlled; they are server-owned.
- The cockpit's access token was returned to the agent; it never is.
- A non-idempotent op could run twice after a reconnect; dispatch is exactly-once with `outcome: unknown`.

### Live validation (P7) — defects only a real editor exposed, fixed
See [`docs/validation/T4-2026-10-03.md`](docs/validation/T4-2026-10-03.md).
- After any timed-out command the server could never reconnect until restarted (the editor ignored `open_connection`
  while it still believed the old channel was up); the client now sends `close_connection` first.
- A rebuilt server kept running the previous companion in a live editor when the version number was unchanged; the
  install sentinel now includes a source digest.
- `pie op=start` froze the editor (and every later call) behind Unreal's modal "Blueprint Compilation Errors" dialog:
  a plugin pre-flight refuses with the Blueprint list (or `ignore_blueprint_errors=true` plays anyway), and a modal
  guard cancels that dialog; on timeout it lists the editor's other windows without touching them.
- `build` ran UBT beside an editor that was running but not answering (locked DLLs); it now refuses (Windows).
- `git op=checkpoint` failed when `.gitignore` already ignored `Saved/`/`Intermediate/`/`DerivedDataCache/`.
- A class-filtered snapshot diff reported a World Partition actor in an unloaded cell as removed.
- Daemon: a cold start longer than the attach call was cancelled and its editor killed (now detached, attach is
  retryable); attaching a project already open in another editor launched a second one and bound the wrong editor
  (now refused on Windows, and spawns bind only the editor they launched); during a lease restart the session lost its
  project and jobs.
- A stdio server with `-project` could bind another project's editor while its own relaunched (after a build); node
  selection is now strict whenever a project is set.
- Daemon gate fixes: a session that ended mid cold start no longer keeps the editor leased forever; abandoned starts
  are adopted by the next session; cold starts of one project are serialized; restarts refuse overlapping
  build/revert/lifecycle/headless work.
- Also: PIE transforms of Static actors, asset search (`ARFilter`), registry tags by package path (and during PIE),
  HighResShots written to absolute paths, stale PIE screenshots, the `game_scene` capture enum, thumbnail lighting,
  widget render class resolution, PIE console commands, null op arguments in playtest beats, garbage collection after
  the PIE pre-flight compiles.

### Architecture & reliability
- 48 → 29 top-level packages (plan target 28) with an enforced import DAG; one supervisor for stdio and daemon; per-session servers in the
  daemon with lease adoption and draining.
- The companion runs in its own namespace (`__main__._mcp2`) alongside a v1 companion; rollback is a binary swap.
- Tests: in-memory e2e over both dispatch backends, every (tool, op) exercised against an op emulator, the real binary
  over stdio and HTTP, companion contract tests in CPython, ≥ 14 fault scenarios, merged coverage gates in CI.
- Generated docs (`docs/tools.md`, `docs/migration-v2.md`) checked for drift in CI.

## v1 — tag `v1-final`

The Go rewrite of the original Python server: 155 tools over Unreal's Python remote execution, the multi-project
daemon, the MCP Cockpit plugin, playtest capture and design audits.
