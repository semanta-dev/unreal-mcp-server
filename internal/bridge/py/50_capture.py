# --- multi-frame capture recorder (in-editor; Go collects in one capture_stop) ---
#
# The command channel is single-flight, so a high-frequency capture cannot be
# driven by Go polling. Instead an in-editor slate-post-tick callback buffers
# timestamped frames + observed state to disk with a manifest; Go reads the
# whole session off disk after ONE capture_stop.

_MCP_RECORDERS = {}


def _capture_session_dir(session):
    return _saved_mcp_dir("capture", session) + "/"


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
                # HighResShot writes asynchronously to the absolute session path;
                # Go tolerates a not-yet-flushed final frame (skips absent files).
                unreal.AutomationLibrary.take_high_res_screenshot(rec["width"], rec["height"], rec["dir"] + rel)
            else:
                _position_capture(rec)
                cc = rec["cap"].capture_component2d
                cc.capture_scene()
                unreal.RenderingLibrary.export_render_target(world, rec["rt"], rec["dir"], rel)
            t_world = unreal.GameplayStatics.get_time_seconds(world) if world else 0.0
            st = _recorder_observe(rec)
            hitch_ms = rec.get("max_dt_ms", dt_ms)
            # The recorder's own tick timing, NOT the game's frame rate: a frame that
            # takes a screenshot is slower because of it. Rubrics must opt in
            # (allow_perturbed); measure the game with a CsvProfiler run instead.
            st["recorder"] = {
                "tick_ms": dt_ms,
                "max_tick_ms": hitch_ms,  # worst tick since the last sample
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
    # UE Python drops an enum's E prefix: EMCPCaptureCamera is unreal.MCPCaptureCamera.
    cam_cls = getattr(unreal, "MCPCaptureCamera", None) or getattr(unreal, "EMCPCaptureCamera", None)
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
        # Frames are HighResShots written to absolute paths in the session dir (a bare
        # name would resolve against the project's configurable screenshot folder).
        rec_dir = _capture_session_dir(session)
        file_prefix = ""
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


