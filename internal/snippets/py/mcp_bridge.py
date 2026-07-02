# mcp_bridge.py — companion module hot-loaded into the Unreal Editor's __main__
# namespace by the Go MCP server. The server invokes _mcp_dispatch(op, b64args)
# with base64-encoded JSON args; each op returns a JSON-serializable value that
# is emitted back via the __MCP_JSON__ marker. Keeping all editor-side logic here
# (not hand-built in Go) is the DRY + injection-safe design (GO_REWRITE_PLAN.md §8).
#
# Editor-scripting gotchas preserved verbatim from the Python server:
#   - unreal.Rotator(a, b, c) is (roll, pitch, yaw), NOT (pitch, yaw, roll).
#   - rotation_pyr args are [pitch, yaw, roll] and map to Rotator(roll, pitch, yaw).
#   - take_screenshot uses SceneCapture2D (renders even when backgrounded; editor
#     world only, NOT during PIE).
#
# Text-style ops return a "message" field carrying the exact string the Python
# server produced, so the A/B parity harness can assert text equality.

_MCP_BRIDGE_VERSION = 6

import unreal
import json
import base64
import traceback
import os
import io
import contextlib

_MARKER = "__MCP_JSON__"


def _jsonable(v):
    """Coerce a value to something JSON-serializable. Unreal enums (UENUM) are
    returned as their enumerator NAME (a string) so string predicates can match;
    other non-primitives fall back to str(). Also usable as json.dumps(default=)."""
    if v is None or isinstance(v, (bool, int, float, str)):
        return v
    name = getattr(v, "name", None)
    if name is not None:
        return name
    return str(v)


# --- prelude helpers -------------------------------------------------------

def _find_actor(label):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    for a in sub.get_all_level_actors():
        if a and a.get_actor_label() == label:
            return a
    return None


def _actor_ref(a):
    loc = a.get_actor_location()
    return {
        "label": a.get_actor_label(),
        "class": a.get_class().get_name(),
        "location": [round(loc.x, 1), round(loc.y, 1), round(loc.z, 1)],
    }


def _emit(payload):
    # default=_jsonable guarantees an op's result can never poison the marker
    # (a single non-serializable field would otherwise fail the whole dispatch).
    print(_MARKER + json.dumps(payload, default=_jsonable))


# --- structured ops --------------------------------------------------------

def _op_editor_status(args):
    world = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()
    return {
        "reachable": True,
        "engine_version": unreal.SystemLibrary.get_engine_version(),
        "project_dir": unreal.SystemLibrary.get_project_directory(),
        "current_level": world.get_name() if world else None,
        "is_in_pie": unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).is_in_play_in_editor(),
        "bridge_version": _MCP_BRIDGE_VERSION,
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


# --- PIE observation / driving (only valid during play-in-editor) ----------

def _game_world():
    return unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_game_world()


_GS_ALLOWLIST = ["WaveNumber", "EnemiesRemaining", "WaveState", "MissionType", "IntermissionTime"]
_ACTOR_ALLOWLIST = ["Health", "Gold", "Score", "Kills", "Ammo"]


def _op_pie_observe(args):
    world = _game_world()
    if not world:
        return {"error": "not in PIE (no game world)"}
    actors = unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor)
    counts = {}
    for a in actors:
        cn = a.get_class().get_name()
        counts[cn] = counts.get(cn, 0) + 1
    gamestate = None
    gs = unreal.GameplayStatics.get_game_state(world)
    if gs:
        gamestate = {"class": gs.get_class().get_name()}
        for prop in _GS_ALLOWLIST:
            try:
                gamestate[prop] = _jsonable(gs.get_editor_property(prop))
            except Exception:
                pass
    want = args.get("actors_of_interest") or []
    interest = []
    if want:
        for a in actors:
            if a.get_actor_label() in want:
                loc = a.get_actor_location()
                info = {"label": a.get_actor_label(), "class": a.get_class().get_name(),
                        "location": [loc.x, loc.y, loc.z]}
                for prop in _ACTOR_ALLOWLIST:
                    try:
                        info[prop] = _jsonable(a.get_editor_property(prop))
                    except Exception:
                        pass
                interest.append(info)
    return {"gamestate": gamestate, "counts": counts, "actors": interest}


def _op_pie_exec(args):
    world = _game_world()
    if not world:
        return {"error": "not in PIE"}
    target = args["target"]
    fn = args["ufunction"]
    fargs = args.get("args") or {}
    actor = None
    if target == "gamestate":
        actor = unreal.GameplayStatics.get_game_state(world)
    else:
        for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor):
            if a.get_actor_label() == target:
                actor = a
                break
    if not actor:
        return {"error": "target not found: " + str(target)}
    result = actor.call_method(fn, kwargs=fargs if isinstance(fargs, dict) else {})
    return {"ok": True, "result": None if result is None else str(result)}


def _op_pie_screenshot(args):
    w = int(args.get("width", 1920))
    h = int(args.get("height", 1080))
    fname = args.get("filename") or "mcp_pie.png"
    unreal.AutomationLibrary.take_high_res_screenshot(w, h, fname)
    out_dir = unreal.SystemLibrary.get_project_directory() + "Saved/Screenshots/"
    return {"file": out_dir + fname, "async": True}


# --- level authoring -------------------------------------------------------

_MCP_SNAPSHOTS = {}
_MCP_SNAP_SEQ = [0]


def _actor_positions():
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    out = {}
    for a in sub.get_all_level_actors():
        if not a:
            continue
        loc = a.get_actor_location()
        out[a.get_actor_label()] = [round(loc.x, 1), round(loc.y, 1), round(loc.z, 1), a.get_class().get_name()]
    return out


def _clean_slate():
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    for a in list(sub.get_all_level_actors()):
        if not a or isinstance(a, unreal.WorldSettings):
            continue
        try:
            sub.destroy_actor(a)
        except Exception:
            pass


def _op_level_snapshot(args):
    _MCP_SNAP_SEQ[0] += 1
    tok = "snap-%d" % _MCP_SNAP_SEQ[0]
    _MCP_SNAPSHOTS[tok] = _actor_positions()
    return {"token": tok, "actors": len(_MCP_SNAPSHOTS[tok])}


def _op_level_diff(args):
    tok = args.get("before_token") or ""
    if not tok and _MCP_SNAPSHOTS:
        tok = "snap-%d" % _MCP_SNAP_SEQ[0]
    before = _MCP_SNAPSHOTS.get(tok, {})
    after = _actor_positions()
    added = [k for k in after if k not in before]
    removed = [k for k in before if k not in after]
    moved = []
    for k in after:
        if k in before and before[k][:3] != after[k][:3]:
            moved.append({"label": k, "from": before[k][:3], "to": after[k][:3]})
    return {"added": added, "removed": removed, "moved": moved}


def _op_apply_level_recipe(args):
    path = args["script_path"]
    save = args.get("save", True)
    clean = args.get("clean_slate", True)
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    before = len(sub.get_all_level_actors())
    if clean:
        _clean_slate()
    with open(path, "r", encoding="utf-8") as f:
        src = f.read()
    g = {"unreal": unreal, "__name__": "__mcp_recipe__", "__file__": path}
    # Capture the recipe's stdout: the team's idempotent Scripts/*.py print
    # "MISSING:<asset>" for unresolved meshes and defensively continue rather than
    # raise, so we must scan the output (not just catch exceptions) to surface them.
    buf = io.StringIO()
    errors = []
    try:
        with contextlib.redirect_stdout(buf):
            exec(compile(src, path, "exec"), g, g)
    except Exception as e:
        errors.append(str(e))
    out = buf.getvalue()
    missing = [ln.split("MISSING:", 1)[1].strip() for ln in out.splitlines() if "MISSING:" in ln]
    after = len(sub.get_all_level_actors())
    saved = False
    if save and not errors:  # don't persist a half-applied recipe
        saved = bool(unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True))
    return {
        "actors_before": before,
        "actors_after": after,
        "saved": saved,
        "missing_meshes": missing,
        "errors": errors,
    }


# --- assets ----------------------------------------------------------------

def _op_asset_info(args):
    path = args["asset_path"]
    asset = unreal.EditorAssetLibrary.load_asset(path)
    if not asset:
        return {"error": "asset not found: " + str(path)}
    info = {"path": path, "class": asset.get_class().get_name()}
    if isinstance(asset, unreal.StaticMesh):
        info["num_lods"] = asset.get_num_lods()
        try:
            info["nanite"] = bool(asset.get_editor_property("nanite_settings").get_editor_property("enabled"))
        except Exception:
            pass
        try:
            bmin, bmax = asset.get_bounds().box_extent, asset.get_bounds().origin
            info["bounds_extent"] = [bmin.x, bmin.y, bmin.z]
            info["bounds_origin"] = [bmax.x, bmax.y, bmax.z]
        except Exception:
            pass
    return info


def _op_asset_reimport(args):
    path = args["asset_path"]
    asset = unreal.EditorAssetLibrary.load_asset(path)
    if not asset:
        return {"ok": False, "error": "asset not found: " + str(path)}
    tools = unreal.AssetToolsHelpers.get_asset_tools()
    if hasattr(tools, "reimport_assets"):
        tools.reimport_assets([asset])
    else:
        unreal.SystemLibrary.reimport_asset(asset)
    return {"ok": True, "asset_path": path}


def _op_create_material_instance(args):
    parent = unreal.EditorAssetLibrary.load_asset(args["parent"])
    dest = args["dest"]
    pkg_path, name = dest.rsplit("/", 1)
    factory = unreal.MaterialInstanceConstantFactoryNew()
    tools = unreal.AssetToolsHelpers.get_asset_tools()
    mi = tools.create_asset(name, pkg_path, unreal.MaterialInstanceConstant, factory)
    if parent:
        unreal.MaterialEditingLibrary.set_material_instance_parent(mi, parent)
    params = args.get("params") or {}
    for k, v in (params.get("scalar") or {}).items():
        unreal.MaterialEditingLibrary.set_material_instance_scalar_parameter_value(mi, k, float(v))
    for k, v in (params.get("vector") or {}).items():
        unreal.MaterialEditingLibrary.set_material_instance_vector_parameter_value(mi, k, unreal.LinearColor(v[0], v[1], v[2], v[3] if len(v) > 3 else 1.0))
    for k, v in (params.get("texture") or {}).items():
        tex = unreal.EditorAssetLibrary.load_asset(v)
        if tex:
            unreal.MaterialEditingLibrary.set_material_instance_texture_parameter_value(mi, k, tex)
    unreal.EditorAssetLibrary.save_asset(dest)
    return {"path": dest}


_OPS = {
    "editor_status": _op_editor_status,
    "list_actors": _op_list_actors,
    "get_actor": _op_get_actor,
    "spawn_actor": _op_spawn_actor,
    "delete_actor": _op_delete_actor,
    "set_actor_transform": _op_set_actor_transform,
    "open_level": _op_open_level,
    "save_all": _op_save_all,
    "list_assets": _op_list_assets,
    "import_assets": _op_import_assets,
    "start_play": _op_start_play,
    "stop_play": _op_stop_play,
    "live_coding_compile": _op_live_coding_compile,
    "take_screenshot": _op_take_screenshot,
    "pie_observe": _op_pie_observe,
    "pie_exec": _op_pie_exec,
    "pie_screenshot": _op_pie_screenshot,
    "level_snapshot": _op_level_snapshot,
    "level_diff": _op_level_diff,
    "apply_level_recipe": _op_apply_level_recipe,
    "asset_info": _op_asset_info,
    "asset_reimport": _op_asset_reimport,
    "create_material_instance": _op_create_material_instance,
}


def _mcp_dispatch(op, b64args):
    try:
        args = json.loads(base64.b64decode(b64args)) if b64args else {}
        fn = _OPS.get(op)
        if fn is None:
            _emit({"ok": False, "error": "unknown op: " + str(op), "traceback": ""})
            return
        result = fn(args)
        _emit({"ok": True, "result": result})
    except Exception as e:
        _emit({"ok": False, "error": str(e), "traceback": traceback.format_exc()})
