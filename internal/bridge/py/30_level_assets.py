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
    clean = args.get("clean_slate", False)  # v1 defaulted True and wiped the level
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
        return {"error": "asset not found: " + str(path), "code": "ASSET_NOT_FOUND"}
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
        return {"error": "asset not found: " + str(path), "code": "ASSET_NOT_FOUND"}
    if not isinstance(mesh, unreal.StaticMesh):
        return {"error": "thumbnails support StaticMesh only; got " + mesh.get_class().get_name(), "code": "UNSUPPORTED"}

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
            "has_road": _try_bool(b, "b_has_road", "has_road", default=True),
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


def _try_bool(obj, *names, default=False):
    for n in names:
        try:
            return bool(obj.get_editor_property(n))
        except Exception:
            continue
    return default


def _op_company_road(args):
    # Drag-build a road line start->end (X-first L), clamped to the affordable/unblocked
    # prefix. Cells are grid coords. Returns {placed, capital, road_cells}.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE)"}
    s = args.get("start", [0, 0])
    e = args.get("end", [0, 0])
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.CompanyManager):
        placed = a.place_road_line(unreal.IntPoint(int(s[0]), int(s[1])), unreal.IntPoint(int(e[0]), int(e[1])))
        return {"placed": int(placed), "capital": int(a.get_editor_property("capital")),
                "road_cells": len(a.get_editor_property("road_cells"))}
    return {"error": "no CompanyManager"}


def _op_company_demolish(args):
    # Bulldoze the building nearest a world location: refund half its catalog cost + destroy
    # it (EndPlay frees its grid cells). Mirrors the controller's bulldoze click.
    world = _pick_world(args.get("world", "auto"))
    if not world:
        return {"error": "no world (start PIE)"}
    loc = args.get("location", [0, 0, 0])
    target = unreal.Vector(float(loc[0]), float(loc[1]), float(loc[2]))
    best, bd = None, 1e12
    for b in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.ProductionBuilding):
        p = b.get_actor_location()
        d = ((p.x - target.x) ** 2 + (p.y - target.y) ** 2) ** 0.5
        if d < bd:
            bd, best = d, b
    n = len(list(unreal.GameplayStatics.get_all_actors_of_class(world, unreal.ProductionBuilding)))
    if not best or bd > 2400:
        buildings = unreal.GameplayStatics.get_all_actors_of_class(world, unreal.ProductionBuilding)
        bl = [[round(b.get_actor_location().x), round(b.get_actor_location().y)] for b in buildings]
        return {"demolished": False, "count": n, "nearest": round(bd, 1), "target": [round(target.x), round(target.y)], "locs": bl}
    name = str(best.get_editor_property("building_name"))
    refund = int(best.get_editor_property("build_cost")) // 2  # stored on the building
    cap = None
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.CompanyManager):
        a.add_capital(refund)
        cap = int(a.get_editor_property("capital"))
        break
    best.destroy_actor()
    return {"demolished": True, "name": name, "refund": refund, "capital": cap}


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


