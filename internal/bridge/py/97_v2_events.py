# --- R5.1: the gameplay event timeline -----------------------------------------------------
# A playtest records two sources while it runs: the engine's own events (damage taken,
# actors spawned / destroyed — the plugin's UMCPEventRecorder, API 8, a C++ ring buffer)
# and the game's journal (game_api GetEventsSince: kills, deaths, VFX, SFX, waves...).
# One slate post-tick callback drains both every interval_s, so nothing depends on the
# capture source. A lost stretch of either source (a ring overrun, a journal gap, a failed
# drain, the recorded world torn down) is kept as a gap: what reads the timeline never
# scores a window with a gap.

_MCP_EVENT_SESSIONS = {}
_EVENTS_MAX = 200000  # per session, across sources
# A journal stamps a hit's visual_t up to 1 s after the hit (GAME_CONTRACT): events are
# taken once they are this old, so a stamp is never read before it is written.
_JOURNAL_SETTLE_S = 1.0


def _events_world_t(world):
    return float(unreal.GameplayStatics.get_time_seconds(world)) if world else 0.0


def _events_gap(sess, source, why, dropped=0, from_t=None):
    """Record a stretch in which the source's events may be missing. One that ends at the
    window's start (the first drain's look back) is before the window and not a gap."""
    # From where the source was last read completely (taken), not the last drain: the
    # journal's newest second is held back, so a drain does not take all it saw.
    start = sess["taken"].get(source, sess["start_t"]) if from_t is None else from_t
    gap = {"source": source, "from_t": start, "to_t": sess["now_t"], "dropped": int(dropped or 0), "reason": why}
    if sess.get("initial") and gap["to_t"] <= sess["start_t"]:
        sess["before_window"].append(gap)
        return gap
    sess["gaps"].append(gap)
    return gap


def _events_open_gaps(sess):
    """Gaps that last until the session ends (a full session, a lost world): extend them."""
    for g in sess["open_gaps"]:
        g["to_t"] = max(g["to_t"], sess["now_t"])


def _events_add(sess, source, events):
    for e in events:
        if not isinstance(e, dict):
            continue
        if len(sess["events"]) >= _EVENTS_MAX:
            if not sess.get("full"):
                sess["full"] = True
                # Every recorded source loses events from here on: this one from the first
                # event dropped, the others from where they were last read completely.
                for src, state in sess["sources"].items():
                    if state == "recorded":
                        start = float(e.get("t") or sess["now_t"]) if src == source else sess["taken"].get(src, sess["start_t"])
                        sess["open_gaps"].append(_events_gap(sess, src, "the session holds %d events: later ones were "
                                                             "not kept" % _EVENTS_MAX, from_t=min(start, sess["now_t"])))
            return
        e = dict(e)
        e["source"] = source
        sess["events"].append(e)


def _journal_cursor(nxt, seq):
    """A cursor just after `seq` in the journal's epoch (cursors are "<epoch>:<seq>")."""
    if not isinstance(nxt, str) or ":" not in nxt:
        raise RuntimeError("the journal answered without a next_cursor \"<epoch>:<seq>\" (GAME_CONTRACT)")
    epoch = nxt.rsplit(":", 1)[0]
    return "%s:%d" % (epoch, int(seq))


def _events_world_changed(sess, why, at_t=None):
    """The recorded world is gone (map travel, a restart) while PIE runs: the new world's
    clock starts again, so the timeline ends here — every recorded source gets a gap from
    then to the end, and nothing more is drained."""
    if sess.get("world_changed"):
        return
    sess["world_changed"] = why
    for src in ("engine", "journal"):
        if sess["sources"].get(src) == "recorded":
            # The engine was drained up to the loss (at_t); the journal only up to what it took.
            start = at_t if (at_t is not None and src == "engine") else sess["taken"].get(src, sess["start_t"])
            _events_gap(sess, src, why, from_t=min(start, sess["now_t"]))


def _events_drain(sess, final=False):
    """Read what both sources recorded since the last drain (game thread)."""
    if sess.get("world_changed"):
        return
    world = _game_world()
    if world is None:
        sess["world_ended"] = True
        return
    now = _events_world_t(world)
    if now < sess["now_t"] - 1e-3:
        # The clock went back: a new world (a restart of the same map). Keep the old clock.
        _events_world_changed(sess, "the world was restarted (its clock went back): the recording ends there")
        return
    sess["now_t"] = now
    _events_open_gaps(sess)
    rec = sess.get("recorder")
    if rec is not None and not sess.get("engine_lost"):
        try:
            for _ in range(1000):
                out = json.loads(rec.drain_events_json(sess["engine_cursor"], 10000) or "{}")
                if out.get("generation") != sess["generation"]:
                    raise RuntimeError("the engine recorder was restarted by another session (generation %s, ours %s)"
                                       % (out.get("generation"), sess["generation"]))
                if out.get("gap"):
                    _events_gap(sess, "engine", out.get("reason") or "the plugin's ring buffer overran", out.get("dropped"))
                _events_add(sess, "engine", out.get("events") or [])
                sess["engine_cursor"] = int(out.get("next_cursor", sess["engine_cursor"]))
                if out.get("world_lost"):
                    # The recorded world was torn down (map travel, a restart): nothing after it.
                    sess["engine_lost"] = True
                    _events_world_changed(sess, "the recorded world was torn down (map travel or restart): the recording "
                                          "ends there", at_t=float(out.get("lost_t") or sess["now_t"]))
                    return
                if not out.get("more"):
                    break
            sess["taken"]["engine"] = sess["now_t"]
        except Exception as e:
            _events_gap(sess, "engine", "drain failed: %s" % e)  # taken stays: the gap runs from there
    j = sess.get("journal")
    if j is not None:
        try:
            # The last drain takes everything left (a visual_t still unstamped then is the
            # game's; the feel audit leaves out hits in the last second).
            settle = float("inf") if final else sess["now_t"] - _JOURNAL_SETTLE_S
            done = False
            for _ in range(64):
                out = _game_json(j["function"], j["obj"].call_method(j["function"], args=(sess["journal_cursor"],)))
                if out.get("gap") and out.get("reason") == "world_changed" and not sess.get("initial"):
                    _events_world_changed(sess, "the game's world changed (journal world_changed): the recording ends there")
                    return
                if out.get("gap"):
                    _events_gap(sess, "journal", out.get("reason") or "the game's journal overran", out.get("dropped"))
                evs = [e for e in (out.get("events") or []) if isinstance(e, dict)]
                nxt = out.get("next_cursor")
                ready = []
                for e in evs:
                    if float(e.get("t") or 0.0) > settle:
                        break  # too young: its visual_t may not be stamped yet; read it again later
                    ready.append(e)
                _events_add(sess, "journal", ready)
                if len(ready) < len(evs):
                    if ready:
                        sess["journal_cursor"] = _journal_cursor(nxt, ready[-1]["seq"])
                    elif out.get("gap"):
                        sess["journal_cursor"] = _journal_cursor(nxt, int(evs[0]["seq"]) - 1)
                    done = True
                    break
                if nxt is None or nxt == sess["journal_cursor"] or not evs:
                    if nxt is not None:
                        sess["journal_cursor"] = nxt
                    done = True
                    break
                sess["journal_cursor"] = nxt
            if not done:
                _events_gap(sess, "journal", "the journal had more than 64 pages to read in one drain")
            else:
                # Read completely up to the settle threshold (everything, on the last drain).
                sess["taken"]["journal"] = max(sess["taken"].get("journal", sess["start_t"]),
                                               sess["now_t"] if final else min(settle, sess["now_t"]))
        except Exception as e:
            _events_gap(sess, "journal", "drain failed: %s" % e)  # taken stays: the gap runs from there


def _events_unregister(sess):
    if sess.get("handle") is not None:
        unreal.unregister_slate_post_tick_callback(sess["handle"])
        sess["handle"] = None


def _events_tick(session):
    def _cb(delta_seconds):
        sess = _MCP_EVENT_SESSIONS.get(session)
        if not sess or not sess["running"]:
            return
        if sess.get("world_ended") or sess.get("world_changed"):
            _events_unregister(sess)  # nothing more to drain until the stop
            return
        sess["elapsed"] += float(delta_seconds or 0.0)
        if sess["elapsed"] - sess["last_drain"] < sess["interval_s"]:
            return
        sess["last_drain"] = sess["elapsed"]
        _events_drain(sess)
    return _cb


def _event_kinds(lib, obj, capabilities):
    """(the event kinds the game declares — GetCapabilitiesJson event_kinds —, or None and
    why). The capabilities read is a game_api read: pure or const, checked."""
    if not capabilities:
        return None, "the project's game_api names no capabilities function"
    if not lib.is_pure_or_const(obj.get_class(), capabilities):
        return None, "%s is not BlueprintPure or const: it is not read" % capabilities
    try:
        caps = _game_json(capabilities, obj.call_method(capabilities))
    except Exception as e:
        return None, "%s failed: %s" % (capabilities, e)
    kinds = caps.get("event_kinds")
    if not isinstance(kinds, list):
        return None, "%s has no event_kinds list" % capabilities
    return sorted(str(k) for k in kinds), None


def _op_events_start(args):
    """Start recording the event timeline in the running game. engine: the plugin's
    recorder (API 8; older: the engine source is unavailable, never a failure);
    journal: {class, function, capabilities?} of the game_api (absent: unavailable)."""
    session = _safe_name(args.get("session") or "", "session")
    world = _game_world()
    if world is None:
        raise _V2Error("NOT_IN_PIE", "PIE is not running")
    # A session whose world is gone (PIE ended, a map travel) can record nothing more: if its
    # stop never came (the server went away), it is evicted rather than blocking for good.
    evicted = []
    for s, v in list(_MCP_EVENT_SESSIONS.items()):
        if v.get("world_ended") or v.get("world_changed") or v.get("world") is not world:
            _events_unregister(v)
            _MCP_EVENT_SESSIONS.pop(s, None)
            evicted.append(s)
    live = [s for s, v in _MCP_EVENT_SESSIONS.items() if v["running"]]
    if live:
        # The engine recorder is one per game: a second session would restart it under the first.
        raise _V2Error("CONFLICT", "event session %s is recording: stop it first (one session at a time)" % live[0])
    now = _events_world_t(world)
    sess = {"session": session, "running": True, "world": world, "start_t": now, "now_t": now, "taken": {}, "events": [], "gaps": [],
            "before_window": [], "open_gaps": [], "sources": {"server": "recorded"}, "source_why": {}, "elapsed": 0.0,
            "last_drain": 0.0, "interval_s": max(0.1, float(args.get("interval_s") or 0.5)), "engine_cursor": 0,
            "journal_cursor": "", "journal_kinds": None}
    # The journal first: a refused journal must not leave the engine recorder bound.
    j = args.get("journal") or {}
    if j.get("class") and j.get("function"):
        lib = _need_plugin(3, "the game API")
        obj = _game_api_object(j)
        if not lib.is_pure_or_const(obj.get_class(), j["function"]):
            raise _V2Error("BAD_VALUE", "%s.%s is not BlueprintPure or const" % (obj.get_class().get_name(), j["function"]))
        sess["journal"] = {"obj": obj, "function": j["function"]}
        sess["sources"]["journal"] = "recorded"
        sess["journal_kinds"], why = _event_kinds(lib, obj, j.get("capabilities"))
        if why:
            sess["source_why"]["journal_kinds"] = why
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
        sess["generation"] = out.get("generation")
    try:
        sess["initial"] = True
        _events_drain(sess)  # anything the journal holds from before the window
        sess["initial"] = False
        sess["handle"] = unreal.register_slate_post_tick_callback(_events_tick(session))
    except Exception:
        if sess.get("recorder") is not None:
            sess["recorder"].stop_recording()
        raise
    _MCP_EVENT_SESSIONS[session] = sess
    out = {"session": session, "start_t": now, "sources": sess["sources"], "source_why": sess["source_why"],
           "bound": sess.get("bound_at_start"), "journal_kinds": sess["journal_kinds"]}
    if evicted:
        out["evicted"] = evicted  # sessions of a world that is gone, never stopped
    return out


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
    events (oldest first), gaps, sources, source_why, journal_kinds, engine: {unbound,
    still_bound}}."""
    session = args.get("session") or ""
    sess = _MCP_EVENT_SESSIONS.pop(session, None)
    if sess is None:
        raise _V2Error("NOT_FOUND", "no event session %s" % session)
    sess["running"] = False
    _events_unregister(sess)
    if not sess.get("world_ended"):
        _events_drain(sess, final=True)
    out = {"session": session, "start_t": sess["start_t"], "end_t": sess["now_t"], "gaps": sess["gaps"],
           "sources": sess["sources"], "source_why": sess["source_why"], "journal_kinds": sess["journal_kinds"]}
    if sess["before_window"]:
        out["before_window"] = sess["before_window"]
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
        # Events of the last drain interval before PIE ended were never read.
        for src in ("engine", "journal"):
            if sess["sources"].get(src) == "recorded":
                _events_gap(sess, src, "PIE ended before the session stopped: its last events were not read")
    out["events"] = sorted(sess["events"], key=lambda e: (float(e.get("t") or 0.0), e.get("source") != "engine"))
    return out
