
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


def _plugin_api():
    """The UnrealMCP plugin API version compiled into this editor: the handshake
    (UMCPCoreLibrary.GetPluginApiVersion, API 3+); 2 for a plugin built before the
    handshake (its subsystems exist, the library does not); 0 without the plugin."""
    lib = getattr(unreal, "MCPCoreLibrary", None)
    if lib is not None and hasattr(lib, "get_plugin_api_version"):
        return int(lib.get_plugin_api_version())
    for cls in ("MCPControlSubsystem", "MCPCaptureSubsystem", "MCPAuthoringSubsystem", "MCPCockpitBridge"):
        if getattr(unreal, cls, None) is not None:
            return 2
    return 0


def _need_plugin(api, feature):
    """Fail unless the editor's plugin offers API >= api (Needs plugin>=N): a stale
    or missing plugin is PLUGIN_MISSING with the versions, never a fallback."""
    have = _plugin_api()
    if have < api:
        raise _V2Error("PLUGIN_MISSING", "%s needs the UnrealMCP plugin API %d; this editor has %s — copy plugin/UnrealMCP "
                       "into <project>/Plugins and rebuild (build strategy=ubt)" % (feature, api, have or "no plugin"),
                       needed=api, have=have)
    return unreal.MCPCoreLibrary


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


def _is_object_ref(ref):
    return isinstance(ref, str) and (ref in ("@gameinstance", "@hud") or ref.startswith("@playerstate")
                                     or ref.startswith("@subsystem:"))


def _resolve_object(world, name, ref, editor_subsystems=False):
    """Resolve an object reference — @gameinstance, @playerstate[:n], @hud (PIE) or
    @subsystem:<Class> — or, for anything else, an actor reference (_resolve_actor).
    World / GameInstance / LocalPlayer subsystems are found through the plugin (UE 5.7's
    Python has no accessor for them). Editor and engine subsystems are reachable only
    when editor_subsystems is set — the read-only reflect op; never from a call."""
    if not _is_object_ref(ref):
        return _resolve_actor(world, name, ref)
    if ref.startswith("@subsystem:"):
        return _resolve_subsystem(world, name, ref[len("@subsystem:"):], editor_subsystems)
    if name != "pie":
        raise _V2Error("NOT_FOUND", "%s exists only in PIE" % ref)
    if ref == "@gameinstance":
        obj = unreal.GameplayStatics.get_game_instance(world)
    elif ref == "@hud":
        pc = unreal.GameplayStatics.get_player_controller(world, 0)
        obj = pc.get_hud() if pc else None
    else:
        _, _, idx = ref.partition(":")
        try:
            i = int(idx or 0)
        except ValueError:
            raise _V2Error("BAD_VALUE", "@playerstate takes an index: @playerstate or @playerstate:<n> (got %r)" % ref) from None
        obj = unreal.GameplayStatics.get_player_state(world, i)
    if not obj:
        raise _V2Error("NOT_FOUND", "%s: none in the running game" % ref)
    return obj


def _resolve_subsystem(world, name, cls_ref, editor_subsystems):
    if not cls_ref:
        raise _V2Error("BAD_VALUE", "@subsystem:<Class> needs a class")
    cls = _resolve_class_v2(cls_ref)
    is_child = unreal.MathLibrary.class_is_child_of
    for base in ("EditorSubsystem", "EngineSubsystem"):
        bcls = getattr(unreal, base, None)
        if bcls is not None and is_child(cls, bcls.static_class()):
            if not editor_subsystems:
                raise _V2Error("BAD_VALUE", "%s is an %s: reachable only from reflect (read-only), not from calls"
                               % (cls.get_name(), base))
            pytype = getattr(unreal, cls.get_name(), None)
            getter = unreal.get_editor_subsystem if base == "EditorSubsystem" else unreal.get_engine_subsystem
            obj = getter(pytype) if pytype is not None else None
            if not obj:
                raise _V2Error("NOT_FOUND", "no %s instance" % cls.get_name())
            return obj
    lib = _need_plugin(3, "@subsystem")
    obj = lib.find_game_subsystem(world, cls)
    if not obj:
        raise _V2Error("NOT_FOUND", "no %s in the %s world (World, GameInstance and LocalPlayer subsystems are "
                       "supported; GameInstance/LocalPlayer ones exist only in PIE)" % (cls.get_name(), name))
    return obj


_OBJ_SEG = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*)(\(\))?$|^([0-9]+)$")


def _observe_path(world, name, path, lib):
    """Read one object path: "@ref.prop.Getter().field". Properties are read by
    reflection; a getter is called only if it is BlueprintPure or const (checked by the
    plugin — Python cannot read function flags); a string a getter returns is decoded as
    JSON when the path continues into it."""
    # The object ref ends before the first property: "@subsystem:/Script/Mod.Class.prop"
    # has a dot inside the class path, so split after the class, not at the first dot.
    if path.startswith("@subsystem:/Script/"):
        m = re.match(r"^(@subsystem:/Script/\w+\.\w+)\.(.*)$", path)
    else:
        m = re.match(r"^(@[A-Za-z_]+(?::\w+)?)\.(.*)$", path)
        # "@subsystem:Module.Class.prop" (the game_api spelling): Module.Class when that
        # names a loaded class, else a short class name followed by a property.
        mm = re.match(r"^@subsystem:(\w+)\.(\w+)\.(.+)$", path)
        if mm and isinstance(unreal.find_object(None, "/Script/%s.%s" % (mm.group(1), mm.group(2))), unreal.Class):
            m = re.match(r"^(@subsystem:\w+\.\w+)\.(.*)$", path)
    head, rest = (m.group(1), m.group(2)) if m else (path, "")
    if not rest:
        raise _V2Error("BAD_VALUE", "%s: an object path needs a property after the object" % path)
    cur = _resolve_object(world, name, head)
    segs = rest.split(".")
    for k, seg in enumerate(segs):
        m = _OBJ_SEG.match(seg)
        if not m:
            raise _V2Error("BAD_VALUE", "%s: bad path segment %r" % (path, seg))
        if m.group(3) is not None:  # an index into a list
            if not isinstance(cur, (list, tuple)):
                raise _V2Error("BAD_VALUE", "%s: %s indexes a non-list" % (path, seg))
            i = int(m.group(3))
            cur = cur[i] if i < len(cur) else None
        elif isinstance(cur, dict):
            if m.group(2):
                raise _V2Error("BAD_VALUE", "%s: %s() on a JSON value" % (path, m.group(1)))
            cur = cur.get(m.group(1))
        elif m.group(2):  # a getter
            fn = m.group(1)
            if not hasattr(cur, "get_class") or not hasattr(cur, "call_method"):
                raise _V2Error("BAD_VALUE", "%s: %s() on a value that is not an object" % (path, fn))
            if lib is None:
                lib = _need_plugin(3, "getters in object paths")
            if not lib.is_pure_or_const(cur.get_class(), fn):
                raise _V2Error("BAD_VALUE", "%s: %s.%s is not BlueprintPure or const — a wait may only call "
                               "read-only getters (use actor_call for anything else)" % (path, cur.get_class().get_name(), fn))
            cur = cur.call_method(fn)
            if isinstance(cur, str) and k < len(segs) - 1:
                try:
                    cur = json.loads(cur)
                except ValueError:
                    raise _V2Error("BAD_VALUE", "%s: %s() returned a string that is not JSON, and the path continues "
                                   "into it" % (path, fn)) from None
        else:
            try:
                cur = cur.get_editor_property(m.group(1))
            except Exception as e:
                raise _V2Error("NOT_FOUND", "%s: no property %s on %s (%s)" % (path, m.group(1), cur.get_name(), e)) from None
        if cur is None:
            return None
    return cur if isinstance(cur, (dict, list, str, int, float, bool)) else _coerce_prop(cur, 512)


def _op_observe_paths(args):
    """Read object paths for a predicate (pie_wait, actor_call until, wait_until beats):
    {values: {path: value}, errors: {path: message}}. Read-only: getters must be pure."""
    paths = args.get("paths") or []
    world, name = _v2_world(args, "auto")
    # The plugin is needed for @subsystem refs and getters; plain properties of
    # @gameinstance / @playerstate / @hud are read by Python alone.
    lib = _need_plugin(3, "object paths in predicates") if any("@subsystem:" in p or "()" in p for p in paths) else None
    values, errors = {}, {}
    for p in paths:
        try:
            values[p] = _observe_path(world, name, p, lib)
        except _V2Error as e:
            if e.code in ("BAD_VALUE", "CLASS_UNRESOLVED", "CONFLICT"):
                raise  # a malformed, unresolvable or non-pure path is the caller's error, not "not yet"
            errors[p] = str(e)
    return {"world": name, "values": values, "errors": errors}


def _game_api_object(args):
    """The game_api subsystem in the running game (PIE)."""
    world, name = _v2_world({"world": "pie"}, "pie")
    return _resolve_subsystem(world, name, args.get("class") or "", False)


def _game_json(fn, raw):
    if not isinstance(raw, str):
        raise _V2Error("BAD_VALUE", "%s returned %s, not a JSON string" % (fn, type(raw).__name__))
    try:
        return json.loads(raw)
    except ValueError as e:
        raise _V2Error("BAD_VALUE", "%s did not return JSON (%s)" % (fn, e), head=raw[:200]) from None


def _op_game_read(args):
    """Call a game_api read function (capabilities / snapshot / events). It must be
    BlueprintPure or const — checked by the plugin, never assumed."""
    lib = _need_plugin(3, "the game API")
    obj = _game_api_object(args)
    fn = args.get("function") or ""
    if not lib.is_pure_or_const(obj.get_class(), fn):
        raise _V2Error("BAD_VALUE", "%s.%s is not BlueprintPure or const: game_api read functions must be read-only"
                       % (obj.get_class().get_name(), fn))
    raw = obj.call_method(fn, args=tuple(args.get("args") or ()))
    return {"result": _game_json(fn, raw)}


def _op_game_command(args):
    """Call the game_api command function with one request (JSON with command,
    request_id, world_epoch). The game deduplicates request_id and refuses another
    world's epoch; this op never retries."""
    _need_plugin(3, "the game API")
    obj = _game_api_object(args)
    fn = args.get("function") or ""
    raw = obj.call_method(fn, args=(args.get("request") or "",))
    return {"result": _game_json(fn, raw)}


def _object_view(obj, world_name):
    """An actor's view, or — for a non-actor object — its identity (no transform)."""
    if hasattr(obj, "get_actor_label"):
        return _actor_view(obj, world_name, True)
    return {"name": obj.get_name(), "path": obj.get_path_name(), "class": obj.get_class().get_name(),
            "kind": "object", "world": world_name}


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
            hint = ""
            short = ref.rsplit(".", 1)[-1] if ref.startswith("/Script/") else ""
            pytype = getattr(unreal, short, None) if short else None
            if isinstance(pytype, type) and hasattr(pytype, "static_class"):
                try:
                    hint = ": did you mean %s?" % pytype.static_class().get_path_name()
                except Exception:
                    hint = ""
            raise _V2Error("CLASS_UNRESOLVED", "could not resolve class %s%s" % (ref, hint))
        return cls
    base = ref[:-2] if ref.endswith("_C") else ref
    if "." in base:  # Module.Class: any loaded module, incl. plugins
        cls = unreal.find_object(None, "/Script/" + base)
        if not cls:
            raise _V2Error("CLASS_UNRESOLVED", "no loaded class /Script/%s" % base)
        return cls
    found = {}
    # Any loaded native class is exposed on the unreal module by its short name (the
    # project's and plugins' modules included, whatever they are called).
    pytype = getattr(unreal, base, None)
    if isinstance(pytype, type) and hasattr(pytype, "static_class"):
        try:
            c = pytype.static_class()
        except Exception:
            c = None
        if isinstance(c, unreal.Class) and c.get_path_name().startswith("/Script/"):
            found[c.get_path_name()] = c
    for mod in _CLASS_MODULES:
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
        return {"world": name, "actor": _object_view(_resolve_object(world, name, args.get("actor")), name)}
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
            _set_prop_merged(obj, k, v)
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


_MCP_TX_PREFIX = "MCP: "  # every server edit's transaction title starts with it (_undoable, _transaction)

# The server's editor edits since this module loaded, oldest first: ("tx", title) for an
# edit that made an undo step, ("untracked", op) for one that did not. undo steps only
# through "tx" entries: undoing past an untracked edit would revert an older edit
# underneath it (and the undo buffer cannot see the untracked one).
_MCP_EDITS = []
_MCP_REDO = []

# Ops that change the editor without an undo transaction (asset edits, imports, game
# commands, console and recipes). World ops count only outside PIE (in PIE they change
# the transient game world, not the level). Kept equal to spec.UndoClass by a Go test.
_UNTRACKED_EDIT_OPS = frozenset(("asset_create", "asset_edit", "asset_reimport", "import_assets", "datatable_import",
                                 "widget_compose", "widget_compile", "set_world_gamemode", "live_coding_compile",
                                 "data_add_variable", "data_input_mapping", "data_set_settings", "widget_bind"))
# The ops whose dry_run is implemented (an op that ignored the flag would edit unjournaled).
_DRY_RUN_OPS = frozenset(("asset_create", "data_set_properties", "data_table_upsert", "data_table_delete",
                          "data_add_variable", "data_input_mapping"))
_UNTRACKED_WORLD_OPS = frozenset(("company_build", "company_road", "company_demolish", "company_select", "console",
                                  "apply_level_recipe"))


def _note_edit(kind, what):
    _MCP_EDITS.append((kind, what))
    del _MCP_EDITS[:-256]
    del _MCP_REDO[:]


def _note_op(op, args):
    """Record a successful op in the edit journal (called by the dispatcher)."""
    if op in ("open_level", "level_revert"):
        del _MCP_EDITS[:]  # a new map starts a new undo buffer
        del _MCP_REDO[:]
    elif args.get("dry_run") and op in _DRY_RUN_OPS:
        pass  # checked, changed nothing
    elif op in _UNTRACKED_EDIT_OPS or (op in _UNTRACKED_WORLD_OPS and not _pie_running()):
        _note_edit("untracked", op)


def _op_note_edit(args):
    """The server reports an untracked edit made outside the companion's ops (the python
    tool runs code directly)."""
    _note_edit("untracked", str(args.get("op") or "python"))
    return {"noted": True}


def _op_editor_undo(args):
    """Undo (or redo) the editor's next transaction — only when the server made it:
    the undo buffer is shared with the human, so a step not titled "MCP: " is a
    CONFLICT and nothing changes. The title check and the step are one plugin call
    on the game thread (no edit can land between them); PIE refuses it."""
    redo = bool(args.get("redo"))
    lib = _need_plugin(3, "undo")
    verb = "redo" if redo else "undo"
    journal, other = (_MCP_REDO, _MCP_EDITS) if redo else (_MCP_EDITS, _MCP_REDO)
    expected = _MCP_TX_PREFIX
    if journal:
        kind, what = journal[-1]
        if kind != "tx":
            raise _V2Error("CONFLICT", "the server's last edit (%s) has no undo step: %s now would revert an older edit "
                           "underneath it — roll back with snapshot_restore or git_revert instead" % (what, verb),
                           untracked=what)
        expected = what  # the exact step the server made last: a hand-made undo/redo shows as a mismatch
    raw = lib.redo_if_titled(expected) if redo else lib.undo_if_titled(expected)
    res = json.loads(raw)
    title = res.get("title", "")
    if res.get("ok"):
        if journal:
            other.append(journal.pop())
        return {("redone" if redo else "undone"): title}
    reason = res.get("reason")
    if reason == "transaction_active":
        raise _V2Error("EDITOR_BUSY", "an editor transaction is in progress (a drag or an edit in a dialog): try again "
                       "when it ends", reason="transaction_active")
    if reason == "pie":
        raise _V2Error("PRECONDITION", "%s is refused while PIE runs (it would rewind the editor world under the game): "
                       "stop PIE first" % verb, reason="pie")
    if reason == "empty":
        raise _V2Error("PRECONDITION", "nothing to %s" % verb, reason="empty")
    if reason == "title_mismatch":
        raise _V2Error("CONFLICT", "the next %s step is not the server's (%r): it was not changed — undo it by hand, "
                       "or roll back with snapshot_restore / git_revert" % (verb, title), title=title)
    raise _V2Error("EDITOR_ERROR", "%s of %r failed" % (verb, title), title=title)


def _op_actor_spawn(args):
    world, name = _edit_world(args)
    if name == "pie":
        _need_plugin(5, "spawning into PIE")
    cls = _resolve_class_v2(args.get("class"))
    loc = _vec(args.get("location"), [0.0, 0.0, 100.0])
    rot = _vec(args.get("rotation"), [0.0, 0.0, 0.0])
    scale = _vec(args.get("scale"), None) if args.get("scale") else None
    mesh = None
    if args.get("static_mesh"):
        mesh = unreal.load_asset(args["static_mesh"])
        if not mesh:
            raise _V2Error("NOT_FOUND", "static_mesh %s did not load" % args["static_mesh"])
    if name == "pie":
        # Python can spawn only into the editor world (spike row 10): the plugin's
        # control subsystem spawns into the running game (gone when PIE stops).
        _, ctrl = _pie_control()
        spawn, destroy = ctrl.spawn_in_game, (lambda a: a.destroy_actor())
    else:
        sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
        spawn, destroy = sub.spawn_actor_from_class, sub.destroy_actor
    with _undoable(name, "spawn " + str(args.get("class"))):
        actor = spawn(cls, unreal.Vector(*loc), unreal.Rotator(rot[2], rot[0], rot[1]))
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
            destroy(actor)  # a failed spawn leaves nothing behind
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


_ARG_DOC = re.compile(r"^\s+(\w+) \(([^)]+)\):", re.M)
_FIELD_DOC = re.compile(r"^- ``(\w+)`` \(([^)]+)\):", re.M)  # a struct's "Editor Properties" list


def _ufunction_params(target, fn):
    """{python parameter name: type} of fn on target, from the docstring UE generates
    for the Python method ("Args:\n    name (Type): ..."); None when no method matches.
    The method is found by name ignoring case and underscores (K2_ prefix optional):
    UE's Python names do not follow one rule (SetActorScale3D -> set_actor_scale3d)."""
    want = fn.lower().replace("_", "")
    alt = want[2:] if want.startswith("k2") else want
    names = [a for a in dir(type(target)) if a.lower().replace("_", "") in (want, alt)]
    # The exact name first (K2_GetFoo and GetFoo can both exist); never first-match.
    exact = [a for a in names if a.lower().replace("_", "") == want]
    names = exact or names
    if not names:
        return None
    if len(names) > 1:
        raise _V2Error("CONFLICT", "%s matches several Python methods: %s" % (fn, ", ".join(sorted(names))))
    doc = getattr(getattr(type(target), names[0], None), "__doc__", None) or ""
    return dict(_ARG_DOC.findall(doc.split("Returns:")[0]))


def _struct_arg(tname, value, where, st=None):
    """A JSON object for a struct parameter, built field by field: UE's own dict
    conversion silently drops unknown keys ({"X": 1} became a zero Vector, live)."""
    m = re.match(r"^Array\[(\w+)\]$", tname)
    if m and isinstance(value, list):
        return [_struct_arg(m.group(1), v, "%s[%d]" % (where, i)) for i, v in enumerate(value)]
    if isinstance(value, list) and any(isinstance(e, dict) for e in value):
        # A Set/Map (or unknown) container of structs: UE would convert it unchecked.
        raise _V2Error("BAD_VALUE", "%s: objects inside a %s cannot be checked (only arrays of structs are)" % (where, tname))
    st = st or getattr(unreal, tname, None)
    base = getattr(unreal, "StructBase", None)
    if not isinstance(value, dict) or base is None or not (isinstance(st, type) and issubclass(st, base)):
        return value
    out = st()
    fields = {n.lower(): t for n, t in _FIELD_DOC.findall(st.__doc__ or "")}
    for k, v in value.items():
        try:
            cur = out.get_editor_property(k)
        except Exception:
            raise _V2Error("BAD_VALUE", "%s: %s has no field %r" % (where, tname, k)) from None
        if isinstance(v, dict) and isinstance(cur, base):
            v = _struct_arg(type(cur).__name__, v, "%s.%s" % (where, k), type(cur))
        elif isinstance(v, list) and any(isinstance(e, dict) for e in v):
            # An array-of-structs field: each element is built and checked too (UE's own
            # conversion would drop unknown keys here as well).
            ftype = fields.get(k.lower())
            if not ftype:
                raise _V2Error("BAD_VALUE", "%s.%s: the field's type is not documented, so its elements cannot be "
                               "checked (pass each element as a list of its fields in order)" % (where, k))
            v = _struct_arg(ftype, v, "%s.%s" % (where, k))
        try:
            out.set_editor_property(k, v)
        except Exception as e:
            raise _V2Error("BAD_VALUE", "%s.%s: %s" % (where, k, e)) from None
    return out


def _call_args(target, fn, fargs):
    """kwargs for call_method: JSON objects (and arrays of them) for struct parameters
    become structs whose every field was checked; other values pass to UE's conversion
    (a list for a struct is positional fields; a list for an array is its elements)."""
    if not any(isinstance(v, dict) or (isinstance(v, list) and any(isinstance(e, dict) for e in v))
               for v in fargs.values()):
        return fargs
    params = _ufunction_params(target, fn)
    if params is None:
        raise _V2Error("BAD_VALUE", "%s has no Python signature: its struct arguments cannot be checked "
                       "(pass a struct as a list of its fields in order)" % fn)
    out = {}
    for k, v in fargs.items():
        if k not in params:
            raise _V2Error("BAD_VALUE", "%s has no parameter %r" % (fn, k), parameters=sorted(params))
        out[k] = _struct_arg(params[k], v, k)
    return out


def _op_actor_call(args):
    world, name = _v2_world(args, "pie")
    if name != "pie":
        raise _V2Error("UNSUPPORTED", "actor_call runs in PIE only in v2.0 (CallInEditor functions are not supported)")
    target = _resolve_object(world, name, args.get("actor"))
    fn = args.get("function")
    if not fn:
        raise _V2Error("BAD_VALUE", "function is required")
    fargs = args.get("args") or {}
    if not isinstance(fargs, dict):
        raise _V2Error("BAD_VALUE", "args must be an object of parameter name -> value")
    parse = args.get("parse") or ""
    if parse not in ("", "json"):
        raise _V2Error("BAD_VALUE", "parse must be json (got %r)" % (parse,))
    try:
        result = target.call_method(fn, kwargs=_call_args(target, fn, fargs))
    except Exception as e:
        if "find function" in str(e).lower() or "no function" in str(e).lower():
            raise _V2Error("NOT_FOUND", "%s has no callable function %r" % (target.get_name(), fn)) from e
        raise
    label = target.get_actor_label() if hasattr(target, "get_actor_label") else target.get_name()
    if parse == "json":
        # Explicit, never guessed: a game API returning a JSON string asks for it.
        if not isinstance(result, str):
            raise _V2Error("BAD_VALUE", "parse=json: %s returned %s, not a string" % (fn, type(result).__name__))
        try:
            decoded = json.loads(result)
        except ValueError as e:
            raise _V2Error("BAD_VALUE", "parse=json: %s did not return JSON (%s)" % (fn, e), head=result[:200]) from None
        return {"world": name, "actor": label, "function": fn, "result": decoded}
    return {"world": name, "actor": label, "function": fn,
            "result": _coerce_prop(result, 2048) if result is not None else None}
