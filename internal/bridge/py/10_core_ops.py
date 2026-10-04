# --- structured ops --------------------------------------------------------

def _op_editor_status(args):
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    world = ues.get_editor_world()
    cam = None
    info = ues.get_level_viewport_camera_info()
    if info:
        loc, rot = info
        cam = {"location": [loc.x, loc.y, loc.z], "rotation": [rot.pitch, rot.yaw, rot.roll]}
    sel = sub.get_selected_level_actors()
    return {
        "reachable": True,
        "engine_version": unreal.SystemLibrary.get_engine_version(),
        "project_dir": unreal.SystemLibrary.get_project_directory(),
        "current_level": world.get_name() if world else None,
        "is_in_pie": unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).is_in_play_in_editor(),
        "viewport_camera": cam,
        "selection": {"count": len(sel), "labels": [a.get_actor_label() for a in sel]},
        "actor_count": len(sub.get_all_level_actors()),
        "recorders": [s for s, r in _MCP_RECORDERS.items() if r["running"]],
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

def _dirty_packages():
    u = unreal.EditorLoadingAndSavingUtils
    return (sorted({p.get_name() for p in u.get_dirty_map_packages()}),
            sorted({p.get_name() for p in u.get_dirty_content_packages()}))


def _op_open_level(args):
    # LevelEditorSubsystem.load_level runs as an unattended script: it never asks — it
    # drops the unsaved changes of the map it leaves (and would end PIE).
    if args.get("save") is False:
        maps, content = _dirty_packages()
        if maps:
            raise _V2Error("PRECONDITION", "the open level has unsaved changes and save=false never saves: loading would "
                           "drop them — save (level op=save_all) or drop them on purpose (level op=revert)", unsaved=maps)
    else:
        unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
    ok = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).load_level(args["level_path"])
    msg = ("Loaded " if ok else "FAILED to load ") + str(args["level_path"])
    out = {"loaded": bool(ok), "level_path": args["level_path"], "message": msg}
    if args.get("save") is False:
        out["content_unsaved"] = _dirty_packages()[1]  # assets stay changed in memory, unsaved
    return out


def _op_level_revert(args):
    """Reload the open level from disk, dropping its unsaved changes (R6 rollback). Unsaved
    assets are not touched (they stay changed in memory) and are listed."""
    if _pie_running():
        raise _V2Error("PRECONDITION", "stop PIE first (reverting the level would end it)")
    world = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()
    open_pkg = (world.get_path_name() if world else "").split(".", 1)[0]
    pkg = (args.get("level_path") or open_pkg).split(".", 1)[0]
    if pkg != open_pkg:
        # Loading another level here would be "open without saving", not a revert.
        raise _V2Error("BAD_VALUE", "revert reloads the open level (%s), not %s: open another level with level op=open"
                       % (open_pkg or "none", pkg))
    if not pkg.startswith("/Game/") or not unreal.EditorAssetLibrary.does_asset_exist(pkg):
        raise _V2Error("PRECONDITION", "%s was never saved: there is nothing on disk to revert to" % (pkg or "the level"))
    maps, content = _dirty_packages()
    ok = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).load_level(pkg)
    if not ok:
        raise _V2Error("EDITOR_ERROR", "could not reload %s" % pkg)
    return {"reverted": pkg, "discarded": maps, "content_unsaved": content}


def _op_save_all(args):
    ok = unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
    return {"saved": bool(ok), "message": "Saved all dirty packages" if ok else "Save reported failures"}


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


