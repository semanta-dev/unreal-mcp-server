# --- tighter editor integration: viewport, selection, state -----------------

def _cam_out(ues):
    loc, rot = ues.get_level_viewport_camera_info() or (unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0))
    return {"location": [loc.x, loc.y, loc.z], "rotation": [rot.pitch, rot.yaw, rot.roll]}


def _editor_refs(refs):
    """Resolve actor references (labels or object paths) in the editor level."""
    world, name = _v2_world({"world": "editor"}, "editor")
    return [_resolve_actor(world, name, r) for r in refs]


def _op_viewport_set(args):
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
    loc, rot = args.get("location"), args.get("rotation")
    if loc or rot:
        loc0, rot0 = ues.get_level_viewport_camera_info() or (unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0))
        v = unreal.Vector(loc[0], loc[1], loc[2]) if loc else loc0
        r = _pyr_to_rotator(rot) if rot else rot0
        ues.set_level_viewport_camera_info(v, r)
    if args.get("pilot"):
        les.pilot_level_actor(_editor_refs([args["pilot"]])[0])
    if args.get("eject"):
        les.eject_pilot_level_actor()
    if "game_view" in args:
        les.editor_set_game_view(bool(args["game_view"]))
    return {"camera": _cam_out(ues)}


def _op_viewport_get(args):
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    info = ues.get_level_viewport_camera_info()
    if not info:
        return {"camera": None, "note": "viewport camera unavailable"}
    loc, rot = info
    game_view = None
    try:
        game_view = bool(unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).editor_get_game_view())
    except Exception:
        pass
    return {
        "camera": {"location": [loc.x, loc.y, loc.z], "rotation": [rot.pitch, rot.yaw, rot.roll]},
        "game_view": game_view,
        "note": "view_mode/piloting not reliably exposed in UE Python",
    }


def _frame_pose_for(actors, pitch_deg, distance_scale):
    aabb = _actor_bounds_aabb(actors)
    origin, extent = aabb["origin"], aabb["extent"]
    radius = max(math.sqrt(extent[0] ** 2 + extent[1] ** 2 + extent[2] ** 2), 100.0)
    dist = radius * max(distance_scale, 1.0)
    pitch = pitch_deg
    horiz = dist * math.cos(math.radians(-pitch)) if pitch < 0 else dist
    height = dist * math.sin(math.radians(-pitch)) if pitch < 0 else 0.0
    loc = unreal.Vector(origin[0] - horiz, origin[1], origin[2] + height)
    rot = unreal.MathLibrary.find_look_at_rotation(loc, unreal.Vector(origin[0], origin[1], origin[2]))
    return loc, rot


def _op_focus_actors(args):
    refs = args.get("actors") or []
    if refs:
        actors = _editor_refs(refs)
    else:
        actors = list(unreal.get_editor_subsystem(unreal.EditorActorSubsystem).get_selected_level_actors())
    if not actors:
        raise _V2Error("NOT_FOUND", "nothing to focus: pass actors or select some first")
    loc, rot = _frame_pose_for(actors, float(args.get("pitch", -30)), float(args.get("distance_scale", 2.0)))
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    ues.set_level_viewport_camera_info(loc, rot)
    return {"framed": len(actors), "camera": _cam_out(ues)}


def _selection_out(sub):
    sel = sub.get_selected_level_actors()
    return {"count": len(sel),
            "selected": [{"label": a.get_actor_label(), "path": a.get_path_name(), "class": a.get_class().get_name()}
                         for a in sel]}


def _op_select_actors(args):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    mode = args.get("mode", "replace")
    if mode not in ("replace", "add", "remove", "none"):
        raise _V2Error("BAD_VALUE", "mode must be replace, add, remove or none (got %r)" % (mode,))
    picked = _editor_refs(args.get("actors") or []) if mode != "none" else []
    cur = list(sub.get_selected_level_actors())
    if mode == "none":
        sub.set_selected_level_actors([])
    elif mode == "add":
        sub.set_selected_level_actors(cur + [a for a in picked if a not in cur])
    elif mode == "remove":
        sub.set_selected_level_actors([a for a in cur if a not in picked])
    else:
        sub.set_selected_level_actors(picked)
    if args.get("frame") and picked:
        _op_focus_actors({"actors": [a.get_path_name() for a in picked]})
    return _selection_out(sub)


def _op_get_selection(args):
    return _selection_out(unreal.get_editor_subsystem(unreal.EditorActorSubsystem))
