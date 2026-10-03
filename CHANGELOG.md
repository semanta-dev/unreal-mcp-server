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
