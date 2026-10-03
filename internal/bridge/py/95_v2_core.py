
# ===========================================================================
# v2 core (docs/plans/OVERHAUL_PLAN.md §2.2 R3, §2.7): a strict world vocabulary,
# unambiguous actor and class resolution, and the actor_query / actor_edit /
# actor_call ops behind the v2 tools of the same names. Failures raise _V2Error,
# which _mcp2_dispatch turns into {ok:false, code, error, details}.
# ===========================================================================

_WORLDS = ("editor", "pie", "auto")
_PIE_PREFIX = re.compile(r"UEDPIE_\d+_")


class _V2Error(Exception):
    """A coded op failure: code is a companion code (NOT_FOUND, CONFLICT, BAD_VALUE,
    NOT_IN_PIE, UNSUPPORTED, ...), details a JSON-able dict for the agent."""

    def __init__(self, code, message, **details):
        Exception.__init__(self, message)
        self.code = code
        self.details = details


def _v2_world(args, default):
    """Resolve args["world"] (editor | pie | auto; default per tool) to (world, name).
    Unknown values are an error, never a silent fallback. "auto" means PIE when it is
    running, else the editor world."""
    w = args.get("world") or default
    if w == "game":  # v1 spelling, accepted until the v1 tools are gone
        w = "pie"
    if w not in _WORLDS:
        raise _V2Error("BAD_VALUE", "world must be editor, pie or auto (got %r)" % (w,))
    if w == "editor":
        world, name = _editor_world(), "editor"
    elif w == "pie":
        world, name = _game_world(), "pie"
        if not world:
            raise _V2Error("NOT_IN_PIE", "PIE is not running (start it with the pie tool)")
    else:
        gw = _game_world()
        world, name = (gw, "pie") if gw else (_editor_world(), "editor")
    if not world:
        raise _V2Error("NOT_FOUND", "no %s world is open" % name)
    return world, name


def _world_actors(world, name):
    if name == "editor":
        sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
        return [a for a in sub.get_all_level_actors() if a]
    return [a for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor) if a]


def _norm_path(p):
    """Object path without the PIE package prefix, so an editor path names the same
    actor in PIE (/Game/Maps/UEDPIE_0_L.L:PersistentLevel.A == /Game/Maps/L.L:...)."""
    return _PIE_PREFIX.sub("", str(p))


def _resolve_actor(world, name, ref):
    """Resolve an actor reference: a label, an object path, or (PIE only) @gamestate,
    @pawn or @controller.
    A label matching several actors is a CONFLICT listing them — never first-match."""
    if not ref:
        raise _V2Error("BAD_VALUE", "actor is required")
    if ref == "@gamestate":
        gs = unreal.GameplayStatics.get_game_state(world) if name == "pie" else None
        if not gs:
            raise _V2Error("NOT_FOUND", "@gamestate exists only in PIE")
        return gs
    if ref == "@controller":
        pc = unreal.GameplayStatics.get_player_controller(world, 0) if name == "pie" else None
        if not pc:
            raise _V2Error("NOT_FOUND", "@controller exists only in PIE")
        return pc
    if ref == "@pawn":
        pawn = unreal.GameplayStatics.get_player_pawn(world, 0) if name == "pie" else None
        if not pawn:
            raise _V2Error("NOT_FOUND", "@pawn exists only in PIE (no player pawn)")
        return pawn
    actors = _world_actors(world, name)
    if "/" in ref or ":" in ref:
        want = _norm_path(ref)
        for a in actors:
            if _norm_path(a.get_path_name()) == want:
                return a
        raise _V2Error("NOT_FOUND", "no actor at path %s in the %s world" % (ref, name))
    hits = [a for a in actors if a.get_actor_label() == ref]
    if not hits:
        raise _V2Error("NOT_FOUND", "no actor labeled %r in the %s world" % (ref, name))
    if len(hits) > 1:
        raise _V2Error("CONFLICT", "%d actors are labeled %r; use an object path" % (len(hits), ref),
                       candidates=[a.get_path_name() for a in hits[:20]])
    return hits[0]


_CLASS_MODULES = ("Engine", "CoreUObject", "UMG", "AIModule", "NavigationSystem", "GameplayTags",
                  "EnhancedInput", "Niagara")


def _resolve_class_v2(ref):
    """Resolve a class reference: /Script/Module.Class, a /Game/... Blueprint path
    (or its _C class), or a short name searched in the common engine modules, the
    project module and the project's Blueprints. Ambiguity is a CONFLICT."""
    if not ref:
        raise _V2Error("BAD_VALUE", "class is required")
    if ref.startswith("/Script/") or ref.startswith("/Game/") or ref.startswith("/"):
        cls = _resolve_class(ref)
        if not cls and ref.endswith("_C"):
            cls = _resolve_class(ref[:-2])
        if not cls:
            raise _V2Error("CLASS_UNRESOLVED", "could not resolve class %s" % ref)
        return cls
    base = ref[:-2] if ref.endswith("_C") else ref
    if "." in base:  # Module.Class: any loaded module, incl. plugins
        cls = unreal.find_object(None, "/Script/" + base)
        if not cls:
            raise _V2Error("CLASS_UNRESOLVED", "no loaded class /Script/%s" % base)
        return cls
    found = {}
    modules = list(_CLASS_MODULES)
    try:
        modules.append(unreal.SystemLibrary.get_project_name())
    except Exception:
        pass
    for mod in modules:
        try:
            c = unreal.find_object(None, "/Script/%s.%s" % (mod, base))
        except Exception:
            c = None
        if c is not None and not isinstance(c, unreal.Class):
            c = None
        if c:
            found["/Script/%s.%s" % (mod, base)] = c
    try:
        ar = unreal.AssetRegistryHelpers.get_asset_registry()
        flt = unreal.ARFilter(class_paths=[unreal.TopLevelAssetPath("/Script/Engine", "Blueprint")],
                              package_paths=["/Game"], recursive_paths=True, recursive_classes=True)
        for ad in ar.get_assets(flt):
            if str(ad.asset_name) == base:
                path = str(ad.package_name) + "." + base
                bp = unreal.load_asset(path)
                if isinstance(bp, unreal.Blueprint):
                    found[path] = bp.generated_class()
    except Exception:
        pass
    if not found:
        raise _V2Error("CLASS_UNRESOLVED",
                       "no class named %r in the engine/project modules or /Game Blueprints "
                       "(use Module.Class, /Script/Module.Class or a /Game Blueprint path)" % ref)
    if len(found) > 1:
        raise _V2Error("CONFLICT", "%d classes are named %r" % (len(found), ref), candidates=sorted(found))
    return next(iter(found.values()))


def _vec(v, default):
    if v is None:
        return default
    if not isinstance(v, (list, tuple)) or len(v) != 3:
        raise _V2Error("BAD_VALUE", "expected [x, y, z], got %r" % (v,))
    return [float(x) for x in v]


def _actor_view(a, world_name, detailed=False):
    loc, rot, scale = a.get_actor_location(), a.get_actor_rotation(), a.get_actor_scale3d()
    out = {
        "label": a.get_actor_label(),
        "path": a.get_path_name(),
        "class": a.get_class().get_name(),
        "world": world_name,
        "location": [round(loc.x, 2), round(loc.y, 2), round(loc.z, 2)],
    }
    if detailed:
        out["rotation"] = [rot.pitch, rot.yaw, rot.roll]
        out["scale"] = [scale.x, scale.y, scale.z]
        out["tags"] = [str(t) for t in a.tags]
        out["components"] = [{"name": c.get_name(), "class": c.get_class().get_name()}
                             for c in a.get_components_by_class(unreal.ActorComponent)]
    return out


def _is_a(actor, cls):
    """actor's class is cls or derives from it (UClass.IsChildOf is not reflected to
    Python in 5.7; KismetMathLibrary.ClassIsChildOf is)."""
    return unreal.MathLibrary.class_is_child_of(actor.get_class(), cls)


def _op_actor_query(args):
    world, name = _v2_world(args, "editor")
    op = args.get("op")
    if op == "get":
        return {"world": name, "actor": _actor_view(_resolve_actor(world, name, args.get("actor")), name, True)}
    if op not in ("list", "find"):
        raise _V2Error("BAD_VALUE", "actor_query op must be list, get or find")
    flt = (args.get("filter") or "").lower()
    cls = _resolve_class_v2(args["class"]) if args.get("class") else None
    where = args.get("where") or {}
    props = args.get("properties") or []
    limit = int(args.get("limit") or 200)
    out, total = [], 0
    for a in _world_actors(world, name):
        label = a.get_actor_label()
        if flt and flt not in label.lower() and flt not in a.get_class().get_name().lower():
            continue
        if cls and not _is_a(a, cls):
            continue
        if where:
            ok = True
            for k, v in where.items():
                try:
                    if str(_coerce_prop(a.get_editor_property(k), 256)) != str(v):
                        ok = False
                        break
                except Exception:
                    ok = False
                    break
            if not ok:
                continue
        total += 1
        if len(out) >= limit:
            continue
        info = _actor_view(a, name)
        for p in props:
            try:
                info[p] = _coerce_prop(a.get_editor_property(p), 256)
            except Exception:
                info[p] = None
        out.append(info)
    return {"world": name, "count": total, "returned": len(out), "truncated": total > len(out), "actors": out}


def _set_props(obj, props):
    errors = []
    for k, v in (props or {}).items():
        try:
            obj.set_editor_property(k, _maybe_asset(v))
        except Exception as e:
            errors.append({"property": k, "error": str(e)})
    return errors


def _edit_world(args):
    w = args.get("world")
    if w not in ("editor", "pie"):
        raise _V2Error("BAD_VALUE", "actor edits require an explicit world: editor or pie (got %r)" % (w,))
    return _v2_world(args, None)


@contextlib.contextmanager
def _undoable(name, label, *actors):
    """Editor edits run inside one named undo transaction with the touched actors
    snapshotted (modify()); PIE edits are transient and need neither."""
    if name != "editor":
        yield
        return
    with _transaction("MCP: " + label):
        for a in actors:
            a.modify()
        yield


def _op_actor_spawn(args):
    world, name = _edit_world(args)
    if name != "editor":
        raise _V2Error("UNSUPPORTED", "spawning into PIE is not supported in v2.0; spawn in the editor world")
    cls = _resolve_class_v2(args.get("class"))
    loc = _vec(args.get("location"), [0.0, 0.0, 100.0])
    rot = _vec(args.get("rotation"), [0.0, 0.0, 0.0])
    scale = _vec(args.get("scale"), None) if args.get("scale") else None
    mesh = None
    if args.get("static_mesh"):
        mesh = unreal.load_asset(args["static_mesh"])
        if not mesh:
            raise _V2Error("NOT_FOUND", "static_mesh %s did not load" % args["static_mesh"])
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    with _undoable(name, "spawn " + str(args.get("class"))):
        actor = sub.spawn_actor_from_class(cls, unreal.Vector(*loc), unreal.Rotator(rot[2], rot[0], rot[1]))
        if not actor:
            raise _V2Error("SPAWN_FAILED", "spawn failed for %s" % args.get("class"))
        try:
            if args.get("label"):
                actor.set_actor_label(args["label"])
            if scale:
                actor.set_actor_scale3d(unreal.Vector(*scale))
            if mesh is not None:
                comp = actor.get_component_by_class(unreal.StaticMeshComponent)
                if not comp:
                    raise _V2Error("BAD_VALUE", "%s has no StaticMeshComponent for static_mesh" % args.get("class"))
                comp.set_static_mesh(mesh)
            errors = _set_props(actor, args.get("properties"))
        except Exception:
            sub.destroy_actor(actor)  # a failed spawn leaves nothing behind
            raise
    return {"world": name, "spawned": _actor_view(actor, name, True), "property_errors": errors}


def _op_actor_delete(args):
    world, name = _edit_world(args)
    actor = _resolve_actor(world, name, args.get("actor"))
    view = _actor_view(actor, name)
    if name == "editor":
        with _undoable(name, "delete " + view["label"], actor):
            unreal.get_editor_subsystem(unreal.EditorActorSubsystem).destroy_actor(actor)
    else:
        actor.destroy_actor()
    return {"world": name, "deleted": view}


def _op_actor_transform(args):
    world, name = _edit_world(args)
    actor = _resolve_actor(world, name, args.get("actor"))
    loc, rot, scale = args.get("location"), args.get("rotation"), args.get("scale")
    if loc is None and rot is None and scale is None:
        raise _V2Error("BAD_VALUE", "transform needs location, rotation and/or scale")
    root = actor.get_editor_property("root_component")
    mobility = None
    if (name == "pie" and root is not None and (loc is not None or rot is not None)
            and root.get_editor_property("mobility") == unreal.ComponentMobility.STATIC):
        # A Static root ignores moves in a game world: the PIE copy is made Movable
        # (discarded on stop) instead of reporting a move that did not happen.
        root.set_mobility(unreal.ComponentMobility.MOVABLE)
        mobility = "static->movable (pie copy only)"
    with _undoable(name, "transform " + actor.get_actor_label(), actor):
        if loc is not None:
            actor.set_actor_location(unreal.Vector(*_vec(loc, None)), False, False)
            # Verified before rotation/scale change: a failure means nothing was changed.
            got = actor.get_actor_location()
            want = _vec(loc, None)
            if max(abs(got.x - want[0]), abs(got.y - want[1]), abs(got.z - want[2])) > 0.5:
                raise _V2Error("EDITOR_ERROR", "the actor did not reach the location; it is now at [%g, %g, %g] (attached "
                               "or constrained?); rotation and scale were not applied" % (got.x, got.y, got.z))
        if rot is not None:
            r = _vec(rot, None)  # [pitch, yaw, roll] -> Rotator(roll, pitch, yaw)
            actor.set_actor_rotation(unreal.Rotator(r[2], r[0], r[1]), False)
        if scale is not None:
            actor.set_actor_scale3d(unreal.Vector(*_vec(scale, None)))
    out = {"world": name, "actor": _actor_view(actor, name, True)}
    if mobility:
        out["mobility"] = mobility
    return out


def _op_actor_set_properties(args):
    world, name = _edit_world(args)
    actor = _resolve_actor(world, name, args.get("actor"))
    if not args.get("properties"):
        raise _V2Error("BAD_VALUE", "set_properties needs a properties map")
    with _undoable(name, "set properties on " + actor.get_actor_label(), actor):
        errors = _set_props(actor, args["properties"])
    if len(errors) == len(args["properties"]):
        raise _V2Error("BAD_VALUE", "no property was set", property_errors=errors)
    return {"world": name, "actor": _actor_view(actor, name), "property_errors": errors}


def _op_actor_call(args):
    world, name = _v2_world(args, "pie")
    if name != "pie":
        raise _V2Error("UNSUPPORTED", "actor_call runs in PIE only in v2.0 (CallInEditor functions are not supported)")
    target = _resolve_actor(world, name, args.get("actor"))
    fn = args.get("function")
    if not fn:
        raise _V2Error("BAD_VALUE", "function is required")
    fargs = args.get("args") or {}
    if not isinstance(fargs, dict):
        raise _V2Error("BAD_VALUE", "args must be an object of parameter name -> value")
    try:
        result = target.call_method(fn, kwargs=fargs)
    except Exception as e:
        if "find function" in str(e).lower() or "no function" in str(e).lower():
            raise _V2Error("NOT_FOUND", "%s has no callable function %r" % (target.get_name(), fn)) from e
        raise
    return {"world": name, "actor": target.get_actor_label() if hasattr(target, "get_actor_label") else str(target),
            "function": fn, "result": _coerce_prop(result, 2048) if result is not None else None}
