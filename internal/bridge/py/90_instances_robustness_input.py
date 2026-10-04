# --- PW instanced-content: HISM/ISM instances (blind to list_actors/counts) --

def _iter_ism_components(world_sel, tag, mesh_filter):
    """Yield (actor, component) for every Instanced/Hierarchical-ISM component,
    optionally filtered by a component tag and/or static-mesh path substring."""
    if world_sel == "auto":
        world_sel = "pie" if _game_world() else "editor"
    elif world_sel not in ("editor", "game", "pie"):
        raise _V2Error("BAD_VALUE", "world must be editor, pie or auto (got %r)" % (world_sel,))
    if world_sel in ("game", "pie"):
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
    try:
        plugin_api = _plugin_api()
    except Exception:
        plugin_api = -1  # a broken plugin build: report it, never fail the liveness probe
    return {"ok": True, "version": _MCP2_BRIDGE_VERSION, "pie": pie, "plugin_api": plugin_api, "t": time.time()}


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

def _pie_control():
    """The running game's MCPControlSubsystem (the UnrealMCP plugin)."""
    world = _game_world()
    if not world:
        raise _V2Error("NOT_IN_PIE", "PIE is not running (start it with the pie tool)")
    ctrl_cls = getattr(unreal, "MCPControlSubsystem", None)
    ctrl = ctrl_cls.get(world) if ctrl_cls is not None else None
    if not ctrl:
        raise _V2Error("PLUGIN_MISSING", "no MCPControlSubsystem in the game instance: copy plugin/UnrealMCP into "
                       "<project>/Plugins and rebuild (build strategy=ubt)")
    return world, ctrl


def _op_pie_input(args):
    """Synthesize input into the live game like a player: keys (tap/press/release/hold)
    and, with plugin API 5, an analog axis sent every tick for duration_s."""
    _, ctrl = _pie_control()
    key = str(args.get("key") or "")
    action = args.get("action") or "tap"
    if action == "release_all":
        ctrl.release_all()
        return {"ok": True, "action": action}
    if not key:
        raise _V2Error("BAD_VALUE", "input needs key")
    if action == "axis":
        _need_plugin(5, "axis input")
        value, dur = float(args.get("value", 0.0)), float(args.get("duration_s") or 0.1)
        why = ctrl.inject_axis(key, value, dur)
        if why:
            raise _V2Error("BAD_VALUE", why)
        return {"ok": True, "key": key, "action": action, "value": value, "duration_s": dur}
    if action == "hold":
        ok = ctrl.hold_key(key, float(args.get("duration_s") or 1.0))
    elif action == "press":
        ok = ctrl.inject_key_by_name(key, True)
    elif action == "release":
        ok = ctrl.inject_key_by_name(key, False)
    elif action == "tap":
        ok = ctrl.tap_key(key)
    else:
        raise _V2Error("BAD_VALUE", "action must be tap, press, release, hold, axis or release_all (got %r)" % (action,))
    if not ok:
        raise _V2Error("BAD_VALUE", "input rejected: no player controller, or %r is not a key" % key)
    return {"ok": True, "key": key, "action": action}


def _xy(v, what):
    if not (isinstance(v, (list, tuple)) and len(v) == 2 and all(isinstance(n, (int, float)) for n in v)):
        raise _V2Error("BAD_VALUE", "%s must be [x, y] in viewport pixels (got %r)" % (what, v))
    return float(v[0]), float(v[1])


def _pointer_result(raw, what):
    """The plugin's pointer JSON, or its refusal as an error (never a silent miss)."""
    try:
        res = json.loads(raw)
    except ValueError:
        raise _V2Error("BAD_VALUE", "%s: the plugin returned %r" % (what, raw)) from None
    if not res.get("ok"):
        msg = str(res.get("error") or "refused")
        code = ("NOT_FOUND" if msg.startswith("no visible") else
                "CONFLICT" if " are named " in msg or "is covered" in msg else
                "NOT_IN_PIE" if msg in ("no game viewport", "no game world") else "BAD_VALUE")
        raise _V2Error(code, "%s: %s" % (what, msg))
    res.pop("ok", None)
    return res


def _op_pie_cursor(args):
    """Move / click / drag the game's cursor in viewport pixels through Slate (plugin
    API 5): GameAndUI input, the user's OS cursor never moved or captured. The game's
    cursor stays where the agent put it until action=release (or PIE ends)."""
    _need_plugin(5, "cursor input")
    _, ctrl = _pie_control()
    action = args.get("action") or "click"
    if action == "release":
        return {"action": action, "was_pinned": bool(ctrl.release_cursor())}
    x, y = _xy(args.get("position"), "position")
    button = str(args.get("button") or "")
    if action == "move":
        raw = ctrl.move_cursor(x, y)
    elif action == "click":
        raw = ctrl.click_at(x, y, button)
    elif action == "drag":
        tx, ty = _xy(args.get("to"), "to")
        raw = ctrl.drag_cursor(x, y, tx, ty, float(args.get("duration_s") or 0.3), button)
    else:
        raise _V2Error("BAD_VALUE", "cursor action must be move, click, drag or release (got %r)" % (action,))
    res = _pointer_result(raw, "cursor " + action)
    res["action"] = action
    return res


def _op_pie_ui_click(args):
    """Click the centre of the one visible live widget with this name (plugin API 5);
    refused when none, several, or something covers it."""
    _need_plugin(5, "ui_click")
    _, ctrl = _pie_control()
    widget = str(args.get("widget") or "")
    if not widget:
        raise _V2Error("BAD_VALUE", "ui_click needs widget (the name of a widget on screen)")
    return _pointer_result(ctrl.click_widget(widget, str(args.get("button") or "")), "ui_click " + widget)
