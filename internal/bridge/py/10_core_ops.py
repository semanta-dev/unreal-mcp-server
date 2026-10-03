# --- structured ops --------------------------------------------------------

def _op_editor_status(args):
    world = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()
    return {
        "reachable": True,
        "engine_version": unreal.SystemLibrary.get_engine_version(),
        "project_dir": unreal.SystemLibrary.get_project_directory(),
        "current_level": world.get_name() if world else None,
        "is_in_pie": unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).is_in_play_in_editor(),
        "bridge_version": _MCP2_BRIDGE_VERSION,
        "editor_pid": os.getpid(),
    }


def _op_list_actors(args):
    flt = (args.get("name_filter") or "").lower()
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    out = []
    for a in sub.get_all_level_actors():
        if not a:
            continue
        label = a.get_actor_label()
        cls = a.get_class().get_name()
        if flt and flt not in label.lower() and flt not in cls.lower():
            continue
        out.append(_actor_ref(a))
    return out  # bare array, matching the Python server's list_actors contract


def _op_get_actor(args):
    a = _find_actor(args["actor_label"])
    if not a:
        return {"error": "No actor labeled " + str(args["actor_label"])}
    loc, rot, scale = a.get_actor_location(), a.get_actor_rotation(), a.get_actor_scale3d()
    return {
        "label": a.get_actor_label(),
        "class": a.get_class().get_name(),
        "path": a.get_path_name(),
        "location": [loc.x, loc.y, loc.z],
        "rotation_pyr": [rot.pitch, rot.yaw, rot.roll],
        "scale": [scale.x, scale.y, scale.z],
        "components": [
            {"name": c.get_name(), "class": c.get_class().get_name()}
            for c in a.get_components_by_class(unreal.ActorComponent)
        ],
    }


def _op_spawn_actor(args):
    cls_path = args["class_path"]
    if cls_path.startswith("/Script/"):
        cls = unreal.load_class(None, cls_path)
    else:
        bp = unreal.load_asset(cls_path)
        if isinstance(bp, unreal.Blueprint):
            cls = bp.generated_class()
        elif isinstance(bp, unreal.Class):
            cls = bp
        else:
            cls = None
    if not cls:
        return {"error": "Could not resolve class: " + str(cls_path)}
    x = args.get("x", 0.0)
    y = args.get("y", 0.0)
    z = args.get("z", 100.0)
    pitch = args.get("pitch", 0.0)
    yaw = args.get("yaw", 0.0)
    roll = args.get("roll", 0.0)
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    actor = sub.spawn_actor_from_class(cls, unreal.Vector(x, y, z), unreal.Rotator(roll, pitch, yaw))
    if not actor:
        return {"error": "Spawn failed"}
    mesh_path = args.get("static_mesh_path") or ""
    if mesh_path:
        mesh = unreal.load_asset(mesh_path)
        comp = actor.get_component_by_class(unreal.StaticMeshComponent)
        if mesh and comp:
            comp.set_static_mesh(mesh)
    label = args.get("label") or ""
    if label:
        actor.set_actor_label(label)
    # Unrounded location, matching the Python server's spawn_actor (list_actors
    # rounds via _actor_ref; spawn/get_actor return full precision).
    loc = actor.get_actor_location()
    return {
        "label": actor.get_actor_label(),
        "class": actor.get_class().get_name(),
        "location": [loc.x, loc.y, loc.z],
    }


def _op_list_assets(args):
    path = args.get("path", "/Game")
    recursive = args.get("recursive", True)
    limit = int(args.get("limit", 200))
    assets = unreal.EditorAssetLibrary.list_assets(path, recursive=recursive, include_folder=False)
    return {"total": len(assets), "assets": sorted(assets)[:limit]}


def _op_import_assets(args):
    dest = args.get("destination_path", "/Game/Imported")
    tasks = []
    for path in args["file_paths"]:
        task = unreal.AssetImportTask()
        task.set_editor_property("filename", path)
        task.set_editor_property("destination_path", dest)
        task.set_editor_property("automated", True)
        task.set_editor_property("save", True)
        task.set_editor_property("replace_existing", True)
        tasks.append(task)
    unreal.AssetToolsHelpers.get_asset_tools().import_asset_tasks(tasks)
    imported, failed = [], []
    for task in tasks:
        paths = list(task.get_editor_property("imported_object_paths") or [])
        if paths:
            imported.extend(paths)
        else:
            failed.append(str(task.get_editor_property("filename")))
    return {"imported": imported, "failed": failed}


# --- text-style ops (carry a "message" matching the Python server) ---------

def _op_open_level(args):
    unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
    ok = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).load_level(args["level_path"])
    msg = ("Loaded " if ok else "FAILED to load ") + str(args["level_path"])
    return {"loaded": bool(ok), "level_path": args["level_path"], "message": msg}


def _op_save_all(args):
    ok = unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
    return {"saved": bool(ok), "message": "Saved all dirty packages" if ok else "Save reported failures"}


def _op_delete_actor(args):
    a = _find_actor(args["actor_label"])
    if not a:
        return {"deleted": False, "message": "No actor labeled " + str(args["actor_label"])}
    unreal.get_editor_subsystem(unreal.EditorActorSubsystem).destroy_actor(a)
    return {"deleted": True, "label": args["actor_label"], "message": "Deleted " + str(args["actor_label"])}


def _op_set_actor_transform(args):
    a = _find_actor(args["actor_label"])
    if not a:
        return {"updated": False, "message": "No actor labeled " + str(args["actor_label"])}
    loc = args.get("location")
    rot = args.get("rotation_pyr")
    scale = args.get("scale")
    if loc:
        a.set_actor_location(unreal.Vector(loc[0], loc[1], loc[2]), False, False)
    if rot:
        # rot is [pitch, yaw, roll] -> Rotator(roll, pitch, yaw)
        a.set_actor_rotation(unreal.Rotator(rot[2], rot[0], rot[1]), False)
    if scale:
        a.set_actor_scale3d(unreal.Vector(scale[0], scale[1], scale[2]))
    return {"updated": True, "label": args["actor_label"], "message": "Updated transform of " + str(args["actor_label"])}


def _op_start_play(args):
    les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
    if args.get("simulate"):
        les.editor_play_simulate()
    else:
        les.editor_request_begin_play()
    return {"message": "Play session starting"}


def _op_stop_play(args):
    unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).editor_request_end_play()
    return {"message": "Play session ending"}


def _op_live_coding_compile(args):
    unreal.SystemLibrary.execute_console_command(None, "LiveCoding.Compile")
    return {"message": "Live Coding compile triggered (see editor log/Live Coding console for status)"}


# --- image op (returns a file path; the Go server reads the PNG bytes) ------

def _op_take_screenshot(args):
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    world = ues.get_editor_world()
    width = int(args.get("width", 1280))
    height = int(args.get("height", 720))
    explicit_loc = args.get("camera_location")
    explicit_rot = args.get("camera_rotation_pyr")
    if explicit_loc:
        loc = unreal.Vector(explicit_loc[0], explicit_loc[1], explicit_loc[2])
        if explicit_rot:
            rot = unreal.Rotator(explicit_rot[2], explicit_rot[0], explicit_rot[1])
        else:
            rot = unreal.MathLibrary.find_look_at_rotation(loc, unreal.Vector(0, 0, 0))
    else:
        cam = ues.get_level_viewport_camera_info()
        if cam:
            loc, rot = cam
        else:
            loc, rot = unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0)
        if loc.length() < 1.0:
            # Viewport camera unset (editor started in background): frame level bounds.
            radius = 1000.0
            for a in unreal.get_editor_subsystem(unreal.EditorActorSubsystem).get_all_level_actors():
                if isinstance(a, unreal.StaticMeshActor):
                    o, e = a.get_actor_bounds(False)
                    radius = max(radius, o.length() * 0.7)
            loc = unreal.Vector(-radius, -radius, radius * 0.9)
            rot = unreal.MathLibrary.find_look_at_rotation(loc, unreal.Vector(0, 0, 0))
    rt = unreal.RenderingLibrary.create_render_target2d(
        world, width, height, unreal.TextureRenderTargetFormat.RTF_RGBA8, unreal.LinearColor(0, 0, 0, 1), False)
    actors = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    cap = actors.spawn_actor_from_class(unreal.SceneCapture2D, loc, rot)
    cc = cap.capture_component2d
    cc.set_editor_property("texture_target", rt)
    cc.set_editor_property("capture_source", unreal.SceneCaptureSource.SCS_FINAL_COLOR_LDR)
    cc.set_editor_property("always_persist_rendering_state", True)
    cc.capture_scene()
    out_dir = unreal.SystemLibrary.get_project_directory() + "Saved/Screenshots/"
    fname = args.get("filename") or "mcp_screenshot.png"
    unreal.RenderingLibrary.export_render_target(world, rt, out_dir, fname)
    actors.destroy_actor(cap)
    return {"file": out_dir + fname}


