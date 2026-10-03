# --- P2 discovery / world model (cold-start orientation; game-agnostic) ------

def _op_asset_query(args):
    """Query the AssetRegistry with an ARFilter (5.7 class_paths, not deprecated
    class_names). Find assets by class and/or content path without loading them."""
    ar = unreal.AssetRegistryHelpers.get_asset_registry()
    # ARFilter's fields are read-only on instances in 5.7: build it in one constructor call.
    kw = {"recursive_paths": bool(args.get("recursive", True))}
    pkgs = args.get("package_paths") or []
    if pkgs:
        kw["package_paths"] = [unreal.Name(p) for p in pkgs]
    tlaps = []
    for cp in (args.get("class_paths") or []):
        if isinstance(cp, str) and cp.startswith("/") and "." in cp:
            pkg, obj = cp.rsplit(".", 1)
            try:
                tlaps.append(unreal.TopLevelAssetPath(pkg, obj))
            except Exception:
                pass
    if tlaps:
        kw["class_paths"] = tlaps
        kw["recursive_classes"] = bool(args.get("recursive_classes", True))
    flt = unreal.ARFilter(**kw)
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
        if "package" in item and "name" in item:
            item["path"] = "%s.%s" % (item["package"], item["name"])
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
    if "." not in path.rsplit("/", 1)[-1]:  # a package path: /Game/X/BP_Y -> /Game/X/BP_Y.BP_Y
        path = path + "." + path.rsplit("/", 1)[-1]
    # The registry by package name: EditorAssetLibrary.find_asset_data refuses during PIE,
    # and get_asset_by_object_path cannot take a path built from a string in 5.7 Python.
    pkg, obj = path.rsplit(".", 1)
    data = None
    for ad in ar.get_assets_by_package_name(pkg) or []:  # a registry failure propagates
        if str(ad.get_editor_property("asset_name")) == obj:
            data = ad
            break
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


