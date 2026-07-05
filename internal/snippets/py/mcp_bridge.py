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

_MCP_BRIDGE_VERSION = 29

import unreal
import json
import base64
import traceback
import os
import io
import math
import time
import fnmatch
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


# --- native cockpit sink (Phase B1, EDITOR_PLUGIN_PLAN.md §5.1) -------------
# When MCPCore drives a dispatch it sets _MCP_NATIVE_SINK to the current op_id so _emit
# routes the result to the native framed channel instead of the stdout marker. This is
# strictly PER-DISPATCH (set/cleared around one op by _mcp_dispatch_native), deliberately
# DECOUPLED from the cockpit_info presence probe, so a session that fell back to uexec can
# never mis-route a result into the native ring and hang.
_MCP_NATIVE_SINK = None


def _mcp_cockpit_bridge():
    """The UMCPCockpitBridge editor subsystem, or None if MCPCore is not loaded."""
    try:
        return unreal.get_editor_subsystem(unreal.MCPCockpitBridge)
    except Exception:
        return None


def _emit(payload):
    # default=_jsonable guarantees an op's result can never poison the marker
    # (a single non-serializable field would otherwise fail the whole dispatch).
    if _MCP_NATIVE_SINK is not None:
        b = _mcp_cockpit_bridge()
        if b is not None:
            try:
                b.emit_result(_MCP_NATIVE_SINK, json.dumps(payload, default=_jsonable))
                return
            except Exception:
                pass  # fall through to the marker (belt-and-suspenders per §5.1)
    print(_MARKER + json.dumps(payload, default=_jsonable))


def _issue(code, label, message):
    """One normalized problem record so agents branch on `code` uniformly across
    ops (scene_apply/apply_level_recipe emit these alongside their legacy arrays).
    Also mirrored to the event stream so a blocked agent can see failures live."""
    rec = {"code": code, "label": label, "message": message}
    try:
        _emit_event("issue", rec)
    except Exception:
        pass
    return rec


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


def _op_pie_observe(args):
    """v2: reflection-driven (no hardcoded allowlist), so it works for any game.
    gamestate/actor fields are DISCOVERED via _reflect_observe. Optional
    include/exclude globs or an explicit `properties` list narrow the fields
    (explicit `properties` preserves your chosen key names for predicate paths).
    Note vs v1: default field keys are the reflected (snake_case) names, e.g.
    gamestate.wave_number; pass properties:["WaveNumber"] to pin a specific key."""
    world = _game_world()
    if not world:
        return {"error": "not in PIE (no game world)"}
    reflect_kw = {
        "include": args.get("include"),
        "exclude": args.get("exclude"),
        "properties": args.get("properties"),
        "max_props": int(args.get("max_props", 48)),
    }
    actors = unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor)
    counts = {}
    for a in actors:
        cn = a.get_class().get_name()
        counts[cn] = counts.get(cn, 0) + 1
    gamestate = None
    gs = unreal.GameplayStatics.get_game_state(world)
    if gs:
        ref = _reflect_observe(gs, **reflect_kw)
        gamestate = dict(ref["properties"])
        gamestate["class"] = ref["class"]
    want = args.get("actors_of_interest") or []
    interest = []
    if want:
        for a in actors:
            if a.get_actor_label() in want:
                loc = a.get_actor_location()
                info = {"label": a.get_actor_label(), "class": a.get_class().get_name(),
                        "location": [loc.x, loc.y, loc.z]}
                info.update(_reflect_observe(a, **reflect_kw)["properties"])
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


def _op_asset_thumbnail(args):
    # AGENTIC_GAMEDEV_PLAN.md §3.1 — the keystone perception primitive: render any
    # browser StaticMesh into a PNG from a canonical 3/4 angle, plus the hard facts
    # (tri/vert count, material slot names, LOD count, bounds). Fixes RC2 (the agent
    # cannot see an asset before using it). Runtime path uses SceneCapture2D +
    # RenderingLibrary.create_render_target2d(RTF_RGBA8) + export_to_disk — validated
    # against UE 5.7 (KismetRenderingLibrary is NOT exposed to Python; float render
    # targets export EXR-only, so the target MUST be RTF_RGBA8).
    import math
    path = args["asset_path"]
    size = int(args.get("size", 512))
    mesh = unreal.EditorAssetLibrary.load_asset(path)
    if not mesh:
        return {"error": "asset not found: " + str(path)}
    if not isinstance(mesh, unreal.StaticMesh):
        return {"error": "asset_thumbnail v1 supports StaticMesh only; got " + mesh.get_class().get_name()}

    facts = {"path": path, "class": "StaticMesh"}
    try:
        facts["num_tris_lod0"] = mesh.get_num_triangles(0)
        facts["num_verts_lod0"] = mesh.get_num_vertices(0)
        facts["num_lods"] = mesh.get_num_lods()
    except Exception as e:
        facts["facts_error"] = str(e)
    try:
        facts["material_slots"] = [str(s.material_slot_name) for s in mesh.get_editor_property("static_materials")]
    except Exception:
        pass
    b = mesh.get_bounds()
    facts["bounds_origin"] = [b.origin.x, b.origin.y, b.origin.z]
    facts["bounds_extent"] = [b.box_extent.x, b.box_extent.y, b.box_extent.z]

    out_dir = os.path.join(unreal.Paths.project_saved_dir(), "MCP", "AssetThumbs")
    os.makedirs(out_dir, exist_ok=True)
    fname = path.strip("/").replace("/", "_") + ".png"
    out_path = os.path.join(out_dir, fname)

    world = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()
    actsys = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    org, ext = b.origin, b.box_extent
    dist = max(ext.x, ext.y, ext.z, 1.0) * 3.2
    dl = math.sqrt(2.36)
    cam = unreal.Vector(org.x + dist / dl, org.y + dist / dl, org.z + 0.35 * dist / dl)
    look = unreal.MathLibrary.find_look_at_rotation(cam, org)
    mesh_actor = capture_actor = None
    temp_lights = []
    try:
        mesh_actor = actsys.spawn_actor_from_object(mesh, unreal.Vector(0, 0, 0))
        # Supply the thumbnail's own lighting so it renders correctly regardless of the
        # editor's current map (a startup/empty map has no lights -> a black capture).
        try:
            # Key light aimed along the camera's view direction (pitched down a bit) so it
            # lights the faces the thumbnail sees; a softer fill from the opposite side
            # opens the shadows. Independent of the editor's current map lighting.
            key_rot = unreal.Rotator(look.pitch - 25.0, look.yaw, 0.0)
            sun = actsys.spawn_actor_from_class(
                unreal.DirectionalLight, unreal.Vector(0, 0, org.z + ext.z + 500.0), key_rot)
            sun.directional_light_component.set_intensity(12.0)
            temp_lights.append(sun)
            fill = actsys.spawn_actor_from_class(
                unreal.DirectionalLight, unreal.Vector(0, 0, org.z + ext.z + 500.0),
                unreal.Rotator(-20.0, look.yaw + 150.0, 0.0))
            fill.directional_light_component.set_intensity(4.0)
            temp_lights.append(fill)
        except Exception as le:
            facts["light_warn"] = str(le)
        rt = unreal.RenderingLibrary.create_render_target2d(world, size, size, unreal.TextureRenderTargetFormat.RTF_RGBA8)
        capture_actor = actsys.spawn_actor_from_class(unreal.SceneCapture2D, cam, look)
        comp = capture_actor.capture_component2d
        comp.texture_target = rt
        comp.capture_source = unreal.SceneCaptureSource.SCS_FINAL_COLOR_LDR
        comp.fov_angle = 40.0
        comp.capture_scene()
        comp.capture_scene()
        opts = unreal.ImageWriteOptions()
        opts.format = unreal.DesiredImageFormat.PNG
        opts.overwrite_file = True
        # export_to_disk writes asynchronously; poll briefly so `rendered` is accurate.
        try:
            os.remove(out_path)
        except OSError:
            pass
        rt.export_to_disk(out_path, opts)
        import time as _t
        for _ in range(40):  # up to ~4s
            if os.path.exists(out_path) and os.path.getsize(out_path) > 0:
                break
            _t.sleep(0.1)
    except Exception as re:
        # A mid-render UE exception must not discard the hard facts already gathered.
        facts["render_error"] = str(re)
    finally:
        if mesh_actor:
            actsys.destroy_actor(mesh_actor)
        if capture_actor:
            actsys.destroy_actor(capture_actor)
        for lt in temp_lights:
            try:
                actsys.destroy_actor(lt)
            except Exception:
                pass
    facts["thumbnail_path"] = out_path
    facts["rendered"] = os.path.exists(out_path) and os.path.getsize(out_path) > 0
    return facts


def _op_audio_capture_start(args):
    # AGENTIC_GAMEDEV_PLAN.md §6.3 — start the ISubmixBufferListener tap on the main
    # submix (C++ UMCPCaptureSubsystem). Audio only renders in PIE, so a play session
    # must be running.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world to tap (start PIE first)"}
    sub = unreal.MCPCaptureSubsystem.get(world)
    if not sub:
        return {"error": "MCPCaptureSubsystem unavailable in this world"}
    session = sub.start_audio_capture(args.get("session", ""))
    if not session:
        return {"error": "audio capture failed to start (no active audio device, or already tapping)"}
    return {"session": session, "running": True}


def _op_audio_capture_stop(args):
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world"}
    sub = unreal.MCPCaptureSubsystem.get(world)
    if not sub:
        return {"error": "MCPCaptureSubsystem unavailable in this world"}
    summary = sub.stop_audio_capture(args.get("out_dir", ""))
    if not summary:
        return {"error": "audio capture not running"}
    try:
        return json.loads(summary)
    except Exception:
        return {"summary": summary}


def _op_company_status(args):
    # Read the CompanyMVP economy from the live PIE world: company Capital + each
    # production building's chosen supplier/market and last-cycle profit.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE on L_CompanyCity)"}
    mgr = None
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.CompanyManager):
        mgr = a
        break
    if not mgr:
        return {"error": "no CompanyManager in world"}
    blds = []
    for b in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.ProductionBuilding):
        sups = b.get_editor_property("suppliers")
        mkts = b.get_editor_property("markets")
        si = b.get_editor_property("supplier_index")
        mi = b.get_editor_property("market_index")
        blds.append({
            "name": str(b.get_editor_property("building_name")),
            "product": str(b.get_editor_property("product")),
            "supplier": str(sups[si].get_editor_property("name")) if si < len(sups) else "?",
            "buy_price": int(sups[si].get_editor_property("unit_price")) if si < len(sups) else 0,
            "market": str(mkts[mi].get_editor_property("name")) if mi < len(mkts) else "?",
            "sell_price": int(mkts[mi].get_editor_property("unit_price")) if mi < len(mkts) else 0,
            "last_profit": int(b.get_editor_property("last_profit")),
        })
    return {"capital": int(mgr.get_editor_property("capital")), "buildings": blds}


def _op_widget_render(args):
    # Render a UserWidget CLASS offscreen to a PNG via MCPAuthoringSubsystem::CaptureWidget
    # (FWidgetRenderer) — the visual-iteration loop the Python WidgetTree path can't do in
    # UE 5.7 (WidgetTree is protected). No PIE needed.
    auth = unreal.get_editor_subsystem(unreal.MCPAuthoringSubsystem)
    if not auth:
        return {"error": "MCPAuthoring editor subsystem unavailable (module not compiled/loaded)"}
    out = auth.capture_widget(args["widget_class"], int(args.get("width", 1280)), int(args.get("height", 720)), args["out_path"])
    return {"ok": bool(out), "path": out}


def _op_company_build(args):
    # Place a factory of Catalog[option] at a world location (what a HUD build-palette
    # click does) — spends Capital. Returns the new capital + whether it built.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE)"}
    mgr = None
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.CompanyManager):
        mgr = a
        break
    if not mgr:
        return {"error": "no CompanyManager"}
    loc = args.get("location", [0, 0, 0])
    before = int(mgr.get_editor_property("capital"))
    b = mgr.build(int(args.get("option", 0)), unreal.Vector(float(loc[0]), float(loc[1]), float(loc[2])))
    return {"built": b is not None, "name": str(b.get_editor_property("building_name")) if b else None,
            "spent": before - int(mgr.get_editor_property("capital")),
            "capital": int(mgr.get_editor_property("capital"))}


def _op_company_select(args):
    # The Capitalism-2 selection: on a production building, choose the SUPPLIER to buy
    # inputs from and the MARKET to sell the product to (indices into the manager's
    # catalogs). Profit updates next cycle. This is the tool a HUD/player drives.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE)"}
    blds = list(unreal.GameplayStatics.get_all_actors_of_class(world, unreal.ProductionBuilding))
    idx = int(args.get("building", 0))
    if idx >= len(blds):
        return {"error": "no production building %d" % idx}
    b = blds[idx]
    if args.get("supplier") is not None:
        b.set_editor_property("supplier_index", int(args["supplier"]))
    if args.get("market") is not None:
        b.set_editor_property("market_index", int(args["market"]))
    return {"ok": True, "building": str(b.get_editor_property("building_name")),
            "supplier_index": int(b.get_editor_property("supplier_index")),
            "market_index": int(b.get_editor_property("market_index"))}


def _op_pawn_state(args):
    # Read the player pawn's location + velocity (for input_inject / verb_response
    # validation: sample this across an injected-input window to see the verb respond).
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE first)"}
    pawn = unreal.GameplayStatics.get_player_pawn(world, int(args.get("player", 0)))
    if not pawn:
        return {"error": "no player pawn"}
    loc = pawn.get_actor_location()
    vel = pawn.get_velocity()
    speed = (vel.x * vel.x + vel.y * vel.y + vel.z * vel.z) ** 0.5
    return {"loc": [loc.x, loc.y, loc.z], "vel": [vel.x, vel.y, vel.z], "speed": speed}


def _op_play_test_sound(args):
    # Test helper (audio-tap validation): play a sound cue into the PIE world so the
    # submix tap has a known non-silent signal to certify.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE first)"}
    sound = unreal.load_asset(args["sound"])
    if not sound:
        return {"error": "sound not found: " + str(args.get("sound"))}
    unreal.GameplayStatics.play_sound2d(world, sound, float(args.get("volume", 1.0)))
    return {"played": True, "sound": args["sound"]}


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


# ===========================================================================
# v7 additions — reflection-driven observation, multi-frame capture recorder,
# declarative scene realize + design lint, and tighter editor integration.
# All ops are additive and injection-safe (base64-JSON args via _mcp_dispatch).
# ===========================================================================

# --- reflection-driven observation (game-agnostic; replaces hardcoded allowlists) ---

# Properties that are almost always noise on any UObject/AActor and would bloat
# an observe payload. Excluded by default; a caller can still request them via
# explicit `properties`.
_NOISE_PROPS = {
    "owner", "instigator", "root_component", "parent_component", "children",
    "attached_actors", "tags", "name", "outer", "class", "layers", "world",
    "level", "components", "actor_guid", "content_bundle_guid", "input",
    "controller", "player_state", "game_mode_class", "game_session",
    "authority_game_mode", "game_state", "spectator_class", "net_driver_name",
    "default_pawn_class", "hud_class", "player_controller_class",
    "replicated_movement", "instance_components", "blocking_volume",
}


def _coerce_prop(v, max_str):
    """Coerce a reflected property value to something compact and JSON-safe.
    Vectors/Rotators become [x,y,z] / [pitch,yaw,roll]; enums become their NAME;
    primitives pass through; everything else becomes a length-capped str()."""
    if v is None or isinstance(v, (bool, int, float)):
        return v
    if isinstance(v, str):
        return v if len(v) <= max_str else v[:max_str] + "..."
    # Vector-ish (Vector, Vector2D) and Rotator
    if isinstance(v, unreal.Rotator):
        return [v.pitch, v.yaw, v.roll]
    if hasattr(v, "x") and hasattr(v, "y"):
        out = [getattr(v, "x"), getattr(v, "y")]
        if hasattr(v, "z"):
            out.append(getattr(v, "z"))
        return out
    name = getattr(v, "name", None)  # UENUM -> enumerator name
    if isinstance(name, str):
        return name
    s = str(v)
    return s if len(s) <= max_str else s[:max_str] + "..."


def _is_scalarish(v):
    """Keep only compactly-representable values in reflected output."""
    return v is None or isinstance(v, (bool, int, float, str, list))


def _reflect_observe(obj, include=None, exclude=None, properties=None,
                     max_props=64, max_str=512):
    """Discover and read an object's exposed properties WITHOUT a hardcoded
    allowlist, so this works for any game. Returns
    {class, path, properties:{name:value}, functions:[name], truncated}.

    - properties given: read exactly those (via get_editor_property, which
      accepts either the snake_case or the C++ PascalCase name), preserving the
      caller's chosen key names (lets callers pin specific predicate paths).
    - else: enumerate dir(obj), drop callables/dunders/noise, apply include
      globs (default ['*']) minus exclude globs, read via getattr, coerce, and
      keep only compact scalar/list/enum values, capped at max_props.
    functions is a best-effort heuristic (callable attributes) — UE 5.x exposes
    no clean UFunction enumerator, so it is explicitly approximate."""
    include = include or ["*"]
    exclude = list(exclude or [])
    out = {}
    truncated = False
    if properties:
        for name in properties:
            try:
                out[name] = _coerce_prop(obj.get_editor_property(name), max_str)
            except Exception:
                try:
                    out[name] = _coerce_prop(getattr(obj, name), max_str)
                except Exception:
                    pass
    else:
        funcs = []
        for name in sorted(dir(obj)):
            if name.startswith("_"):
                continue
            if name in _NOISE_PROPS:
                continue
            if not any(fnmatch.fnmatch(name, p) for p in include):
                continue
            if any(fnmatch.fnmatch(name, p) for p in exclude):
                continue
            try:
                val = getattr(obj, name)
            except Exception:
                continue
            if callable(val):
                if len(funcs) < 128:
                    funcs.append(name)
                continue
            coerced = _coerce_prop(val, max_str)
            if not _is_scalarish(coerced):
                continue
            if len(out) >= max_props:
                truncated = True
                break
            out[name] = coerced
        result_functions = funcs
    try:
        path = obj.get_path_name()
    except Exception:
        path = None
    res = {
        "class": obj.get_class().get_name() if hasattr(obj, "get_class") else str(type(obj)),
        "path": path,
        "properties": out,
        "truncated": truncated,
    }
    if not properties:
        res["functions"] = result_functions
    return res


def _resolve_reflect_target(target):
    """Resolve a reflect/observe target string to a live object:
    'gamestate' -> the game state; an actor label (game world first, then
    editor); or a /Script or /Game class path -> its class default object."""
    if target == "gamestate":
        world = _game_world()
        return unreal.GameplayStatics.get_game_state(world) if world else None
    if target in ("playercontroller", "pawn", "playerpawn"):
        world = _game_world()
        if not world:
            return None
        pc = unreal.GameplayStatics.get_player_controller(world, 0)
        if target == "playercontroller":
            return pc
        return pc.get_controlled_pawn() if pc else None
    if isinstance(target, str) and target.startswith("/"):
        cls = unreal.load_class(None, target) if target.startswith("/Script/") else None
        if cls is None:
            asset = unreal.load_asset(target)
            cls = asset.generated_class() if isinstance(asset, unreal.Blueprint) else asset
        return unreal.get_default_object(cls) if cls else None
    # actor label: prefer the running game world, fall back to the editor world
    world = _game_world()
    if world:
        for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor):
            if a.get_actor_label() == target:
                return a
    return _find_actor(target)


def _op_reflect_object(args):
    target = args.get("target", "gamestate")
    obj = _resolve_reflect_target(target)
    if not obj:
        return {"error": "target not found: " + str(target)}
    return _reflect_observe(
        obj,
        include=args.get("include"),
        exclude=args.get("exclude"),
        properties=args.get("properties"),
        max_props=int(args.get("max_props", 64)),
        max_str=int(args.get("max_str", 512)),
    )


# --- shared world / bounds helpers ------------------------------------------

def _editor_world():
    return unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()


def _pick_world(which):
    """which in {auto, editor, game}. 'auto' prefers the running game world."""
    if which == "editor":
        return _editor_world()
    if which == "game":
        return _game_world()
    return _game_world() or _editor_world()


def _actor_bounds_aabb(actors):
    """Union AABB of the given actors as {origin:[x,y,z], extent:[x,y,z]}."""
    lo = [float("inf")] * 3
    hi = [float("-inf")] * 3
    found = False
    for a in actors:
        try:
            origin, extent = a.get_actor_bounds(False)
        except Exception:
            continue
        found = True
        for i, o, e in ((0, origin.x, extent.x), (1, origin.y, extent.y), (2, origin.z, extent.z)):
            lo[i] = min(lo[i], o - e)
            hi[i] = max(hi[i], o + e)
    if not found:
        return {"origin": [0.0, 0.0, 0.0], "extent": [1000.0, 1000.0, 1000.0]}
    origin = [(lo[i] + hi[i]) / 2.0 for i in range(3)]
    extent = [max((hi[i] - lo[i]) / 2.0, 1.0) for i in range(3)]
    return {"origin": origin, "extent": extent}


def _pyr_to_rotator(pyr):
    """[pitch, yaw, roll] -> unreal.Rotator(roll, pitch, yaw)."""
    return unreal.Rotator(pyr[2], pyr[0], pyr[1])


# --- multi-frame capture recorder (in-editor; Go collects in one capture_stop) ---
#
# The command channel is single-flight, so a high-frequency capture cannot be
# driven by Go polling. Instead an in-editor slate-post-tick callback buffers
# timestamped frames + observed state to disk with a manifest; Go reads the
# whole session off disk after ONE capture_stop.

_MCP_RECORDERS = {}


def _capture_session_dir(session):
    d = unreal.SystemLibrary.get_project_directory() + "Saved/MCP/capture/" + session + "/"
    if not os.path.isdir(d):
        os.makedirs(d, exist_ok=True)
    return d


def _make_scene_capture(world, width, height):
    """Spawn a SceneCapture2D + render target in `world` (editor/simulate). Not
    valid for possessed-PIE gameplay (that lives in a separate game world) —
    use source='pie_highres' there."""
    rt = unreal.RenderingLibrary.create_render_target2d(
        world, width, height, unreal.TextureRenderTargetFormat.RTF_RGBA8,
        unreal.LinearColor(0, 0, 0, 1), False)
    actors = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    cap = actors.spawn_actor_from_class(unreal.SceneCapture2D, unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0))
    cc = cap.capture_component2d
    cc.set_editor_property("texture_target", rt)
    cc.set_editor_property("capture_source", unreal.SceneCaptureSource.SCS_FINAL_COLOR_LDR)
    cc.set_editor_property("always_persist_rendering_state", True)
    return cap, rt


def _position_capture(rec):
    """Aim the recorder's SceneCapture2D for this frame. Default follows the
    editor viewport camera (records what the editor shows, incl. a simulating
    world); 'fixed' uses a static pose; 'actor' rides an actor's transform."""
    cam = rec.get("camera") or {}
    mode = cam.get("mode", "viewport")
    cap = rec["cap"]
    if cap is None:
        return
    if mode == "fixed" and cam.get("location"):
        loc = cam["location"]
        rot = cam.get("rotation_pyr") or [0, 0, 0]
        cap.set_actor_location_and_rotation(
            unreal.Vector(loc[0], loc[1], loc[2]), _pyr_to_rotator(rot), False, False)
    elif mode == "actor" and cam.get("actor_label"):
        a = _find_actor(cam["actor_label"])
        if a:
            cap.set_actor_location_and_rotation(a.get_actor_location(), a.get_actor_rotation(), False, False)
    else:
        info = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_level_viewport_camera_info()
        if info:
            loc, rot = info
            cap.set_actor_location_and_rotation(loc, rot, False, False)


def _recorder_observe(rec):
    """Per-frame observation reusing the reflection observe over the game world."""
    world = _pick_world(rec["world"])
    state = {"counts": {}, "gamestate": None}
    if not world:
        return state
    try:
        actors = unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor)
        counts = {}
        for a in actors:
            cn = a.get_class().get_name()
            counts[cn] = counts.get(cn, 0) + 1
        state["counts"] = counts
        gs = unreal.GameplayStatics.get_game_state(world)
        if gs:
            ref = _reflect_observe(gs, **rec["observe"])
            g = dict(ref["properties"])
            g["class"] = ref["class"]
            state["gamestate"] = g
        tracked = []
        want = rec.get("track_actors") or []
        if want:
            for a in actors:
                if a.get_actor_label() in want:
                    loc = a.get_actor_location()
                    rot = a.get_actor_rotation()
                    entry = {"label": a.get_actor_label(),
                             "location": [loc.x, loc.y, loc.z],
                             "rotation_pyr": [rot.pitch, rot.yaw, rot.roll]}
                    entry.update(_reflect_observe(a, **rec["observe"])["properties"])
                    tracked.append(entry)
        state["tracked"] = tracked
    except Exception as e:
        state["observe_error"] = str(e)
    return state


def _recorder_tick(session):
    """Return the slate-post-tick callback for a recorder session."""
    def _cb(delta_seconds):
        rec = _MCP_RECORDERS.get(session)
        if not rec or not rec["running"]:
            return
        try:
            dt = float(delta_seconds) if delta_seconds else 0.0
            rec["elapsed"] += dt
            # Track the worst frame time since the last sample so a hitch that
            # happened BETWEEN screenshots is still reported (perf tier-a, free).
            dt_ms = dt * 1000.0
            if dt_ms > rec.get("max_dt_ms", 0.0):
                rec["max_dt_ms"] = dt_ms
            if rec["elapsed"] - rec["last_fire"] < rec["interval_s"] and rec["frames"]:
                return
            rec["last_fire"] = rec["elapsed"]
            idx = len(rec["frames"])
            rel = rec["file_prefix"] + ("f%05d.png" % idx)
            world = _pick_world(rec["world"])
            if rec["source"] == "pie_highres":
                # HighResShot writes asynchronously to Saved/Screenshots/<rel>;
                # Go tolerates a not-yet-flushed final frame (skips absent files).
                unreal.AutomationLibrary.take_high_res_screenshot(rec["width"], rec["height"], rel)
            else:
                _position_capture(rec)
                cc = rec["cap"].capture_component2d
                cc.capture_scene()
                unreal.RenderingLibrary.export_render_target(world, rec["rt"], rec["dir"], rel)
            t_world = unreal.GameplayStatics.get_time_seconds(world) if world else 0.0
            st = _recorder_observe(rec)
            hitch_ms = rec.get("max_dt_ms", dt_ms)
            st["perf"] = {
                "fps": (1000.0 / dt_ms) if dt_ms > 0 else 0.0,
                "frame_ms": dt_ms,
                "hitch_ms": hitch_ms,  # worst frame time since the last sample
            }
            rec["max_dt_ms"] = 0.0  # reset the hitch window
            rec["frames"].append({
                "index": idx, "file": rel, "t_wall": rec["elapsed"],
                "t_world": t_world, "state": st,
            })
            rec["errors"] = 0
            rec["last_error"] = None
            if idx + 1 >= rec["max_frames"]:
                _recorder_selfstop(rec, "max_frames")
            elif rec["elapsed"] >= rec["max_seconds"]:
                _recorder_selfstop(rec, "max_seconds")
            elif (idx + 1) % 20 == 0:
                _recorder_flush(rec)
        except Exception as e:
            rec["errors"] = rec.get("errors", 0) + 1
            rec["last_error"] = str(e)
            if rec["errors"] >= 5:
                _recorder_selfstop(rec, "errors: " + str(e))
    return _cb


def _recorder_selfstop(rec, reason):
    """Stop a recorder from inside its own tick and release its editor resources
    (so an auto-stop/error can't leak the callback or the SceneCapture2D)."""
    rec["running"] = False
    rec["stop_reason"] = reason
    _recorder_flush(rec)
    _recorder_teardown(rec)


def _recorder_teardown(rec):
    """Idempotently release a recorder's editor resources: unregister the slate
    callback and destroy the SceneCapture2D. Safe from the tick's self-stop paths
    AND from capture_stop; leaves rec['frames'] intact for collection."""
    h = rec.get("handle")
    if h is not None:
        try:
            unreal.unregister_slate_post_tick_callback(h)
        except Exception:
            pass
        rec["handle"] = None
    cap = rec.get("cap")
    if cap is not None:
        try:
            unreal.get_editor_subsystem(unreal.EditorActorSubsystem).destroy_actor(cap)
        except Exception:
            pass
        rec["cap"] = None
    rec["rt"] = None


def _recorder_flush(rec):
    try:
        with open(rec["dir"] + "manifest.json", "w", encoding="utf-8") as f:
            json.dump({
                "session": rec["session"], "dir": rec["dir"], "source": rec["source"],
                "cell_width": rec["width"], "cell_height": rec["height"],
                "frame_count": len(rec["frames"]), "frames": rec["frames"],
            }, f, default=_jsonable)
    except Exception:
        pass


# --- game_scene backend: the UnrealMCP plugin's C++ SceneCapture in the GAME
# world (renders backgrounded, sees live possessed-PIE — the one thing Python
# can't do). Optional: only used when source='game_scene' AND the plugin is
# compiled into the project. Slots into the same capture_start/stop/poll flow.

def _mcp_capture_subsystem():
    cls = getattr(unreal, "MCPCaptureSubsystem", None)
    if cls is None:
        return None
    world = _game_world()
    if not world:
        return None
    # The plugin's static Get(WorldContext) — Python can't call the C++ template
    # UGameInstance::GetSubsystem<T>() directly.
    try:
        return cls.get(world)
    except Exception:
        return None


def _game_scene_start(args, session, width, height):
    sub = _mcp_capture_subsystem()
    if sub is None:
        return {"error": "game_scene capture needs the UnrealMCP plugin (MCPCaptureSubsystem) compiled into the "
                         "project AND an active PIE game world", "code": "PLUGIN_MISSING"}
    out_dir = _capture_session_dir(session)
    cam = args.get("camera") or {}
    mode = cam.get("mode", "player")
    cam_cls = getattr(unreal, "EMCPCaptureCamera", None)
    if cam_cls is None:
        return {"error": "game_scene capture needs the UnrealMCP plugin's EMCPCaptureCamera enum "
                         "(recompile the plugin into the project)", "code": "PLUGIN_MISSING"}
    # Resolve the camera to the ENUM instance — NEVER a bare int 0 (StartCapture's
    # EMCPCaptureCamera param rejects an int). Use explicit None checks, not `or`: the
    # 0-valued PLAYER member is falsy, so `getattr(...) or PLAYER` would misfire.
    name = {"fixed": "FIXED", "actor": "ACTOR"}.get(mode, "PLAYER")
    cam_val = getattr(cam_cls, name, None)
    if cam_val is None:
        cam_val = getattr(cam_cls, "PLAYER", None)
    if cam_val is None:
        return {"error": "EMCPCaptureCamera has no PLAYER member (stale plugin build)", "code": "PLUGIN_MISSING"}
    loc = cam.get("location") or [0, 0, 0]
    rot = cam.get("rotation_pyr") or [0, 0, 0]
    interval = max(float(args.get("interval_s", 0.25)), 0.05)
    ret = sub.start_capture(session, out_dir, "", int(width), int(height), interval,
                            int(args.get("max_frames", 240)), float(args.get("max_seconds", 60)),
                            cam_val, unreal.Vector(loc[0], loc[1], loc[2]), _pyr_to_rotator(rot),
                            float(cam.get("fov", 90.0)), cam.get("actor_label", ""),
                            bool(args.get("include_ui", False)))
    if not ret:
        return {"error": "game_scene capture failed to start (no game world / spawn failed)",
                "code": "CAPTURE_START_FAILED"}
    _MCP_RECORDERS[session] = {"session": session, "dir": out_dir, "backend": "plugin",
                               "running": True, "width": width, "height": height}
    return {"session": session, "dir": out_dir, "source": "game_scene", "running": True, "backend": "plugin"}


def _game_scene_stop(session, rec):
    sub = _mcp_capture_subsystem()
    manifest = sub.stop_capture() if sub else ""
    _MCP_RECORDERS.pop(session, None)
    try:
        data = json.loads(manifest) if manifest else {}
    except Exception:
        data = {}
    return {
        "manifest_path": rec["dir"] + "manifest.json", "dir": rec["dir"], "session": session,
        "frame_count": data.get("frame_count", 0),
        "cell_width": data.get("cell_width", rec.get("width", 0)),
        "cell_height": data.get("cell_height", rec.get("height", 0)),
        "frames": data.get("frames", []),
        "stop_reason": data.get("stop_reason", "requested"),
    }


def _op_capture_start(args):
    session = args.get("session") or ("s%d" % int(time.time() * 1000))
    old = _MCP_RECORDERS.get(session)
    if old:
        if old.get("running"):
            return {"error": "session already running: " + session}
        _recorder_teardown(old)  # reclaim a stopped-but-uncollected session id
    world_sel = args.get("world", "auto")
    source = args.get("source", "scene_capture")
    if source == "game_scene":
        return _game_scene_start(args, session, int(args.get("cell_width", 480)), int(args.get("cell_height", 270)))
    width = int(args.get("cell_width", 480))
    height = int(args.get("cell_height", 270))
    world = _pick_world(world_sel)
    if not world:
        return {"error": "no world to capture (open a level / start play)"}
    warning = None
    if source == "pie_highres":
        # HighResShot writes to Saved/Screenshots — record frames there under a
        # session-prefixed name so Go resolves dir+file to the real path.
        rec_dir = unreal.SystemLibrary.get_project_directory() + "Saved/Screenshots/"
        file_prefix = "mcp_" + session + "_"
        min_interval = 0.2  # HighResShot is slow; a tight interval drops frames
        cap, rt = (None, None)
        if not os.path.isdir(rec_dir):
            os.makedirs(rec_dir, exist_ok=True)
    else:
        rec_dir = _capture_session_dir(session)
        file_prefix = ""
        min_interval = 0.02
        cap, rt = _make_scene_capture(world, width, height)
        if unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).is_in_play_in_editor():
            warning = ("scene_capture renders the editor world; for possessed-PIE "
                       "gameplay use source='pie_highres'")
    rec = {
        "session": session, "dir": rec_dir, "file_prefix": file_prefix, "world": world_sel,
        "source": source, "width": width, "height": height,
        "interval_s": max(float(args.get("interval_s", 0.25)), min_interval),
        "max_frames": int(args.get("max_frames", 240)),
        "max_seconds": float(args.get("max_seconds", 60)),
        "track_actors": args.get("track_actors") or [],
        "camera": args.get("camera") or {},
        "observe": {
            "include": args.get("observe", {}).get("include"),
            "exclude": args.get("observe", {}).get("exclude"),
            "properties": args.get("observe", {}).get("properties"),
            "max_props": int(args.get("observe", {}).get("max_props", 48)),
        },
        "frames": [], "elapsed": 0.0, "last_fire": -1e9, "running": True,
        "errors": 0, "last_error": None, "stop_reason": None, "max_dt_ms": 0.0,
        "cap": cap, "rt": rt,
    }
    rec["handle"] = unreal.register_slate_post_tick_callback(_recorder_tick(session))
    _MCP_RECORDERS[session] = rec
    out = {"session": session, "dir": rec_dir, "source": source, "running": True}
    if warning:
        out["warning"] = warning
    return out


def _op_capture_poll(args):
    """One-shot status read — NO unreal call, NOT a poll loop (the recorder owns
    cadence; Go must not per-frame round-trip)."""
    rec = _MCP_RECORDERS.get(args.get("session", ""))
    if not rec:
        return {"error": "no such session"}
    if rec.get("backend") == "plugin":
        sub = _mcp_capture_subsystem()
        if sub:
            try:
                return json.loads(sub.poll_capture())
            except Exception:
                pass
        return {"running": rec.get("running", False), "frames_captured": 0, "dir": rec["dir"]}
    return {
        "running": rec["running"], "frames_captured": len(rec["frames"]),
        "last_t_world": rec["frames"][-1]["t_world"] if rec["frames"] else None,
        "elapsed_s": rec["elapsed"], "errors": rec.get("errors", 0),
        "dir": rec["dir"],
    }


def _op_capture_stop(args):
    session = args.get("session", "")
    rec = _MCP_RECORDERS.get(session)
    if not rec:
        return {"error": "no such session"}
    if rec.get("backend") == "plugin":
        return _game_scene_stop(session, rec)
    if rec["running"]:
        rec["stop_reason"] = "requested"
    rec["running"] = False
    _recorder_teardown(rec)
    _recorder_flush(rec)
    _MCP_RECORDERS.pop(session, None)
    return {
        "manifest_path": rec["dir"] + "manifest.json", "dir": rec["dir"],
        "session": session, "frame_count": len(rec["frames"]),
        "cell_width": rec["width"], "cell_height": rec["height"],
        "frames": rec["frames"], "stop_reason": rec.get("stop_reason") or "requested",
    }


def _op_capture_list(args):
    # .get() so a game_scene/plugin session (no in-memory frames list) is listed too.
    return [
        {"session": s, "running": r.get("running", False),
         "frames_captured": len(r.get("frames", [])), "dir": r.get("dir", ""),
         "backend": r.get("backend", "recorder")}
        for s, r in _MCP_RECORDERS.items()
    ]


def _op_capture_poses(args):
    """Synchronous multi-angle capture: render ONE SceneCapture2D from each of
    the given Go-computed poses (orbit ring / bookmarks / arbitrary list). No
    tick, no async — the backgrounded-safe spatial axis for contact sheets."""
    poses = args.get("poses") or []
    width = int(args.get("cell_width", 480))
    height = int(args.get("cell_height", 270))
    world = _pick_world(args.get("world", "editor"))
    if not world:
        return {"error": "no world"}
    session = args.get("session") or ("poses%d" % int(time.time() * 1000))
    out_dir = _capture_session_dir(session)
    cap, rt = _make_scene_capture(world, width, height)
    cc = cap.capture_component2d
    cells = []
    try:
        for i, p in enumerate(poses):
            loc = p.get("location") or [0, 0, 0]
            rot = p.get("rotation_pyr") or [0, 0, 0]
            cap.set_actor_location_and_rotation(
                unreal.Vector(loc[0], loc[1], loc[2]), _pyr_to_rotator(rot), False, False)
            cc.capture_scene()
            rel = "p%05d.png" % i
            unreal.RenderingLibrary.export_render_target(world, rt, out_dir, rel)
            cells.append({"index": i, "file": rel, "location": loc, "rotation_pyr": rot})
    finally:
        unreal.get_editor_subsystem(unreal.EditorActorSubsystem).destroy_actor(cap)
    return {"dir": out_dir, "session": session, "cell_width": width, "cell_height": height, "cells": cells}


# --- declarative scene realize + design lint --------------------------------

@contextlib.contextmanager
def _transaction(label):
    """One atomic, named undo unit if ScopedEditorTransaction is available;
    otherwise a no-op (the realize still runs, just not coalesced)."""
    tx = getattr(unreal, "ScopedEditorTransaction", None)
    if tx is None:
        yield None
        return
    with tx(label) as t:
        yield t


def _resolve_spawn_class(placement):
    kind = placement.get("kind", "static_mesh")
    cp = placement.get("class_path") or ""
    if kind == "static_mesh" and not cp:
        return unreal.StaticMeshActor
    env_map = {
        "directional_light": unreal.DirectionalLight, "sky_light": unreal.SkyLight,
        "sky_atmosphere": unreal.SkyAtmosphere, "height_fog": unreal.ExponentialHeightFog,
        "post_process": unreal.PostProcessVolume,
    }
    if kind in env_map and not cp:
        return env_map[kind]
    if cp.startswith("/Script/"):
        return unreal.load_class(None, cp)
    asset = unreal.load_asset(cp) if cp else None
    if isinstance(asset, unreal.Blueprint):
        return asset.generated_class()
    if isinstance(asset, unreal.Class):
        return asset
    return unreal.StaticMeshActor


def _route_component_property(actor, key, value):
    """Route a well-known env/light/fog/exposure key to the correct COMPONENT
    (these are not actor-level UPROPERTYs). Returns True if the key was recognized
    and handled (best-effort), else False so the caller tries the actor directly."""
    if key in ("intensity_lux", "intensity", "color_temperature_k", "temperature"):
        lc = None
        for cls in (unreal.DirectionalLightComponent, unreal.SkyLightComponent, unreal.LightComponent):
            try:
                lc = actor.get_component_by_class(cls)
            except Exception:
                lc = None
            if lc:
                break
        if not lc:
            return False
        if key in ("intensity_lux", "intensity"):
            lc.set_editor_property("intensity", float(value))
        else:
            try:
                lc.set_editor_property("use_temperature", True)
            except Exception:
                pass
            lc.set_editor_property("temperature", float(value))
        return True
    if key in ("density", "fog_density"):
        try:
            fc = actor.get_component_by_class(unreal.ExponentialHeightFogComponent)
        except Exception:
            fc = None
        if fc:
            fc.set_editor_property("fog_density", float(value))
            return True
        return False
    if key in ("exposure_ev100", "exposure_method") and isinstance(actor, unreal.PostProcessVolume):
        try:
            settings = actor.get_editor_property("settings")
            if key == "exposure_ev100":
                settings.set_editor_property("auto_exposure_method", unreal.AutoExposureMethod.AEM_MANUAL)
                settings.set_editor_property("override_auto_exposure_bias", True)
                settings.set_editor_property("auto_exposure_bias", float(value))
                try:
                    settings.set_editor_property("override_auto_exposure_method", True)
                except Exception:
                    pass
            actor.set_editor_property("settings", settings)
            return True
        except Exception:
            return False
    return False


def _apply_material(actor, path, label, warnings):
    mat = unreal.load_asset(path) if isinstance(path, str) else None
    if not mat:
        warnings.append(label + ": material not found: " + str(path))
        return
    comp = actor.get_component_by_class(unreal.StaticMeshComponent)
    if not comp:
        comp = actor.get_component_by_class(unreal.PrimitiveComponent)
    if not comp:
        warnings.append(label + ": no mesh component for material")
        return
    try:
        comp.set_material(0, mat)
    except Exception as e:
        warnings.append(label + ": material: " + str(e))


def _apply_placement(sub, existing, placement, missing, errors, warnings):
    label = placement["label"]
    actor = existing.get(label)
    created = False
    loc = placement.get("location", [0, 0, 0])
    rot = placement.get("rotation_pyr", [0, 0, 0])
    if actor is None:
        cls = _resolve_spawn_class(placement)
        if not cls:
            errors.append(label + ": could not resolve class " + str(placement.get("class_path")))
            return None, False
        actor = sub.spawn_actor_from_class(cls, unreal.Vector(loc[0], loc[1], loc[2]), _pyr_to_rotator(rot))
        if not actor:
            errors.append(label + ": spawn failed")
            return None, False
        actor.set_actor_label(label)
        created = True
    else:
        actor.set_actor_location(unreal.Vector(loc[0], loc[1], loc[2]), False, False)
        actor.set_actor_rotation(_pyr_to_rotator(rot), False)
    scale = placement.get("scale") or [1, 1, 1]
    actor.set_actor_scale3d(unreal.Vector(scale[0], scale[1], scale[2]))
    mesh_path = placement.get("static_mesh_path") or ""
    if mesh_path:
        mesh = unreal.load_asset(mesh_path)
        comp = actor.get_component_by_class(unreal.StaticMeshComponent)
        if not mesh:
            missing.append(label + " -> " + mesh_path)
        elif comp:
            comp.set_static_mesh(mesh)
    # Material: the dedicated Placement.Material field, or a 'material' left in properties.
    props = placement.get("properties") or {}
    mat_path = placement.get("material") or props.get("material")
    if mat_path:
        _apply_material(actor, mat_path, label, warnings)
    tags = list(placement.get("tags") or [])
    try:
        actor.set_editor_property("tags", [unreal.Name(t) for t in tags])
    except Exception:
        pass
    folder = placement.get("folder") or ""
    if folder:
        try:
            actor.set_folder_path(unreal.Name(folder))
        except Exception:
            pass
    # Properties are BEST-EFFORT: a key that can't be set (e.g. a component-level
    # env key or an unknown name) is a soft WARNING, not a hard error, so it never
    # blocks the whole-scene save. Known component keys are routed to their component.
    for k, v in props.items():
        if k == "material":
            continue  # handled above
        try:
            if _route_component_property(actor, k, v):
                continue
            actor.set_editor_property(k, v)
        except Exception as e:
            warnings.append(label + ": property " + str(k) + ": " + str(e))
    return actor, created


def _op_scene_apply(args):
    scene_id = args["scene_id"]
    placements = args.get("placements") or []
    prune = args.get("prune", False)
    save = args.get("save", True)
    tag = "mcp_scene:" + scene_id
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    plan_labels = {p["label"] for p in placements}
    existing = {a.get_actor_label(): a for a in sub.get_all_level_actors() if a}
    spawned, updated, pruned = 0, 0, 0
    missing, errors, warnings = [], [], []
    with _transaction("MCP: scene " + scene_id):
        for p in placements:
            actor, created = _apply_placement(sub, existing, p, missing, errors, warnings)
            if actor is None:
                continue
            if created:
                spawned += 1
                existing[p["label"]] = actor
            else:
                updated += 1
        if prune:
            for label, a in list(existing.items()):
                if label in plan_labels:
                    continue
                try:
                    atags = [str(t) for t in a.get_editor_property("tags")]
                except Exception:
                    atags = []
                if tag in atags:
                    sub.destroy_actor(a)
                    pruned += 1
    # Only HARD errors (spawn/class-resolution failures) block the save; soft
    # per-property warnings do not — a valid scene still persists.
    saved = False
    if save and not errors:
        saved = bool(unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True))
    issues = ([_issue("MISSING_MESH", m.split(" -> ")[0], m) for m in missing]
              + [_issue("SPAWN_ERROR", e.split(":", 1)[0], e) for e in errors]
              + [_issue("PROPERTY_WARNING", w.split(":", 1)[0], w) for w in warnings])
    return {
        "scene_id": scene_id, "spawned": spawned, "updated": updated, "pruned": pruned,
        "missing_meshes": missing, "errors": errors, "warnings": warnings,
        "issues": issues, "saved": saved,
        "actors_after": len(sub.get_all_level_actors()),
    }


def _op_scene_clear(args):
    scene_id = args["scene_id"]
    tag = "mcp_scene:" + scene_id
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    removed = 0
    with _transaction("MCP: clear " + scene_id):
        for a in list(sub.get_all_level_actors()):
            if not a:
                continue
            try:
                atags = [str(t) for t in a.get_editor_property("tags")]
            except Exception:
                atags = []
            if tag in atags:
                sub.destroy_actor(a)
                removed += 1
    saved = False
    if args.get("save", True):
        saved = bool(unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True))
    return {"scene_id": scene_id, "removed": removed, "saved": saved,
            "actors_after": len(sub.get_all_level_actors())}


def _op_scene_bounds(args):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    labels = set(args.get("labels") or [])
    globs = args.get("globs") or []
    actors = []
    for a in sub.get_all_level_actors():
        if not a:
            continue
        lbl = a.get_actor_label()
        if labels and lbl not in labels:
            continue
        if globs and not any(fnmatch.fnmatch(lbl, g) for g in globs):
            continue
        actors.append(a)
    if not labels and not globs:
        actors = [a for a in sub.get_all_level_actors() if a]
    return {"combined": _actor_bounds_aabb(actors), "count": len(actors)}


def _op_design_probe(args):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    histogram, missing, dir_lights, sky_lights, player_starts, nav_bounds = {}, [], [], [], [], []
    for a in sub.get_all_level_actors():
        if not a:
            continue
        cn = a.get_class().get_name()
        histogram[cn] = histogram.get(cn, 0) + 1
        comp = a.get_component_by_class(unreal.StaticMeshComponent)
        if comp is not None and comp.get_editor_property("static_mesh") is None:
            missing.append(a.get_actor_label())
        if isinstance(a, unreal.DirectionalLight):
            dir_lights.append({"label": a.get_actor_label(), "pitch": a.get_actor_rotation().pitch})
        elif isinstance(a, unreal.SkyLight):
            sky_lights.append(a.get_actor_label())
        elif isinstance(a, unreal.PlayerStart):
            player_starts.append(a.get_actor_label())
        elif isinstance(a, unreal.NavMeshBoundsVolume):
            nav_bounds.append(a.get_actor_label())
    return {
        "class_histogram": histogram, "missing_meshes": missing,
        "directional_lights": dir_lights, "sky_lights": sky_lights,
        "player_starts": player_starts, "nav_bounds": nav_bounds,
        "world_aabb": _actor_bounds_aabb([a for a in sub.get_all_level_actors() if a]),
        "lighting_needs_rebuild": None,  # no reliable Lumen-era API from Python
    }


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
    picked = [by_label[l] for l in labels if l in by_label]
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


# --- P2 discovery / world model (cold-start orientation; game-agnostic) ------

def _op_asset_query(args):
    """Query the AssetRegistry with an ARFilter (5.7 class_paths, not deprecated
    class_names). Find assets by class and/or content path without loading them."""
    ar = unreal.AssetRegistryHelpers.get_asset_registry()
    flt = unreal.ARFilter()
    pkgs = args.get("package_paths") or []
    if pkgs:
        flt.set_editor_property("package_paths", [unreal.Name(p) for p in pkgs])
    flt.set_editor_property("recursive_paths", bool(args.get("recursive", True)))
    tlaps = []
    for cp in (args.get("class_paths") or []):
        if isinstance(cp, str) and cp.startswith("/") and "." in cp:
            pkg, obj = cp.rsplit(".", 1)
            try:
                tlaps.append(unreal.TopLevelAssetPath(pkg, obj))
            except Exception:
                pass
    if tlaps:
        flt.set_editor_property("class_paths", tlaps)
        flt.set_editor_property("recursive_classes", bool(args.get("recursive_classes", True)))
    # blueprints=True => find Blueprint assets whose PARENT is in class_paths
    # (get_assets matches an asset's OWN class, and a BP's class is always
    # /Script/Engine.Blueprint, so it can never find "BPs deriving ATurret").
    if args.get("blueprints"):
        assets = unreal.AssetRegistryHelpers.get_blueprint_assets(flt)
    else:
        assets = ar.get_assets(flt)
    limit = int(args.get("limit", 200))
    out = []
    for a in assets[:limit]:
        item = {}
        try:
            item["name"] = str(a.get_editor_property("asset_name"))
        except Exception:
            pass
        try:
            item["package"] = str(a.get_editor_property("package_name"))
        except Exception:
            pass
        try:
            item["class"] = str(a.get_editor_property("asset_class_path").get_editor_property("asset_name"))
        except Exception:
            pass
        try:
            item["path"] = str(a.to_soft_object_path())
        except Exception:
            pass
        out.append(item)
    return {"total": len(assets), "assets": out}


def _op_asset_deps(args):
    ar = unreal.AssetRegistryHelpers.get_asset_registry()
    pkg = str(args["asset"]).split(".")[0]
    # get_dependencies/get_referencers require an options struct (no C++ default);
    # omitting it raises TypeError and silently returns nothing.
    opts = unreal.AssetRegistryDependencyOptions()
    for prop in ("include_hard_package_references", "include_soft_package_references"):
        try:
            opts.set_editor_property(prop, True)
        except Exception:
            pass
    try:
        deps = ar.get_dependencies(unreal.Name(pkg), opts) or []
    except Exception:
        deps = []
    try:
        refs = ar.get_referencers(unreal.Name(pkg), opts) or []
    except Exception:
        refs = []
    return {"asset": pkg, "dependencies": [str(d) for d in deps], "referencers": [str(r) for r in refs]}


def _op_asset_tags(args):
    """Read an asset's registry tags (Blueprint lineage: ParentClass /
    NativeParentClass / GeneratedClass) WITHOUT loading the asset."""
    ar = unreal.AssetRegistryHelpers.get_asset_registry()
    path = args["asset"]
    try:
        data = ar.get_asset_by_object_path(unreal.SoftObjectPath(path))
    except Exception:
        data = None
    if not data or not data.is_valid():
        return {"error": "asset not found: " + str(path), "code": "ASSET_NOT_FOUND"}
    tags = {}
    for key in ("ParentClass", "NativeParentClass", "GeneratedClass", "BlueprintType"):
        try:
            v = data.get_tag_value(key)
            if v:
                tags[key] = str(v)
        except Exception:
            pass
    return {"asset": path, "tags": tags}


def _op_reflect_class(args):
    """Reflect a CLASS contract (its CDO's default property values + callables),
    not a live instance. class_path is /Script/Module.Class or a /Game BP path."""
    path = args["class_path"]
    cls = unreal.load_class(None, path) if path.startswith("/Script/") else None
    if cls is None:
        asset = unreal.load_asset(path)
        cls = asset.generated_class() if isinstance(asset, unreal.Blueprint) else (asset if isinstance(asset, unreal.Class) else None)
    if cls is None:
        return {"error": "class not found: " + str(path), "code": "CLASS_UNRESOLVED"}
    cdo = unreal.get_default_object(cls)
    ref = _reflect_observe(cdo, include=args.get("include"), exclude=args.get("exclude"),
                           properties=args.get("properties"), max_props=int(args.get("max_props", 96)))
    ref["class_path"] = path
    # Parent: unreal.Class has no reliable super accessor from Python; the C++
    # parent is available offline via project_map, and a Blueprint's parent via
    # asset_tags (NativeParentClass/ParentClass).
    ref["parent_hint"] = "use project_map (C++ parent) or asset_tags (Blueprint NativeParentClass)"
    return ref


def _op_enum_values(args):
    """List a UENUM(BlueprintType) or UserDefinedEnum's enumerators. Note: a plain
    UENUM() (not BlueprintType) is not Python-visible and returns NOT_FOUND."""
    path = args["enum_path"]
    if path.startswith("/"):
        enum_cls = unreal.load_object(None, path)
    else:
        enum_cls = getattr(unreal, path.split(".")[-1], None)
    if enum_cls is None:
        return {"error": "enum not found (needs UENUM(BlueprintType) or a UserDefinedEnum): " + str(path),
                "code": "NOT_FOUND"}
    values = []
    try:
        for e in enum_cls:
            values.append({"name": e.name, "value": int(e.value)})
    except Exception as ex:
        return {"error": "could not enumerate " + str(path) + ": " + str(ex), "code": "BAD_VALUE"}
    return {"enum": path, "values": values}


def _op_map_gameplay(args):
    """Discover the gameplay framework: the level's WorldSettings GameMode
    override, the project default GameMode, and (during PIE) the live
    mode/state/controller/pawn classes."""
    result = {}
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    for a in sub.get_all_level_actors():
        if isinstance(a, unreal.WorldSettings):
            try:
                gm = a.get_editor_property("default_game_mode")
                result["world_settings_game_mode"] = gm.get_name() if gm else None
            except Exception:
                pass
            break
    try:
        gms = unreal.get_default_object(unreal.GameMapsSettings)
        result["project_default_game_mode"] = str(gms.get_editor_property("global_default_game_mode"))
    except Exception:
        pass
    world = _game_world()
    if world:
        gm = unreal.GameplayStatics.get_game_mode(world)
        gs = unreal.GameplayStatics.get_game_state(world)
        pc = unreal.GameplayStatics.get_player_controller(world, 0)
        pawn = pc.get_controlled_pawn() if pc else None
        result["live"] = {
            "game_mode": gm.get_class().get_name() if gm else None,
            "game_state": gs.get_class().get_name() if gs else None,
            "player_controller": pc.get_class().get_name() if pc else None,
            "pawn": pawn.get_class().get_name() if pawn else None,
        }
    return result


def _op_find_actors(args):
    """Find live actors by class, optionally filtered by a where={prop:value} map,
    returning selected reflected props. Works in PIE (game world) or the editor."""
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (open a level / start play)", "code": "NOT_IN_PIE"}
    cls = unreal.Actor
    cp = args.get("class_path")
    if cp:
        c = unreal.load_class(None, cp) if cp.startswith("/Script/") else None
        if c is None:
            asset = unreal.load_asset(cp)
            c = asset.generated_class() if isinstance(asset, unreal.Blueprint) else asset
        if c:
            cls = c
    actors = unreal.GameplayStatics.get_all_actors_of_class(world, cls)
    where = args.get("where") or {}
    reflect_props = args.get("reflect") or []
    limit = int(args.get("limit", 100))
    out = []
    for a in actors:
        if where:
            ok = True
            for k, v in where.items():
                try:
                    av = _coerce_prop(a.get_editor_property(k), 256)
                except Exception:
                    ok = False
                    break
                if str(av) != str(v):
                    ok = False
                    break
            if not ok:
                continue
        loc = a.get_actor_location()
        info = {"label": a.get_actor_label(), "class": a.get_class().get_name(),
                "location": [loc.x, loc.y, loc.z]}
        for p in reflect_props:
            try:
                info[p] = _coerce_prop(a.get_editor_property(p), 256)
            except Exception:
                pass
        out.append(info)
        if len(out) >= limit:
            break
    return {"count": len(out), "actors": out}


# --- P3 structured authoring (the Python-feasible 70%: logic in C++, Blueprints
# carry data + component composition; graph/node authoring is plugin-only, P7) --

def _resolve_class(path):
    if not path:
        return None
    if path.startswith("/Script/"):
        return unreal.load_class(None, path)
    asset = unreal.load_asset(path)
    if isinstance(asset, unreal.Blueprint):
        return asset.generated_class()
    return asset if isinstance(asset, unreal.Class) else None


def _maybe_asset(v):
    """Coerce an asset/class PATH string to the loaded UObject so it can be set on
    an object-valued property (e.g. a UStaticMesh* 'HeadMesh'): set_editor_property
    needs the object, not the path. Non-path values pass through unchanged."""
    if isinstance(v, str) and (v.startswith("/Game/") or v.startswith("/Script/")):
        obj = unreal.load_class(None, v) if v.startswith("/Script/") else None
        if obj is None:
            try:
                obj = unreal.load_asset(v)
            except Exception:
                obj = None
        if obj is not None:
            return obj
    return v


def _op_blueprint_create(args):
    parent = args["parent_class_path"]
    dest = args["dest"]
    parent_cls = _resolve_class(parent)
    if not parent_cls:
        return {"error": "parent class not found: " + str(parent), "code": "CLASS_UNRESOLVED"}
    pkg_path, name = dest.rsplit("/", 1)
    factory = unreal.BlueprintFactory()
    factory.set_editor_property("parent_class", parent_cls)
    dt = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, unreal.Blueprint, factory)
    if not dt:
        return {"error": "blueprint create failed", "code": "SPAWN_FAILED"}
    unreal.EditorAssetLibrary.save_asset(dest)
    return {"created": dest, "parent": parent}


def _op_blueprint_set_defaults(args):
    bp_path = args["blueprint"]
    bp = unreal.load_asset(bp_path)
    if not isinstance(bp, unreal.Blueprint):
        return {"error": "not a blueprint: " + str(bp_path), "code": "ASSET_NOT_FOUND"}
    cdo = unreal.get_default_object(bp.generated_class())
    errors = []
    defaults = args.get("defaults") or {}
    for k, v in defaults.items():
        try:
            cdo.set_editor_property(k, _maybe_asset(v))
        except Exception as e:
            errors.append(_issue("PROPERTY_SET_FAILED", k, str(e)))
    unreal.BlueprintEditorLibrary.compile_blueprint(bp)
    unreal.EditorAssetLibrary.save_asset(bp_path)
    return {"blueprint": bp_path, "set": list(defaults.keys()), "errors": errors}


def _op_assign_subclass(args):
    bp = unreal.load_asset(args["target"])
    if not isinstance(bp, unreal.Blueprint):
        return {"error": "target not a blueprint: " + str(args["target"]), "code": "ASSET_NOT_FOUND"}
    cls = _resolve_class(args["class_path"])
    if not cls:
        return {"error": "class not found: " + str(args["class_path"]), "code": "CLASS_UNRESOLVED"}
    cdo = unreal.get_default_object(bp.generated_class())
    try:
        cdo.set_editor_property(args["prop"], cls)
    except Exception as e:
        return {"error": "set " + str(args["prop"]) + ": " + str(e), "code": "PROPERTY_READONLY"}
    unreal.BlueprintEditorLibrary.compile_blueprint(bp)
    unreal.EditorAssetLibrary.save_asset(args["target"])
    return {"target": args["target"], "prop": args["prop"], "class": args["class_path"]}


def _op_blueprint_add_component(args):
    bp = unreal.load_asset(args["blueprint"])
    if not isinstance(bp, unreal.Blueprint):
        return {"error": "not a blueprint", "code": "ASSET_NOT_FOUND"}
    comp_cls = _resolve_class(args["component_class"])
    if not comp_cls:
        return {"error": "component class not found: " + str(args["component_class"]), "code": "CLASS_UNRESOLVED"}
    sub = unreal.get_engine_subsystem(unreal.SubobjectDataSubsystem)
    handles = sub.k2_gather_subobject_data_for_blueprint(bp)
    if not handles:
        return {"error": "no subobject data for blueprint", "code": "EDITOR_ERROR"}
    params = unreal.AddNewSubobjectParams()
    params.set_editor_property("parent_handle", handles[0])
    params.set_editor_property("new_class", comp_cls)
    params.set_editor_property("blueprint_context", bp)
    new_handle, fail = sub.add_new_subobject(params)
    if fail and str(fail):
        return {"error": "add component failed: " + str(fail), "code": "SPAWN_FAILED"}
    name = args.get("name") or ""
    if name:
        try:
            sub.rename_subobject(new_handle, unreal.Text(name))
        except Exception:
            pass
    unreal.SubobjectDataBlueprintFunctionLibrary.get_data(new_handle)  # no-op access to validate handle
    unreal.BlueprintEditorLibrary.compile_blueprint(bp)
    unreal.EditorAssetLibrary.save_asset(args["blueprint"])
    return {"blueprint": args["blueprint"], "component": args["component_class"], "name": name}


def _op_datatable_create(args):
    dest = args["dest"]
    rs = args["row_struct"]
    struct = unreal.load_object(None, rs) if rs.startswith("/Script/") else unreal.load_asset(rs)
    if not struct:
        return {"error": "row struct not found: " + str(rs), "code": "NOT_FOUND"}
    pkg_path, name = dest.rsplit("/", 1)
    factory = unreal.DataTableFactory()
    factory.set_editor_property("struct", struct)
    dt = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, unreal.DataTable, factory)
    if not dt:
        return {"error": "datatable create failed", "code": "SPAWN_FAILED"}
    unreal.EditorAssetLibrary.save_asset(dest)
    return {"created": dest, "row_struct": rs}


def _op_datatable_import(args):
    dt = unreal.load_asset(args["datatable"])
    if not isinstance(dt, unreal.DataTable):
        return {"error": "not a datatable: " + str(args["datatable"]), "code": "ASSET_NOT_FOUND"}
    # fill_data_table_from_{json,csv}_string return a BOOL (True=success), not a
    # problems list; per-row errors are logged by the editor (UE_LOG), not returned.
    ok = True
    if args.get("json"):
        ok = bool(unreal.DataTableFunctionLibrary.fill_data_table_from_json_string(dt, args["json"]))
    elif args.get("csv"):
        ok = bool(unreal.DataTableFunctionLibrary.fill_data_table_from_csv_string(dt, args["csv"]))
    unreal.EditorAssetLibrary.save_asset(args["datatable"])
    if not ok:
        return {"error": "datatable import reported failure (row/column errors in the editor log)",
                "code": "IMPORT_FAILED", "datatable": args["datatable"], "rows": len(dt.get_row_names())}
    return {"datatable": args["datatable"], "imported": True, "rows": len(dt.get_row_names())}


def _op_dataasset_create(args):
    cls = _resolve_class(args["class"])
    if not cls:
        return {"error": "class not found: " + str(args["class"]), "code": "CLASS_UNRESOLVED"}
    pkg_path, name = args["dest"].rsplit("/", 1)
    factory = unreal.DataAssetFactory()
    try:
        factory.set_editor_property("data_asset_class", cls)
    except Exception:
        pass
    da = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, cls, factory)
    if not da:
        return {"error": "dataasset create failed", "code": "SPAWN_FAILED"}
    unreal.EditorAssetLibrary.save_asset(args["dest"])
    return {"created": args["dest"], "class": args["class"]}


# ---------------------------------------------------------------------------
# HUD / UMG authoring — Phase 0a (flat primitive authoring, Python-first).
# Composite (WBP-in-WBP), structured FCompilerResultsLog compile, BindWidget
# enumeration, and FWidgetRenderer capture are Phase 0b/1 (the MCPAuthoring C++
# Editor module). See HUD_TOOLING_PLAN.md. The reflected recipe here (new_object
# for primitives + universal add_child + bare-UPROPERTY setters) is the one the
# plan's Spike 0 validates first; any op a UE point-release stops reflecting
# moves to the C++ subsystem without changing this contract.
# ---------------------------------------------------------------------------

# Friendly widget-class names -> the unreal.* type. Primitive UWidget subclasses
# only; a /Game WBP or UserWidget subclass is a COMPOSITE (Phase 0b, C++).
_WIDGET_CLASSES = {
    "CanvasPanel": "CanvasPanel", "Overlay": "Overlay", "VerticalBox": "VerticalBox",
    "HorizontalBox": "HorizontalBox", "ScrollBox": "ScrollBox", "GridPanel": "GridPanel",
    "UniformGridPanel": "UniformGridPanel", "SizeBox": "SizeBox", "Border": "Border",
    "ScaleBox": "ScaleBox", "WidgetSwitcher": "WidgetSwitcher", "SafeZone": "SafeZone",
    "TextBlock": "TextBlock", "RichTextBlock": "RichTextBlock", "Image": "Image",
    "Button": "Button", "ProgressBar": "ProgressBar", "Slider": "Slider",
    "CheckBox": "CheckBox", "EditableText": "EditableText", "EditableTextBox": "EditableTextBox",
    "Spacer": "Spacer", "NamedSlot": "NamedSlot",
}
# Anchor presets -> (min[x,y], max[x,y], alignment[x,y]) — the single highest-leverage
# layout-correctness lever (coherent anchors+alignment, per the plan §3.b).
_ANCHOR_PRESETS = {
    "TopLeft": ((0, 0), (0, 0), (0, 0)), "TopCenter": ((0.5, 0), (0.5, 0), (0.5, 0)),
    "TopRight": ((1, 0), (1, 0), (1, 0)), "CenterLeft": ((0, 0.5), (0, 0.5), (0, 0.5)),
    "Center": ((0.5, 0.5), (0.5, 0.5), (0.5, 0.5)), "CenterRight": ((1, 0.5), (1, 0.5), (1, 0.5)),
    "BottomLeft": ((0, 1), (0, 1), (0, 1)), "BottomCenter": ((0.5, 1), (0.5, 1), (0.5, 1)),
    "BottomRight": ((1, 1), (1, 1), (1, 1)), "Fill": ((0, 0), (1, 1), (0, 0)),
}


def _widget_prim_class(name):
    """Resolve a widget class name to (unreal_class, is_composite). Friendly names are
    the primitives; anything that loads as a WidgetBlueprint (a /Game WBP) or resolves
    to a WidgetBlueprintGeneratedClass is a COMPOSITE (needs the C++ ConstructWidget
    path). is_child_of is NOT a reflected UFUNCTION in 5.7, so detection is by asset
    type + generated-class name, never by is_child_of."""
    friendly = _WIDGET_CLASSES.get(name)
    if friendly is not None:
        return getattr(unreal, friendly, None), False
    # A /Game path: composite iff the asset is a WidgetBlueprint.
    asset = None
    try:
        asset = unreal.load_asset(name)
    except Exception:
        asset = None
    if isinstance(asset, unreal.WidgetBlueprint):
        return asset.generated_class(), True
    cls = _resolve_class(name)
    if cls is None:
        return None, False
    # A WidgetBlueprintGeneratedClass (name ends _C) or a class whose name flags a
    # UserWidget base is a composite; a bare /Script UWidget subclass is primitive.
    cname = ""
    try:
        cname = str(cls.get_name())
    except Exception:
        cname = ""
    is_composite = cname.endswith("_C") or "UserWidget" in cname
    return cls, is_composite


def _widget_apply_slot(child, slot_body):
    """Apply a slot-class-aware layout body to a child's (already-added) UPanelSlot.
    Must run AFTER add_child (which mints a fresh slot). Returns [issue,...]."""
    issues = []
    slot = child.slot
    if slot is None or not slot_body:
        return issues
    tn = type(slot).__name__
    try:
        if isinstance(slot, unreal.CanvasPanelSlot):
            preset = slot_body.get("anchor_preset")
            if preset and preset in _ANCHOR_PRESETS:
                mn, mx, al = _ANCHOR_PRESETS[preset]
                slot.set_anchors(unreal.Anchors(unreal.Vector2D(*mn), unreal.Vector2D(*mx)))
                slot.set_alignment(unreal.Vector2D(*al))
            elif slot_body.get("anchors"):
                a = slot_body["anchors"]
                slot.set_anchors(unreal.Anchors(unreal.Vector2D(*a[0]), unreal.Vector2D(*a[1])))
            if slot_body.get("alignment") and not preset:
                slot.set_alignment(unreal.Vector2D(*slot_body["alignment"]))
            off = slot_body.get("offsets")
            if off:
                slot.set_offsets(unreal.Margin(off[0], off[1], off[2], off[3]))
            if "z" in slot_body:
                slot.set_z_order(int(slot_body["z"]))
            if slot_body.get("size_to_content"):
                slot.set_auto_size(True)
        elif isinstance(slot, (unreal.HorizontalBoxSlot, unreal.VerticalBoxSlot)):
            sz = slot_body.get("size")
            if sz:
                rule = unreal.SlateSizeRule.FILL if sz.get("fill") or sz.get("value") else unreal.SlateSizeRule.AUTOMATIC
                slot.set_size(unreal.SlateChildSize(value=float(sz.get("value", 1.0)), size_rule=rule))
            _widget_slot_common(slot, slot_body)
        elif isinstance(slot, (unreal.OverlaySlot, unreal.BorderSlot)):
            _widget_slot_common(slot, slot_body)
        elif isinstance(slot, (unreal.GridSlot, unreal.UniformGridSlot)):
            if "row" in slot_body:
                slot.set_row(int(slot_body["row"]))
            if "col" in slot_body:
                slot.set_column(int(slot_body["col"]))
            # Spans exist only on GridSlot; UniformGridSlot cells are single.
            if isinstance(slot, unreal.GridSlot):
                if "row_span" in slot_body:
                    slot.set_row_span(int(slot_body["row_span"]))
                if "col_span" in slot_body:
                    slot.set_column_span(int(slot_body["col_span"]))
        else:
            issues.append(_issue("SLOT_TYPE_UNHANDLED", tn, "no slot recipe for " + tn))
    except Exception as e:
        issues.append(_issue("SLOT_SET_FAILED", tn, str(e)))
    return issues


def _widget_slot_common(slot, body):
    if body.get("padding"):
        p = body["padding"]
        slot.set_padding(unreal.Margin(p[0], p[1], p[2], p[3]) if isinstance(p, list) else unreal.Margin(p, p, p, p))
    if body.get("h_align"):
        slot.set_horizontal_alignment(getattr(unreal.HorizontalAlignment, "H_ALIGN_" + body["h_align"].upper(), unreal.HorizontalAlignment.H_ALIGN_FILL))
    if body.get("v_align"):
        slot.set_vertical_alignment(getattr(unreal.VerticalAlignment, "V_ALIGN_" + body["v_align"].upper(), unreal.VerticalAlignment.V_ALIGN_FILL))


def _widget_apply_props(widget, props):
    """Reflection-set leaf props (Text as FText, colors, sizes, visibility). Partial
    failure: accumulate per-prop issues rather than aborting."""
    issues = []
    for k, v in (props or {}).items():
        try:
            if k == "Text" and isinstance(v, str):
                widget.set_editor_property("text", unreal.Text.from_string(v))
            elif k == "Visibility" and isinstance(v, str):
                widget.set_editor_property("visibility", getattr(unreal.SlateVisibility, v.upper(), unreal.SlateVisibility.VISIBLE))
            else:
                widget.set_editor_property(_snake(k), _maybe_asset(v))
        except Exception as e:
            issues.append(_issue("PROPERTY_SET_FAILED", k, str(e)))
    return issues


def _snake(name):
    """CamelCase -> snake_case for set_editor_property (UMG props are exposed snake)."""
    out = []
    for i, ch in enumerate(name):
        if ch.isupper() and i > 0 and not name[i - 1].isupper():
            out.append("_")
        out.append(ch.lower())
    return "".join(out)


def _op_widget_create(args):
    """Create a WidgetBlueprint shell with a chosen parent class + root panel."""
    pkg_path, name = args["dest"].rsplit("/", 1)
    factory = unreal.WidgetBlueprintFactory()
    parent_path = args.get("parent_class") or "/Script/UMG.UserWidget"
    parent_cls = _resolve_class(parent_path)
    if parent_cls is not None:
        try:
            factory.set_editor_property("parent_class", parent_cls)
        except Exception:
            pass
    wbp = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, unreal.WidgetBlueprint, factory)
    if not wbp:
        return {"error": "widget create failed", "code": "SPAWN_FAILED"}
    root_panel = args.get("root_panel") or "CanvasPanel"
    wt = wbp.get_editor_property("widget_tree")
    root_cls, _ = _widget_prim_class(root_panel)
    root_name = "RootPanel"
    if root_cls is not None and wt.get_editor_property("root_widget") is None:
        root = unreal.new_object(root_cls, outer=wt, name=root_name)
        wt.set_editor_property("root_widget", root)
    unreal.BlueprintEditorLibrary.compile_blueprint(wbp)
    unreal.EditorAssetLibrary.save_asset(args["dest"])
    return {"created": args["dest"], "root": root_name, "parent_class": parent_path,
            "required_bindwidgets": []}  # BindWidget enumeration is Phase 0b (C++)


def _op_widget_compose(args):
    """Phase 0a flat reconcile: build the spec tree of PRIMITIVE nodes, apply
    slots/props (after all adds), interim-compile, and echo the read-back tree +
    digest. Composite (UserWidget) children => COMPOSITE_NEEDS_PLUGIN (Phase 0b)."""
    bp_path = args["blueprint"]
    wbp = unreal.load_asset(bp_path)
    if not isinstance(wbp, unreal.WidgetBlueprint):
        return {"error": "not a WidgetBlueprint: " + str(bp_path), "code": "ASSET_NOT_FOUND"}
    wt = wbp.get_editor_property("widget_tree")
    spec = args.get("tree") or {}
    issues = []
    restore_token = None
    destructive = bool(args.get("prune") or args.get("remove"))
    if destructive:
        restore_token = _widget_snapshot(bp_path, wt)

    # Index existing nodes by name (UWidgetTree.find_widget is NOT reflected in 5.7,
    # so walk root_widget explicitly). This drives adopt/patch + idempotence.
    index = {}
    cur_root = wt.get_editor_property("root_widget")
    if cur_root is not None:
        _widget_index(cur_root, index)

    # PASS 1 — build/adopt every node by name (structure first: adds, no slot yet).
    built = {}
    root = _widget_reconcile_node(wt, None, spec, index, built, issues)
    # Repoint the root whenever the reconciled spec root differs from the current one
    # (widget_create leaves a RootPanel; a spec with its own root must be adopted).
    if root is not None and wt.get_editor_property("root_widget") != root:
        wt.set_editor_property("root_widget", root)

    # PASS 2 — apply slot + props to every node (after all adds mint their slots).
    _widget_apply_all(spec, built, issues)

    # remove / prune (destructive; snapshotted above).
    removed = []
    to_remove = list(args.get("remove") or [])
    if args.get("prune"):
        spec_names = set()
        _widget_spec_names(spec, spec_names)
        for nm in index:
            if nm not in spec_names and nm not in built:
                to_remove.append(nm)
    for nm in to_remove:
        w = built.get(nm) or index.get(nm)
        if w is not None and w != root:
            try:
                w.remove_from_parent()
                removed.append(nm)
            except Exception as e:
                issues.append(_issue("REMOVE_FAILED", nm, str(e)))

    if not args.get("defer"):
        unreal.BlueprintEditorLibrary.compile_blueprint(wbp)
        unreal.EditorAssetLibrary.save_asset(bp_path)
    tree = _widget_canon(wt)
    out = {"blueprint": bp_path, "tree": tree, "digest": _widget_digest(tree),
           "removed": removed, "mode": args.get("mode") or "full",
           "compile_log": {"compiled": not args.get("defer"), "structured": False},  # structured log is 0b
           "issues": issues}
    if restore_token and (removed or destructive):
        out["restore_token"] = restore_token
    return out


def _widget_index(w, out):
    """Recursively map node-name -> UWidget over the live tree (get_children_count/
    get_child_at + get_content are reflected; find_widget is not)."""
    out[str(w.get_name())] = w
    if isinstance(w, unreal.PanelWidget):
        for i in range(w.get_children_count()):
            _widget_index(w.get_child_at(i), out)
    elif isinstance(w, unreal.ContentWidget):
        c = w.get_content()
        if c is not None:
            _widget_index(c, out)


def _widget_spec_names(node, out):
    if node.get("name"):
        out.add(node["name"])
    for c in node.get("children") or []:
        _widget_spec_names(c, out)


def _widget_reconcile_node(wt, parent, node, index, built, issues):
    """Construct or adopt a node by name (from the pre-built index) and add it under
    parent. Returns the UWidget (recurses children). A composite (UserWidget/WBP) child
    needs the MCPAuthoring C++ AddChildWidget path — call it if present, else issue."""
    name = node.get("name")
    cls_name = node.get("class")
    if not name or not cls_name:
        issues.append(_issue("NODE_INVALID", str(name), "every node needs name+class"))
        return None
    cls, is_composite = _widget_prim_class(cls_name)
    if cls is None:
        issues.append(_issue("CLASS_UNRESOLVED", cls_name, "unknown widget class"))
        return None
    existing = index.get(name)
    if existing is not None:
        widget = existing
    elif is_composite:
        # Composite construction is the C++ ConstructWidget path (Phase 0b).
        auth = _mcp_authoring()
        parent_name = str(parent.get_name()) if parent is not None else ""
        if auth is None or parent is None or not auth.add_child_widget(wt.get_outer(), unreal.Name(parent_name), cls, unreal.Name(name), bool(node.get("is_variable"))):
            issues.append(_issue("COMPOSITE_NEEDS_PLUGIN", name, "composite child needs the MCPAuthoring C++ module (Phase 0b) loaded + compiled"))
            return None
        widget = _widget_index_find(wt, name)
        built[name] = widget
        return widget  # composite subtree is opaque; not expanded
    else:
        widget = unreal.new_object(cls, outer=wt, name=unreal.Name(name))
    # Repoint the root the moment the top-level node exists — BEFORE recursing — so a
    # composite child (C++ AddChildWidget resolves its parent via root-anchored
    # FindWidget) can reach a parent that lives in the new subtree.
    if parent is None and wt.get_editor_property("root_widget") != widget:
        wt.set_editor_property("root_widget", widget)
    if node.get("is_variable"):
        try:
            widget.set_editor_property("is_variable", True)
        except Exception as e:
            issues.append(_issue("IS_VARIABLE_NOT_MATERIALIZED", name, str(e)))
    built[name] = widget
    if parent is not None and existing is None:
        try:
            parent.add_child(widget)
        except Exception as e:
            issues.append(_issue("ADD_CHILD_FAILED", name, str(e)))
    for child in node.get("children") or []:
        _widget_reconcile_node(wt, widget, child, index, built, issues)
    return widget


def _mcp_authoring():
    """The MCPAuthoring editor subsystem, or None if the module isn't compiled/loaded."""
    try:
        return unreal.get_editor_subsystem(unreal.MCPAuthoringSubsystem)
    except Exception:
        return None


def _widget_index_find(wt, name):
    idx = {}
    root = wt.get_editor_property("root_widget")
    if root is not None:
        _widget_index(root, idx)
    return idx.get(name)


def _widget_apply_all(node, built, issues):
    w = built.get(node.get("name"))
    if w is not None:
        issues.extend(_widget_apply_props(w, node.get("props")))
        if node.get("slot"):
            issues.extend(_widget_apply_slot(w, node["slot"]))
    for child in node.get("children") or []:
        _widget_apply_all(child, built, issues)


def _widget_canon(wt):
    """Canonical in-memory tree JSON (child order preserved) — the layer-1 oracle."""
    root = wt.get_editor_property("root_widget")
    return _widget_canon_node(root) if root is not None else {}


def _widget_canon_node(w):
    d = {"name": str(w.get_name()), "class": type(w).__name__}
    children = []
    if isinstance(w, unreal.PanelWidget):
        for i in range(w.get_children_count()):
            children.append(_widget_canon_node(w.get_child_at(i)))
    elif isinstance(w, unreal.ContentWidget):
        c = w.get_content()
        if c is not None:
            children.append(_widget_canon_node(c))
    if children:
        d["children"] = children
    return d


def _widget_digest(canon):
    import hashlib
    return hashlib.sha256(json.dumps(canon, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()[:16]


def _widget_snapshot(bp_path, wt):
    """Dump the tree to Saved/MCP/widget_snapshots and return a restore token."""
    import os
    proj = unreal.Paths.project_saved_dir()
    d = os.path.join(proj, "MCP", "widget_snapshots", bp_path.replace("/", "_"))
    os.makedirs(d, exist_ok=True)
    token = str(int(time.time() * 1000))
    with open(os.path.join(d, token + ".json"), "w") as f:
        json.dump(_widget_canon(wt), f)
    return token


def _op_widget_compile(args):
    """Phase 0a interim compile (unstructured pass/fail). The structured
    FCompilerResultsLog log is Phase 0b (C++ widget_compile)."""
    bp_path = args["blueprint"]
    wbp = unreal.load_asset(bp_path)
    if not isinstance(wbp, unreal.WidgetBlueprint):
        return {"error": "not a WidgetBlueprint", "code": "ASSET_NOT_FOUND"}
    unreal.BlueprintEditorLibrary.compile_blueprint(wbp)
    unreal.EditorAssetLibrary.save_asset(bp_path)
    tree = _widget_canon(wbp.get_editor_property("widget_tree"))
    return {"compiled": True, "digest": _widget_digest(tree),
            "compile_log": {"structured": False, "note": "structured log is Phase 0b"}}


def _op_widget_tree(args):
    bp_path = args["blueprint"]
    wbp = unreal.load_asset(bp_path)
    if not isinstance(wbp, unreal.WidgetBlueprint):
        return {"error": "not a WidgetBlueprint", "code": "ASSET_NOT_FOUND"}
    mode = args.get("mode") or "get"
    if mode == "restore":
        return {"error": "restore is not yet implemented in Phase 0a", "code": "NOT_IMPLEMENTED",
                "restore_token": args.get("restore_token")}
    tree = _widget_canon(wbp.get_editor_property("widget_tree"))
    return {"blueprint": bp_path, "tree": tree, "digest": _widget_digest(tree)}


def _op_widget_describe(args):
    """Phase 0a: the authorable palette (friendly names) or a class's reflected
    props via reflect_class. BindWidget/handler enumeration is Phase 0b (C++)."""
    wc = args.get("widget_class")
    if not wc:
        return {"palette": sorted(_WIDGET_CLASSES.keys()),
                "anchor_presets": sorted(_ANCHOR_PRESETS.keys())}
    return _op_reflect_class({"class_path": wc})


def _op_set_world_gamemode(args):
    cls = _resolve_class(args["class_path"])
    if not cls:
        return {"error": "gamemode class not found: " + str(args["class_path"]), "code": "CLASS_UNRESOLVED"}
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    ws = next((a for a in sub.get_all_level_actors() if isinstance(a, unreal.WorldSettings)), None)
    if not ws:
        return {"error": "no WorldSettings actor in the level", "code": "NOT_FOUND"}
    try:
        ws.set_editor_property("default_game_mode", cls)
    except Exception as e:
        return {"error": "set default_game_mode: " + str(e), "code": "PROPERTY_READONLY"}
    unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
    return {"world_game_mode": args["class_path"]}


def _op_pie_set_property(args):
    """Set a property on a LIVE actor in the game world (test preconditions)."""
    world = _game_world()
    if not world:
        return {"error": "not in PIE", "code": "NOT_IN_PIE"}
    label = args["target"]
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor):
        if a.get_actor_label() == label:
            errors = []
            for k, v in (args.get("properties") or {}).items():
                try:
                    a.set_editor_property(k, _maybe_asset(v))
                except Exception as e:
                    errors.append(_issue("PROPERTY_SET_FAILED", k, str(e)))
            return {"target": label, "errors": errors}
    return {"error": "target not found: " + str(label), "code": "TARGET_NOT_FOUND"}


def _op_pie_destroy(args):
    world = _game_world()
    if not world:
        return {"error": "not in PIE", "code": "NOT_IN_PIE"}
    label = args["target"]
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor):
        if a.get_actor_label() == label:
            a.destroy_actor()
            return {"destroyed": label}
    return {"error": "target not found: " + str(label), "code": "TARGET_NOT_FOUND"}


# --- PW instanced-content: HISM/ISM instances (blind to list_actors/counts) --

def _iter_ism_components(world_sel, tag, mesh_filter):
    """Yield (actor, component) for every Instanced/Hierarchical-ISM component,
    optionally filtered by a component tag and/or static-mesh path substring."""
    if world_sel == "game":
        world = _game_world()
        actors = unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor) if world else []
    else:
        actors = unreal.get_editor_subsystem(unreal.EditorActorSubsystem).get_all_level_actors()
    for a in actors:
        if not a:
            continue
        # HISM is a subclass of ISM, so this catches both.
        for comp in a.get_components_by_class(unreal.InstancedStaticMeshComponent):
            if tag:
                try:
                    ctags = [str(t) for t in comp.get_editor_property("component_tags")]
                except Exception:
                    ctags = []
                if tag not in ctags:
                    continue
            if mesh_filter:
                m = comp.get_static_mesh()
                if not m or mesh_filter not in m.get_path_name():
                    continue
            yield a, comp


def _instance_transforms(comp):
    out = []
    mesh = comp.get_static_mesh()
    mesh_path = mesh.get_path_name() if mesh else ""
    n = comp.get_instance_count()
    for i in range(n):
        try:
            res = comp.get_instance_transform(i, True)  # world space
        except Exception:
            continue
        # returns (bool, Transform) or just Transform depending on binding
        xform = res[1] if isinstance(res, tuple) else res
        if xform is None:
            continue
        loc = xform.translation
        rot = xform.rotation.rotator()
        sc = xform.scale3d
        out.append({"mesh": mesh_path, "loc": [loc.x, loc.y, loc.z],
                    "rot": [rot.pitch, rot.yaw, rot.roll], "scale": [sc.x, sc.y, sc.z]})
    return out


def _op_instances_count(args):
    tag = args.get("tag")
    mesh_filter = args.get("mesh")
    world_sel = args.get("world", "editor")
    total = 0
    by_mesh = {}
    for _a, comp in _iter_ism_components(world_sel, tag, mesh_filter):
        n = comp.get_instance_count()
        total += n
        m = comp.get_static_mesh()
        key = m.get_path_name() if m else "(none)"
        by_mesh[key] = by_mesh.get(key, 0) + n
    return {"total": total, "by_mesh": by_mesh}


def _op_instances_list(args):
    tag = args.get("tag")
    mesh_filter = args.get("mesh")
    world_sel = args.get("world", "editor")
    limit = int(args.get("limit", 8192))
    out = []
    for _a, comp in _iter_ism_components(world_sel, tag, mesh_filter):
        for inst in _instance_transforms(comp):
            out.append(inst)
            if len(out) >= limit:
                return {"count": len(out), "instances": out, "truncated": True}
    return {"count": len(out), "instances": out}


def _op_actor_transforms(args):
    """Full transforms (loc/rot_pyr/scale/class) of level actors — the actor-scope
    input for scene_digest (list_actors rounds and omits rotation/scale)."""
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    flt = (args.get("class_filter") or "").lower()
    out = []
    for a in sub.get_all_level_actors():
        if not a:
            continue
        cn = a.get_class().get_name()
        label = a.get_actor_label()
        if flt and flt not in cn.lower() and flt not in label.lower():
            continue
        loc, rot, sc = a.get_actor_location(), a.get_actor_rotation(), a.get_actor_scale3d()
        out.append({"mesh": cn, "label": label, "loc": [loc.x, loc.y, loc.z],
                    "rot": [rot.pitch, rot.yaw, rot.roll], "scale": [sc.x, sc.y, sc.z]})
    return {"count": len(out), "transforms": out}


# --- P5 robustness: liveness, event stream, scene restore -------------------

def _events_path():
    try:
        d = os.path.join(unreal.Paths.project_saved_dir(), "PyMCP")
        os.makedirs(d, exist_ok=True)
        return os.path.join(d, "events.ndjson")
    except Exception:
        return None


def _emit_event(etype, data=None):
    """Append a structured event to Saved/PyMCP/events.ndjson so the Go side can
    observe failures/transitions via a cheap file tail even while the single-flight
    command channel is busy."""
    rec = {"type": etype, "t": time.time()}
    if data:
        rec.update(data)
    # Native push when MCPCore is present (ambient — no-ops if no Go peer is connected).
    # Events are lower-stakes than results, so this is gated on presence, not per-dispatch.
    b = _mcp_cockpit_bridge()
    if b is not None:
        try:
            b.emit_event(etype, json.dumps(rec, default=_jsonable))
        except Exception:
            pass
    # Durable file floor (the uexec tail path + the journal). Kept even on the native path.
    p = _events_path()
    if not p:
        return
    try:
        with open(p, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec) + "\n")
    except Exception:
        pass


def _op_editor_ping(args):
    """Cheap liveness probe (bridge version + PIE state) — a fast heartbeat that
    doesn't touch assets, for health-gating after a rebuild/relaunch."""
    pie = False
    try:
        les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
        pie = bool(les.is_in_play_in_editor())
    except Exception:
        pass
    return {"ok": True, "version": _MCP_BRIDGE_VERSION, "pie": pie, "t": time.time()}


def _op_cockpit_info(args):
    """Native-presence probe (§2.5). If MCPCore is loaded, return its transport coords
    {cockpit_port, session_epoch, token, protocol_version}; otherwise the explicit
    not_present sentinel so the Go backend selector stays on the uexec/Python fallback.
    This is a plain uexec-channel op — it is how Go discovers the native port to dial."""
    b = _mcp_cockpit_bridge()
    if b is None:
        return {"cockpit": "not_present"}
    try:
        return json.loads(b.cockpit_info())
    except Exception:
        return {"cockpit": "not_present"}


def _op_scene_restore(args):
    """Restore actor transforms captured by scene_snapshot (undo an experiment)."""
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    by_label = {a.get_actor_label(): a for a in sub.get_all_level_actors() if a}
    restored, missing = 0, []
    for t in (args.get("transforms") or []):
        a = by_label.get(t.get("label"))
        if not a:
            missing.append(t.get("label"))
            continue
        loc, rot, scale = t.get("loc"), t.get("rot"), t.get("scale")
        if loc:
            a.set_actor_location(unreal.Vector(loc[0], loc[1], loc[2]), False, False)
        if rot:
            a.set_actor_rotation(unreal.Rotator(roll=rot[2], pitch=rot[0], yaw=rot[1]), False)
        if scale:
            a.set_actor_scale3d(unreal.Vector(scale[0], scale[1], scale[2]))
        restored += 1
    unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, False)
    return {"restored": restored, "missing": missing}


# --- P7 input synthesis (plugin-backed: drive the game via injected input) ---

def _op_pie_input(args):
    """Synthesize input into the live game (WASD movement, button taps) via the
    UnrealMCP C++ plugin's control subsystem. Needs the plugin compiled in."""
    world = _game_world()
    if not world:
        return {"error": "not in PIE", "code": "NOT_IN_PIE"}
    ctrl_cls = getattr(unreal, "MCPControlSubsystem", None)
    if ctrl_cls is None:
        return {"error": "MCPControlSubsystem unavailable — rebuild the UnrealMCP plugin", "code": "PLUGIN_MISSING"}
    ctrl = ctrl_cls.get(world)
    if not ctrl:
        return {"error": "no MCPControlSubsystem in the game instance", "code": "PLUGIN_MISSING"}
    key = str(args["key"])
    action = args.get("action", "tap")
    if action == "hold":
        ok = ctrl.hold_key(key, float(args.get("duration_s", 1.0)))
    elif action == "press":
        ok = ctrl.inject_key_by_name(key, True)
    elif action == "release":
        ok = ctrl.inject_key_by_name(key, False)
    elif action == "release_all":
        ctrl.release_all()
        ok = True
    else:
        ok = ctrl.tap_key(key)
    if not ok:
        return {"error": "input rejected (no player controller or invalid key '" + key + "')", "code": "INPUT_FAILED"}
    return {"ok": True, "key": key, "action": action}


# --- P4 spatial verification: nav / trace / overlap queries -----------------

def _op_world_query(args):
    """Spatial queries against the GAME world (needs a built navmesh + collision,
    so run during PIE). kind: line_trace | sphere_overlap | nav_path | project_point."""
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (spatial queries need collision/navmesh — run in PIE)", "code": "NOT_IN_PIE"}
    kind = args.get("kind")
    if kind == "line_trace":
        s, e = args["start"], args["end"]
        hit = unreal.SystemLibrary.line_trace_single(
            world, unreal.Vector(s[0], s[1], s[2]), unreal.Vector(e[0], e[1], e[2]),
            unreal.TraceTypeQuery.TRACE_TYPE_QUERY1, False, [], unreal.DrawDebugTrace.NONE, True)
        if not hit:
            return {"kind": kind, "blocked": False, "clear": True}

        def _hp(n):
            try:
                return hit.get_editor_property(n)
            except Exception:
                return None
        # FHitResult has no HitActor prop in UE5 (it's in HitObjectHandle); reach
        # the actor via the hit Component's owner. get_editor_property raises on a
        # missing name, so every read is guarded.
        comp = _hp("component")
        actor = comp.get_owner() if comp else None
        loc = _hp("location")
        return {"kind": kind, "blocked": True, "clear": False,
                "hit_actor": actor.get_actor_label() if actor else None,
                "hit_location": [loc.x, loc.y, loc.z] if loc else None,
                "distance": _hp("distance")}
    if kind == "sphere_overlap":
        c = args["center"]
        actors = unreal.SystemLibrary.sphere_overlap_actors(
            world, unreal.Vector(c[0], c[1], c[2]), float(args.get("radius", 100.0)),
            [unreal.ObjectTypeQuery.OBJECT_TYPE_QUERY1], None, []) or []
        return {"kind": kind, "count": len(actors),
                "actors": [a.get_actor_label() for a in actors]}
    if kind == "nav_path":
        s, e = args["start"], args["end"]
        nav = unreal.NavigationSystemV1.get_navigation_system(world)
        if not nav:
            return {"error": "no navigation system (build a NavMeshBoundsVolume)", "code": "NOT_FOUND"}
        path = nav.find_path_to_location_synchronously(
            world, unreal.Vector(s[0], s[1], s[2]), unreal.Vector(e[0], e[1], e[2]))
        if not path:
            return {"kind": kind, "path_exists": False}
        try:
            valid = path.is_valid() if hasattr(path, "is_valid") else True
        except Exception:
            valid = True
        # PathPoints is a UPROPERTY (no get_path_points() UFUNCTION exists).
        try:
            points = path.get_editor_property("path_points") or []
        except Exception:
            points = []
        try:
            partial = path.is_partial()
        except Exception:
            partial = None
        return {"kind": kind, "path_exists": bool(valid), "num_points": len(points), "is_partial": partial}
    if kind == "project_point":
        p = args["point"]
        nav = unreal.NavigationSystemV1.get_navigation_system(world)
        if not nav:
            return {"error": "no navigation system", "code": "NOT_FOUND"}
        # NavData/FilterClass have no C++ default -> must be passed (None) explicitly.
        loc = nav.project_point_to_navigation(world, unreal.Vector(p[0], p[1], p[2]), None, None)
        return {"kind": kind, "on_navmesh": loc is not None,
                "projected": [loc.x, loc.y, loc.z] if loc else None}
    return {"error": "unknown query kind: " + str(kind), "code": "BAD_VALUE"}


def _op_console(args):
    """Run an editor/PIE console command (e.g. 'slomo 8' to speed the sim, 'stat
    fps'). During PIE the command is routed through the game world's player
    controller so cheat/exec commands like 'slomo' actually apply TimeDilation
    (a null world context finds no PC and silently no-ops)."""
    cmd = args["command"]
    world = _game_world()
    pc = unreal.GameplayStatics.get_player_controller(world, 0) if world else None
    if pc:
        pc.console_command(cmd)
        return {"ran": cmd, "via": "player_controller"}
    unreal.SystemLibrary.execute_console_command(world, cmd)
    return {"ran": cmd, "via": "world" if world else "editor"}


def _op_editor_state(args):
    les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
    ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    world = ues.get_editor_world()
    info = ues.get_level_viewport_camera_info()
    cam = None
    if info:
        loc, rot = info
        cam = {"location": [loc.x, loc.y, loc.z], "rotation_pyr": [rot.pitch, rot.yaw, rot.roll]}
    sel = sub.get_selected_level_actors()
    return {
        "current_level": world.get_name() if world else None,
        "is_in_pie": les.is_in_play_in_editor(),
        "viewport_camera": cam,
        "selection": {"count": len(sel), "labels": [a.get_actor_label() for a in sel]},
        "actor_count": len(sub.get_all_level_actors()),
        "recorders": [s for s, r in _MCP_RECORDERS.items() if r["running"]],
        "bridge_version": _MCP_BRIDGE_VERSION,
    }


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
    "asset_thumbnail": _op_asset_thumbnail,
    "audio_capture_start": _op_audio_capture_start,
    "audio_capture_stop": _op_audio_capture_stop,
    "play_test_sound": _op_play_test_sound,
    "pawn_state": _op_pawn_state,
    "company_status": _op_company_status,
    "widget_render": _op_widget_render,
    "company_build": _op_company_build,
    "company_select": _op_company_select,
    "asset_reimport": _op_asset_reimport,
    "create_material_instance": _op_create_material_instance,
    # v7 additions
    "reflect_object": _op_reflect_object,
    "capture_start": _op_capture_start,
    "capture_poll": _op_capture_poll,
    "capture_stop": _op_capture_stop,
    "capture_list": _op_capture_list,
    "capture_poses": _op_capture_poses,
    "scene_apply": _op_scene_apply,
    "scene_clear": _op_scene_clear,
    "scene_bounds": _op_scene_bounds,
    "design_probe": _op_design_probe,
    "viewport_set": _op_viewport_set,
    "viewport_get": _op_viewport_get,
    "focus_actors": _op_focus_actors,
    "select_actors": _op_select_actors,
    "get_selection": _op_get_selection,
    "editor_state": _op_editor_state,
    "console": _op_console,
    # P2 discovery
    "asset_query": _op_asset_query,
    "asset_deps": _op_asset_deps,
    "asset_tags": _op_asset_tags,
    "reflect_class": _op_reflect_class,
    "enum_values": _op_enum_values,
    "map_gameplay": _op_map_gameplay,
    "find_actors": _op_find_actors,
    # P3 structured authoring
    "blueprint_create": _op_blueprint_create,
    "blueprint_set_defaults": _op_blueprint_set_defaults,
    "assign_subclass": _op_assign_subclass,
    "blueprint_add_component": _op_blueprint_add_component,
    "datatable_create": _op_datatable_create,
    "datatable_import": _op_datatable_import,
    "dataasset_create": _op_dataasset_create,
    "widget_create": _op_widget_create,
    "widget_compose": _op_widget_compose,
    "widget_compile": _op_widget_compile,
    "widget_tree": _op_widget_tree,
    "widget_describe": _op_widget_describe,
    "set_world_gamemode": _op_set_world_gamemode,
    "pie_set_property": _op_pie_set_property,
    "pie_destroy": _op_pie_destroy,
    # P4 spatial verification
    "world_query": _op_world_query,
    # PW instanced content
    "instances_count": _op_instances_count,
    "instances_list": _op_instances_list,
    "actor_transforms": _op_actor_transforms,
    # P5 robustness
    "editor_ping": _op_editor_ping,
    "cockpit_info": _op_cockpit_info,
    "scene_restore": _op_scene_restore,
    # P7 plugin-backed input synthesis
    "pie_input": _op_pie_input,
}


# Stable error codes an autonomous agent can branch on, instead of regexing a
# raw Python traceback. Only TIMEOUT is retryable at the op layer (the transport
# handles connection loss); everything else is a caller/state fault to fix.
_RETRYABLE_CODES = {"TIMEOUT", "EDITOR_BUSY"}


def _classify_error_message(msg):
    """Classify a failure MESSAGE string (from an in-band op {"error": ...} return
    or an exception) into a stable code. Shared by the exception path and the
    in-band-error promotion so NOT_IN_PIE/CLASS_UNRESOLVED/etc. are actually
    produced by the conditions that name them."""
    low = str(msg).lower()
    if "not in pie" in low or "no game world" in low or "no world" in low:
        return "NOT_IN_PIE"
    if "could not resolve class" in low or ("class" in low and "resolve" in low):
        return "CLASS_UNRESOLVED"
    if "spawn failed" in low or ("spawn" in low and "fail" in low):
        return "SPAWN_FAILED"
    if "asset not found" in low or ("not found" in low and "asset" in low):
        return "ASSET_NOT_FOUND"
    if "target not found" in low or ("not found" in low and "target" in low):
        return "TARGET_NOT_FOUND"
    if ("save" in low and ("fail" in low or "block" in low)):
        return "SAVE_BLOCKED"
    if "read-only" in low or "readonly" in low:
        return "PROPERTY_READONLY"
    if "not found" in low:
        return "NOT_FOUND"
    if "no such session" in low:
        return "NO_SESSION"
    return "EDITOR_ERROR"


def _classify_error(e):
    name = type(e).__name__
    if isinstance(e, KeyError):
        return "MISSING_ARG"
    if isinstance(e, FileNotFoundError):
        return "FILE_NOT_FOUND"
    if name == "AttributeError" or "no attribute" in str(e).lower():
        return "BAD_ATTRIBUTE"
    if name == "TypeError":
        return "BAD_ARGS"
    if name == "ValueError":
        return "BAD_VALUE"
    m = _classify_error_message(e)
    return m if m != "EDITOR_ERROR" else "EDITOR_ERROR"


def _mcp_dispatch(op, b64args):
    try:
        args = json.loads(base64.b64decode(b64args)) if b64args else {}
        fn = _OPS.get(op)
        if fn is None:
            _emit({"ok": False, "error": "unknown op: " + str(op), "code": "UNKNOWN_OP",
                   "retryable": False, "traceback": ""})
            return
        result = fn(args)
        # Promote an in-band failure ({"error": "..."} — the common way ops signal
        # NOT_IN_PIE / CLASS_UNRESOLVED / ASSET_NOT_FOUND / SPAWN_FAILED, etc.) to a
        # coded envelope failure, so every op is machine-branchable, not just ones
        # that raise. (A partial-result "errors"/"warnings" list is NOT this.)
        if isinstance(result, dict) and result.get("error") and "code" not in result:
            code = _classify_error_message(result["error"])
            _emit({"ok": False, "error": str(result["error"]), "code": code,
                   "retryable": code in _RETRYABLE_CODES})
            return
        _emit({"ok": True, "result": result})
    except Exception as e:
        code = _classify_error(e)
        _emit({"ok": False, "error": str(e), "code": code,
               "retryable": code in _RETRYABLE_CODES, "traceback": traceback.format_exc()})


def _mcp_dispatch_native(op, b64args, op_id):
    """Native dispatch entry (Phase B1). MCPCore's game-thread Dispatcher calls this via
    ExecPythonCommandEx. It sets the per-dispatch native sink so the op's single _emit
    routes its result to the framed channel keyed by op_id, then always clears it — so a
    later uexec dispatch on the same interpreter is never mis-routed. MCPCore reconciles:
    if this never reaches _emit (an import/binding failure before the op body), no
    emit_result(op_id) fires and the native side synthesizes EDITOR_EXEC_FAILED (§5.1)."""
    global _MCP_NATIVE_SINK
    _MCP_NATIVE_SINK = op_id
    try:
        _mcp_dispatch(op, b64args)
    finally:
        _MCP_NATIVE_SINK = None
