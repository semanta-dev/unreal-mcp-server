# --- tighter editor integration: viewport, selection, state -----------------

def _op_viewport_set(args):
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
    cam = args.get("camera") or {}
    if cam.get("location") or cam.get("rotation_pyr"):
        loc0, rot0 = ues.get_level_viewport_camera_info() or (unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0))
        loc = cam.get("location")
        rot = cam.get("rotation_pyr")
        v = unreal.Vector(loc[0], loc[1], loc[2]) if loc else loc0
        r = _pyr_to_rotator(rot) if rot else rot0
        ues.set_level_viewport_camera_info(v, r)
    if args.get("pilot_actor"):
        a = _find_actor(args["pilot_actor"])
        if a:
            les.pilot_level_actor(a)
    if args.get("eject"):
        les.eject_pilot_level_actor()
    if "game_view" in args:
        les.editor_set_game_view(bool(args["game_view"]))
    for cmd in args.get("console") or []:
        unreal.SystemLibrary.execute_console_command(None, cmd)
    loc, rot = ues.get_level_viewport_camera_info() or (unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0))
    return {"camera": {"location": [loc.x, loc.y, loc.z], "rotation_pyr": [rot.pitch, rot.yaw, rot.roll]}}


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
        "camera": {"location": [loc.x, loc.y, loc.z], "rotation_pyr": [rot.pitch, rot.yaw, rot.roll]},
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
    targets = args.get("targets", "selection")
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    if targets == "selection":
        actors = sub.get_selected_level_actors()
    else:
        want = set(targets if isinstance(targets, list) else [targets])
        actors = [a for a in sub.get_all_level_actors() if a and a.get_actor_label() in want]
    if not actors:
        return {"error": "no actors to focus"}
    loc, rot = _frame_pose_for(actors, float(args.get("pitch", -30)), float(args.get("distance_scale", 2.0)))
    unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).set_level_viewport_camera_info(loc, rot)
    return {"framed": len(actors),
            "camera": {"location": [loc.x, loc.y, loc.z], "rotation_pyr": [rot.pitch, rot.yaw, rot.roll]}}


def _op_select_actors(args):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    labels = set(args.get("labels") or [])
    mode = args.get("mode", "replace")
    by_label = {a.get_actor_label(): a for a in sub.get_all_level_actors() if a}
    picked = [by_label[lbl] for lbl in labels if lbl in by_label]
    if mode == "none":
        sub.set_selected_level_actors([])
    elif mode == "add":
        cur = list(sub.get_selected_level_actors())
        sub.set_selected_level_actors(cur + [a for a in picked if a not in cur])
    elif mode == "remove":
        cur = [a for a in sub.get_selected_level_actors() if a not in picked]
        sub.set_selected_level_actors(cur)
    else:
        sub.set_selected_level_actors(picked)
    if args.get("frame") and picked:
        _op_focus_actors({"targets": [a.get_actor_label() for a in picked]})
    sel = sub.get_selected_level_actors()
    return {"count": len(sel),
            "selected": [{"label": a.get_actor_label(), "class": a.get_class().get_name()} for a in sel]}


def _op_get_selection(args):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    sel = sub.get_selected_level_actors()
    return {"count": len(sel),
            "selected": [{"label": a.get_actor_label(), "class": a.get_class().get_name()} for a in sel]}


