# ===========================================================================
# v7 additions — reflection-driven observation, multi-frame capture recorder,
# declarative scene realize + design lint, and tighter editor integration.
# All ops are additive and injection-safe (base64-JSON args via _mcp2_dispatch).
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
        out = [v.x, v.y]
        if hasattr(v, "z"):
            out.append(v.z)
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


