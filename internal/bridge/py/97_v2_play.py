# ===========================================================================
# v2 play / snapshot / scene / capture helpers (docs/plans/OVERHAUL_PLAN.md §2.3
# rows 16-27, §2.5, §2.7 items 5, 10, 11): snapshots keyed by object path with
# World Partition awareness, path-matched restore inside one undo transaction,
# tag-scoped scene matching, and server-owned output paths.
# ===========================================================================

_SAFE_NAME = re.compile(r"^[A-Za-z0-9_\-]{1,64}$")


def _safe_name(value, what):
    """A name that becomes part of a file path: no separators, no '..'."""
    if not isinstance(value, str) or not _SAFE_NAME.match(value):
        raise _V2Error("BAD_VALUE", "%s must be 1-64 letters, digits, '_' or '-' (got %r)" % (what, value))
    return value


def _full_path(p):
    """FPaths results are often relative to Engine/Binaries; the Go server reads the
    files the editor writes, so every path handed back must be absolute."""
    return unreal.Paths.convert_relative_path_to_full(p)


def _saved_mcp_dir(*parts):
    d = os.path.join(_full_path(unreal.Paths.project_saved_dir()), "MCP", *parts)
    os.makedirs(d, exist_ok=True)
    return d


def _dirty_map_names():
    try:
        return {p.get_name() for p in unreal.EditorLoadingAndSavingUtils.get_dirty_map_packages()}
    except Exception:
        return set()


def _dirtied_since(before):
    """Map packages that became dirty during an op (transient capture actors mark
    the level dirty; git_revert's dirty check must know it was us)."""
    return sorted(_dirty_map_names() - before)


# --- PIE --------------------------------------------------------------------------

def _pie_running():
    return _game_world() is not None


def _op_pie_start(args):
    if _pie_running():
        return {"pie": True, "already_running": True}
    _op_start_play(args)
    return {"pie": "starting", "simulate": bool(args.get("simulate"))}


def _op_pie_stop(args):
    if not _pie_running():
        return {"pie": False, "already_stopped": True}
    _op_stop_play(args)
    return {"pie": "stopping"}


def _op_pie_observe_v2(args):
    if not _pie_running():
        raise _V2Error("NOT_IN_PIE", "PIE is not running (start it with pie op=start)")
    a = {k: args[k] for k in ("include", "exclude", "properties", "max_props") if k in args}
    want = list(args.get("actors") or [])
    if want:
        a["actors_of_interest"] = want
    out = _op_pie_observe(a)
    if want:
        found = {x["label"] for x in out.get("actors") or []}
        out["missing"] = [w for w in want if w not in found]
    if args.get("pawn"):
        ps = _op_pawn_state({"world": "pie", "player": int(args.get("player", 0))})
        out["pawn"] = None if "error" in ps else {"location": ps["loc"], "velocity": ps["vel"], "speed": ps["speed"]}
    return out


# --- snapshots (§2.5) -----------------------------------------------------------

def _wp_actor_paths(world):
    """Every actor path World Partition knows for the editor world, loaded or not;
    None for a non-WP level (or when the API is unavailable)."""
    lib = getattr(unreal, "WorldPartitionBlueprintLibrary", None)
    if lib is None:
        return None
    try:
        descs = lib.get_actor_descs()
    except Exception:
        return None
    if not descs:
        return None
    paths = set()
    for d in descs:
        try:
            paths.add(_norm_path(str(d.get_editor_property("actor_path"))))
        except Exception:
            pass
    return paths or None


def _op_snapshot_actors(args):
    """Editor-world actors with path, label, class, tags and full transform; under
    World Partition also the actors that exist but are not loaded."""
    world, name = _v2_world({"world": "editor"}, "editor")
    flt = (args.get("class_filter") or "").lower()
    out = []
    for a in _world_actors(world, name):
        cn = a.get_class().get_name()
        label = a.get_actor_label()
        if flt and flt not in cn.lower() and flt not in label.lower():
            continue
        loc, rot, sc = a.get_actor_location(), a.get_actor_rotation(), a.get_actor_scale3d()
        out.append({"path": _norm_path(a.get_path_name()), "label": label, "class": cn,
                    "tags": [str(t) for t in a.tags],
                    "loc": [loc.x, loc.y, loc.z], "rot": [rot.pitch, rot.yaw, rot.roll], "scale": [sc.x, sc.y, sc.z]})
    res = {"world": name, "count": len(out), "actors": out, "world_partition": False,
           "class_filter": args.get("class_filter") or ""}
    known = _wp_actor_paths(world)
    if known is not None:
        res["world_partition"] = True
        if not flt:  # a filtered snapshot cannot tell an unloaded actor from a filtered-out one
            loaded = {_norm_path(a.get_path_name()) for a in _world_actors(world, name)}
            res["unloaded"] = sorted(known - loaded)
    return res


def _op_snapshot_restore(args):
    """Restore the transforms of actors that still exist, matched by object path,
    as one undo step. Never recreates or deletes actors: reports both."""
    world, name = _v2_world({"world": "editor"}, "editor")
    by_path = {_norm_path(a.get_path_name()): a for a in _world_actors(world, name)}
    snap = args.get("actors") or []
    snap_paths = {t.get("path") for t in snap}
    known = _wp_actor_paths(world)  # World Partition: actors that exist, loaded or not
    unloaded = (known - set(by_path)) if known is not None else set()
    restored, removed, unknown = 0, [], []

    def depth(t):
        a, d = by_path.get(t.get("path")), 0
        while a is not None and d < 64:
            try:
                a = a.get_attach_parent_actor()
            except Exception:
                a = None
            d += a is not None
        return d

    # World transforms: a parent must be in place before its attached children.
    with _transaction("MCP: snapshot restore " + str(args.get("name", ""))):
        for t in sorted(snap, key=depth):
            a = by_path.get(t.get("path"))
            if a is None:
                (unknown if t.get("path") in unloaded else removed).append(t.get("path"))
                continue
            a.modify()
            loc, rot, scale = t.get("loc"), t.get("rot"), t.get("scale")
            if loc:
                a.set_actor_location(unreal.Vector(loc[0], loc[1], loc[2]), False, False)
            if rot:
                a.set_actor_rotation(_pyr_to_rotator(rot), False)
            if scale:
                a.set_actor_scale3d(unreal.Vector(scale[0], scale[1], scale[2]))
            restored += 1
    added = sorted(p for p in by_path if p not in snap_paths)
    saved = False
    if args.get("save", True):
        saved = bool(unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, False))
    out = {"restored": restored, "not_restored": {"added": added, "removed": removed}, "saved": saved}
    if unknown:
        out["not_restored"]["unknown"] = unknown  # in World Partition cells that are not loaded
    return out


# --- scenes (tag-scoped; §2.7 item 11) -------------------------------------------

def _scene_tag(scene_id):
    return "mcp_scene:" + scene_id


def _scene_actors(scene_id):
    """This scene's actors by label — only actors carrying its tag, so a hand-placed
    actor whose label collides with a spec label is never adopted or overwritten."""
    tag = _scene_tag(scene_id)
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    out = {}
    for a in sub.get_all_level_actors():
        if not a:
            continue
        try:
            atags = [str(t) for t in a.get_editor_property("tags")]
        except Exception:
            atags = []
        if tag in atags:
            key = next((t[len("mcp_label:"):] for t in atags if t.startswith("mcp_label:")), a.get_actor_label())
            out[key] = a
    return out


def _scene_id(args):
    sid = args.get("scene_id")
    if not isinstance(sid, str) or not sid:
        raise _V2Error("BAD_VALUE", "scene_id is required")
    return sid


def _op_scene_actors(args):
    scene_id = _scene_id(args)
    out = []
    for label, a in sorted(_scene_actors(scene_id).items()):
        loc = a.get_actor_location()
        out.append({"label": label, "class": a.get_class().get_name(), "location": [loc.x, loc.y, loc.z]})
    return {"scene_id": scene_id, "actors": out}


def _op_scene_prune(args):
    """Destroy this scene's tagged actors whose label is not in keep (the v1
    scene_apply prune:true, now its own destructive op and transaction)."""
    scene_id = _scene_id(args)
    keep = set(args.get("keep") or [])
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    pruned = []
    with _transaction("MCP: prune " + scene_id):
        for label, a in _scene_actors(scene_id).items():
            if label not in keep:
                sub.destroy_actor(a)
                pruned.append(label)
    saved = False
    if args.get("save", True):
        saved = bool(unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True))
    return {"scene_id": scene_id, "pruned": sorted(pruned), "saved": saved}


# --- server-owned output paths ------------------------------------------------------

def _op_take_screenshot_v2(args):
    fname = args.get("filename") or ""
    if os.path.basename(fname) != fname or not fname.endswith(".png") or ".." in fname:
        raise _V2Error("BAD_VALUE", "filename must be a bare .png name")
    before = _dirty_map_names()
    out = _op_take_screenshot(args)
    out["dirtied"] = _dirtied_since(before)
    return out


def _op_pie_screenshot_v2(args):
    fname = args.get("filename") or ""
    if os.path.basename(fname) != fname or not fname.endswith(".png") or ".." in fname:
        raise _V2Error("BAD_VALUE", "filename must be a bare .png name")
    if not _pie_running():
        raise _V2Error("NOT_IN_PIE", "PIE is not running")
    return _op_pie_screenshot(args)


def _op_capture_start_v2(args):
    if args.get("session"):
        _safe_name(args["session"], "session")
    before = _dirty_map_names()
    out = _op_capture_start(args)
    if isinstance(out, dict) and "error" not in out:
        out["dirtied"] = _dirtied_since(before)
    return out


def _op_capture_poses_v2(args):
    if args.get("session"):
        _safe_name(args["session"], "session")
    before = _dirty_map_names()
    out = _op_capture_poses(args)
    if isinstance(out, dict) and "error" not in out:
        out["dirtied"] = _dirtied_since(before)
    return out


def _audio_world():
    world = _game_world()
    if not world:
        raise _V2Error("NOT_IN_PIE", "audio renders only in PIE (start it with pie op=start)")
    return world


def _op_audio_capture_start_v2(args):
    _audio_world()
    if args.get("session"):
        _safe_name(args["session"], "session")
    return _op_audio_capture_start({"world": "pie", "session": args.get("session", "")})


def _op_audio_capture_stop_v2(args):
    _audio_world()
    return _op_audio_capture_stop({"world": "pie", "out_dir": _saved_mcp_dir("audio")})


def _op_play_test_sound_v2(args):
    _audio_world()
    return _op_play_test_sound(dict(args, world="pie"))


# --- PolyWorld (Company-MVP): PIE only -------------------------------------------

def _pie_only(fn):
    """The company_* ops act on the running game. Their v1 world=auto fell back to the
    EDITOR level when PIE was off (building into the saved map): v2 refuses instead."""
    def op(args):
        if not _pie_running():
            raise _V2Error("NOT_IN_PIE", "PolyWorld runs only in PIE (start it with pie op=start)")
        return fn(dict(args, world="pie"))
    op.__name__ = fn.__name__
    return op
