# ===========================================================================
# v2 editor lifecycle (docs/plans/OVERHAUL_PLAN.md §2.5): what the safe-shutdown
# routine and the editor-aware git_revert need to know before closing the editor —
# the FULL dirty set (content + maps, including never-saved new assets), which
# packages are loaded, PIE state — and a graceful quit.
# ===========================================================================


def _op_packages_state(args):
    """{dirty: [...], loaded: {package: bool}, pie, map, editor_pid}. `packages` are
    long package names (/Game/Maps/L_Arena) whose loaded state to report."""
    eals = unreal.EditorLoadingAndSavingUtils
    dirty = set()
    for getter in ("get_dirty_content_packages", "get_dirty_map_packages"):
        try:
            dirty |= {p.get_name() for p in getattr(eals, getter)()}
        except Exception:
            pass
    loaded = {}
    for name in args.get("packages") or []:
        try:
            loaded[name] = unreal.find_object(None, name) is not None
        except Exception:
            loaded[name] = False
    current = None
    try:
        world = _editor_world()
        current = world.get_outermost().get_name() if world else None
    except Exception:
        pass
    return {"dirty": sorted(dirty), "loaded": loaded, "pie": _pie_running(), "map": current,
            "editor_pid": os.getpid()}


def _op_quit_editor(args):
    """Ask the editor to exit gracefully. Only valid with nothing dirty (a dirty
    editor would raise the save-changes modal): the caller checked packages_state
    immediately before. The command channel drops as the editor exits."""
    dirty = _op_packages_state({})["dirty"]
    if dirty:
        raise _V2Error("PRECONDITION", "%d unsaved package(s); refusing a graceful quit" % len(dirty), dirty=dirty)
    unreal.SystemLibrary.quit_editor()
    return {"quitting": True}
