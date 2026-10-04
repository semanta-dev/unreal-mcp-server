# The engine's default object channels, as ObjectTypeQuery1..6 (a project's own channels follow).
_OBJECT_TYPES = {"world_static": 1, "world_dynamic": 2, "pawn": 3, "physics_body": 4, "vehicle": 5, "destructible": 6}


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
        names = args.get("object_types") or list(_OBJECT_TYPES)
        nums = []
        for n in names:
            if n in _OBJECT_TYPES:
                nums.append(_OBJECT_TYPES[n])
                continue
            # A project object channel, by the name it has in Project Settings > Collision.
            lib = _need_plugin(9, "a project object channel")
            q = int(lib.object_type_by_channel_name(str(n)))
            if q <= 0:
                raise _V2Error("BAD_VALUE", "object_types: %r is not an engine object type (%s) nor a project object "
                               "channel (a trace channel is not an object type)" % (n, ", ".join(_OBJECT_TYPES)))
            nums.append(q)
        # Every type asked for — before, only WorldStatic: pawns, physics bodies and every
        # movable actor were invisible to the overlap.
        types = [getattr(unreal.ObjectTypeQuery, "OBJECT_TYPE_QUERY%d" % n) for n in nums]
        actors = unreal.SystemLibrary.sphere_overlap_actors(
            world, unreal.Vector(c[0], c[1], c[2]), float(args.get("radius", 100.0)), types, None, []) or []
        return {"kind": kind, "count": len(actors), "object_types": names,
                "actors": [a.get_actor_label() for a in actors],
                "hits": [{"label": a.get_actor_label(), "path": a.get_path_name(), "class": a.get_class().get_name()} for a in actors]}
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
    where = args.get("world")  # v2 passes editor|pie; absent = v1 behaviour (PIE if running)
    if where == "editor":
        unreal.SystemLibrary.execute_console_command(None, cmd)
        return {"ran": cmd, "via": "editor", "world": "editor"}
    world = _game_world()
    if where == "pie" and not world:
        raise _V2Error("NOT_IN_PIE", "PIE is not running; use world=editor or start PIE")
    pc = unreal.GameplayStatics.get_player_controller(world, 0) if world else None
    if pc:
        # PlayerController.ConsoleCommand is not exposed to Python in 5.7; the library
        # call with specific_player routes through that controller just the same.
        unreal.SystemLibrary.execute_console_command(world, cmd, pc)
        return {"ran": cmd, "via": "player_controller", "world": "pie"}
    unreal.SystemLibrary.execute_console_command(world, cmd)
    return {"ran": cmd, "via": "world" if world else "editor", "world": "pie" if world else "editor"}


