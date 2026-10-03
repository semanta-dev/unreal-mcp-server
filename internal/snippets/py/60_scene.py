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


