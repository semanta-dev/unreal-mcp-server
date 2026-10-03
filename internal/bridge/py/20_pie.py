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


