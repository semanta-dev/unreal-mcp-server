# --- R5.1: the gameplay event timeline -----------------------------------------------------
# A playtest records two sources while it runs: the engine's own events (damage taken,
# actors spawned / destroyed — the plugin's UMCPEventRecorder, API 8, a C++ ring buffer)
# and the game's journal (game_api GetEventsSince: kills, deaths, VFX, SFX, waves...).
# One slate post-tick callback drains both every interval_s, so nothing depends on the
# capture source. A lost stretch of either source (a ring overrun, a journal gap, a failed
# drain) is kept as a gap: what reads the timeline never scores a window with a gap.

_MCP_EVENT_SESSIONS = {}
_EVENTS_MAX = 200000  # per session, across sources


def _events_world_t(world):
    return float(unreal.GameplayStatics.get_time_seconds(world)) if world else 0.0


def _events_gap(sess, source, why, dropped=0):
    sess["gaps"].append({"source": source, "from_t": sess["last_t"].get(source, sess["start_t"]),
                         "to_t": sess["now_t"], "dropped": int(dropped or 0), "reason": why})


def _events_add(sess, source, events):
    for e in events:
        if len(sess["events"]) >= _EVENTS_MAX:
            if not sess.get("full"):
                sess["full"] = True
                _events_gap(sess, source, "the session holds %d events: the rest were not kept" % _EVENTS_MAX)
            sess["gaps"][-1]["to_t"] = sess["now_t"]
            return
        if not isinstance(e, dict):
            continue
        e = dict(e)
        e["source"] = source
        sess["events"].append(e)


def _events_drain(sess):
    """Read what both sources recorded since the last drain (game thread)."""
    world = _game_world()
    if world is None:
        sess["world_ended"] = True
        return
    sess["now_t"] = _events_world_t(world)
    rec = sess.get("recorder")
    if rec is not None:
        try:
            while True:
                out = json.loads(rec.drain_events_json(sess["engine_cursor"], 10000) or "{}")
                if out.get("gap"):
                    _events_gap(sess, "engine", "the plugin's ring buffer overran", out.get("dropped"))
                _events_add(sess, "engine", out.get("events") or [])
                sess["engine_cursor"] = int(out.get("next_cursor", sess["engine_cursor"]))
                if not out.get("more"):
                    break
            sess["last_t"]["engine"] = sess["now_t"]
        except Exception as e:
            _events_gap(sess, "engine", "drain failed: %s" % e)
            sess["last_t"]["engine"] = sess["now_t"]
    j = sess.get("journal")
    if j is not None:
        try:
            for _ in range(64):  # a journal pages its answer; 64 pages is far beyond any ring
                out = _game_json(j["function"], j["obj"].call_method(j["function"], args=(sess["journal_cursor"],)))
                if out.get("gap"):
                    _events_gap(sess, "journal", out.get("reason") or "the game's journal overran", out.get("dropped"))
                evs = out.get("events") or []
                _events_add(sess, "journal", evs)
                nxt = out.get("next_cursor")
                if nxt is None or nxt == sess["journal_cursor"] or not evs:
                    if nxt is not None:
                        sess["journal_cursor"] = nxt
                    break
                sess["journal_cursor"] = nxt
            sess["last_t"]["journal"] = sess["now_t"]
        except Exception as e:
            _events_gap(sess, "journal", "drain failed: %s" % e)
            sess["last_t"]["journal"] = sess["now_t"]


def _events_tick(session):
    def _cb(delta_seconds):
        sess = _MCP_EVENT_SESSIONS.get(session)
        if not sess or not sess["running"] or sess.get("world_ended"):
            return
        sess["elapsed"] += float(delta_seconds or 0.0)
        if sess["elapsed"] - sess["last_drain"] < sess["interval_s"]:
            return
        sess["last_drain"] = sess["elapsed"]
        _events_drain(sess)
    return _cb


def _op_events_start(args):
    """Start recording the event timeline in the running game. engine: the plugin's
    recorder (API 8; older: the engine source is unavailable, never a failure);
    journal: {class, function} of the game_api events read (absent: unavailable)."""
    session = _safe_name(args.get("session") or "", "session")
    if session in _MCP_EVENT_SESSIONS and _MCP_EVENT_SESSIONS[session]["running"]:
        raise _V2Error("CONFLICT", "event session %s is already recording" % session)
    world = _game_world()
    if world is None:
        raise _V2Error("NOT_IN_PIE", "PIE is not running")
    now = _events_world_t(world)
    sess = {"session": session, "running": True, "start_t": now, "now_t": now, "last_t": {}, "events": [], "gaps": [],
            "sources": {"server": "recorded"}, "source_why": {}, "elapsed": 0.0, "last_drain": 0.0,
            "interval_s": max(0.1, float(args.get("interval_s") or 0.5)), "engine_cursor": 0, "journal_cursor": ""}
    # The journal first: a refused journal must not leave the engine recorder bound.
    j = args.get("journal") or {}
    if j.get("class") and j.get("function"):
        lib = _need_plugin(3, "the game API")
        obj = _game_api_object(j)
        if not lib.is_pure_or_const(obj.get_class(), j["function"]):
            raise _V2Error("BAD_VALUE", "%s.%s is not BlueprintPure or const" % (obj.get_class().get_name(), j["function"]))
        sess["journal"] = {"obj": obj, "function": j["function"]}
        sess["sources"]["journal"] = "recorded"
    else:
        sess["sources"]["journal"] = "unavailable"
        sess["source_why"]["journal"] = args.get("journal_why") or "the project declares no game_api (.umcp.json): no game journal"
    api = _plugin_api()
    if not args.get("engine", True):
        sess["sources"]["engine"], sess["source_why"]["engine"] = "off", "not requested"
    elif api < 8:
        sess["sources"]["engine"] = "unavailable"
        sess["source_why"]["engine"] = "the UnrealMCP plugin API is %d; the engine recorder needs 8" % api
    else:
        rec = unreal.MCPEventRecorder.get(world)
        out = json.loads(rec.start_recording(int(args.get("capacity") or 65536)) or "{}") if rec else {}
        if not out.get("ok"):
            raise _V2Error("EDITOR_ERROR", "the engine recorder did not start: %s" % (out.get("error") or "no recorder in this world"))
        sess["recorder"], sess["sources"]["engine"], sess["bound_at_start"] = rec, "recorded", out.get("bound")
    try:
        _events_drain(sess)  # anything the journal holds from before the window
        sess["handle"] = unreal.register_slate_post_tick_callback(_events_tick(session))
    except Exception:
        if sess.get("recorder") is not None:
            sess["recorder"].stop_recording()
        raise
    _MCP_EVENT_SESSIONS[session] = sess
    return {"session": session, "start_t": now, "sources": sess["sources"], "source_why": sess["source_why"],
            "bound": sess.get("bound_at_start")}


def _op_seed_random(args):
    """Seed the engine's global random streams (plugin API 8) for a seeded playtest run."""
    lib = _need_plugin(8, "seeded runs")
    seed = args.get("seed")
    if not isinstance(seed, int) or isinstance(seed, bool) or not -2**31 <= seed < 2**31:
        raise _V2Error("BAD_VALUE", "seed must be a 32-bit integer (got %r)" % (seed,))
    if _game_world() is None:
        raise _V2Error("NOT_IN_PIE", "PIE is not running")
    lib.seed_random_streams(seed)
    return {"seeded": seed}


def _op_events_stop(args):
    """Drain one last time, stop both sources and return the timeline: {start_t, end_t,
    events (oldest first), gaps, sources, source_why, engine: {unbound, still_bound}}."""
    session = args.get("session") or ""
    sess = _MCP_EVENT_SESSIONS.pop(session, None)
    if sess is None:
        raise _V2Error("NOT_FOUND", "no event session %s" % session)
    sess["running"] = False
    if sess.get("handle") is not None:
        unreal.unregister_slate_post_tick_callback(sess["handle"])
    if not sess.get("world_ended"):
        _events_drain(sess)
    out = {"session": session, "start_t": sess["start_t"], "end_t": sess["now_t"], "gaps": sess["gaps"],
           "sources": sess["sources"], "source_why": sess["source_why"]}
    rec = sess.get("recorder")
    if rec is not None:
        if sess.get("world_ended"):
            # PIE ended first: the recorder unbound itself with its game instance.
            out["engine"] = {"stopped_with": "pie_end"}
        else:
            try:
                out["engine"] = json.loads(rec.stop_recording() or "{}")
            except Exception as e:
                out["engine"] = {"error": str(e)}
    if sess.get("world_ended"):
        _events_gap(sess, "engine" if rec is not None else "journal", "PIE ended before the session stopped")
    out["events"] = sorted(sess["events"], key=lambda e: (float(e.get("t") or 0.0), e.get("source") != "engine"))
    return out
