"""R5.1 companion ops: the event timeline session — the plugin's engine recorder and the
game journal drained on one tick, every lost stretch kept as a gap."""
import json

import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Class, Fake, installed
from test_v2_r1 import Obj

SUB_CLS = Class("AgentSubsystem", "/Script/Game.AgentSubsystem")


class Recorder:
    """The plugin's UMCPEventRecorder: a ring the test fills; drain pages by max."""

    def __init__(self):
        self.events, self.dropped_before, self.recording, self.stopped = [], 0, False, 0
        self.generation, self.lost_t = 0, None

    def start_recording(self, capacity):
        self.recording = True
        self.generation += 1
        return json.dumps({"ok": True, "bound": 12, "capacity": capacity, "generation": self.generation})

    def stop_recording(self):
        self.recording, self.stopped = False, self.stopped + 1
        return json.dumps({"ok": True, "unbound": 12, "still_bound": 0})

    def drain_events_json(self, cursor, max_events):
        after = [e for e in self.events if e["seq"] > cursor]
        oldest = self.events[0]["seq"] if self.events else cursor + 1
        lost = max(0, oldest - (cursor + 1))
        page = after[:2]  # pages of 2: the drain must keep reading while more
        nxt = page[-1]["seq"] if page else cursor
        out = {"events": page, "next_cursor": nxt, "gap": lost > 0, "dropped": lost,
               "more": len(after) > len(page), "generation": self.generation}
        if self.lost_t is not None and not out["more"]:
            out["world_lost"], out["lost_t"] = True, self.lost_t
        return json.dumps(out)


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = []
    journal = {"events": [], "gap_on": None}

    def since(cursor):
        start = int(cursor.split(":")[1]) if cursor else 0
        evs = [e for e in journal["events"] if e["seq"] > start]
        nxt = "e:%d" % (evs[-1]["seq"] if evs else start)
        gap = journal["gap_on"] is not None and journal["gap_on"] == cursor
        return json.dumps({"events": evs, "next_cursor": nxt, "gap": gap, "dropped": 9 if gap else 0})

    sub = Obj("AgentSubsystem_0", SUB_CLS, functions={"GetEventsSince": since, "GetCapabilitiesJson": lambda: json.dumps(
        {"event_kinds": ["kill", "hit", "death", "wave_start", "sfx"]})})
    fake.classes["/Script/Game.AgentSubsystem"] = SUB_CLS
    fake.EditorSubsystem = _NS(static_class=lambda: Class("EditorSubsystem", "/Script/EditorSubsystem.EditorSubsystem"))
    fake.EngineSubsystem = _NS(static_class=lambda: Class("EngineSubsystem", "/Script/Engine.EngineSubsystem"))
    api = {"v": 8}
    fake.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: api["v"],
                              find_game_subsystem=lambda world, cls: sub if cls is SUB_CLS else None,
                              is_pure_or_const=lambda cls, fn: str(fn) in ("GetEventsSince", "GetCapabilitiesJson"))
    rec = Recorder()
    fake.MCPEventRecorder = _NS(get=lambda world: rec)
    clock = {"t": 10.0}
    fake.GameplayStatics.get_time_seconds = lambda world: clock["t"]
    ticks = []
    fake.register_slate_post_tick_callback = lambda cb: ticks.append(cb) or len(ticks)
    fake.unregister_slate_post_tick_callback = lambda h: ticks.__setitem__(h - 1, None)
    fake.rec, fake.journal, fake.clock, fake.ticks, fake.api = rec, journal, clock, ticks, api
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, op, args):
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


JOURNAL = {"class": "/Script/Game.AgentSubsystem", "function": "GetEventsSince", "capabilities": "GetCapabilitiesJson"}


def tick(ue, dt=1.0):
    ue.clock["t"] += dt
    ue.ticks[-1](dt)


def test_events_session_drains_both_sources(v2, ue):
    ue.journal["events"] = [{"seq": 1, "t": 9.0, "kind": "wave_start"}]  # before the window: kept
    env = call(v2, "events_start", {"session": "p1", "journal": JOURNAL})
    assert env["ok"] and env["result"]["sources"] == {"server": "recorded", "engine": "recorded", "journal": "recorded"}, env
    assert env["result"]["bound"] == 12 and ue.rec.recording
    ue.rec.events = [{"seq": i, "t": 10.0 + i, "kind": "damage"} for i in range(1, 6)]  # 5: three pages
    ue.journal["events"].append({"seq": 2, "t": 11.0, "kind": "kill"})
    tick(ue)
    ue.journal["events"].append({"seq": 3, "t": 12.5, "kind": "death"})
    env = call(v2, "events_stop", {"session": "p1"})
    out = env["result"]
    assert env["ok"] and [e["kind"] for e in out["events"]].count("damage") == 5, env
    assert {e["kind"] for e in out["events"] if e["source"] == "journal"} == {"wave_start", "kill", "death"}
    assert out["gaps"] == [] and out["start_t"] == 10.0 and out["end_t"] == 11.0
    assert [e["t"] for e in out["events"]] == sorted(e["t"] for e in out["events"])
    assert out["engine"]["still_bound"] == 0 and ue.rec.stopped == 1 and ue.ticks[-1] is None
    assert call(v2, "events_stop", {"session": "p1"})["code"] == "NOT_FOUND"


def test_lost_events_become_gaps(v2, ue):
    call(v2, "events_start", {"session": "p2", "journal": JOURNAL})
    # The ring overwrote seqs 1-3 before a drain read them; the journal reports a gap.
    ue.rec.events = [{"seq": 4, "t": 10.5, "kind": "damage"}]
    ue.journal["gap_on"] = "e:0"
    tick(ue, 2.0)
    out = call(v2, "events_stop", {"session": "p2"})["result"]
    gaps = {g["source"]: g for g in out["gaps"]}
    assert gaps["engine"]["dropped"] == 3 and gaps["engine"]["from_t"] == 10.0 and gaps["engine"]["to_t"] == 12.0, out
    assert gaps["journal"]["dropped"] == 9


def test_old_plugin_and_no_game_api_are_unavailable_not_failures(v2, ue):
    ue.api["v"] = 7
    env = call(v2, "events_start", {"session": "p3", "journal_why": "no game_api in .umcp.json"})
    out = env["result"]
    assert env["ok"] and out["sources"]["engine"] == "unavailable" and "API is 7" in out["source_why"]["engine"], env
    assert out["sources"]["journal"] == "unavailable" and out["source_why"]["journal"] == "no game_api in .umcp.json"
    assert not ue.rec.recording
    assert call(v2, "events_start", {"session": "p3"})["code"] == "CONFLICT"
    assert call(v2, "events_stop", {"session": "p3"})["ok"]


def test_a_failed_drain_is_a_gap(v2, ue):
    call(v2, "events_start", {"session": "p4", "journal": JOURNAL})

    def boom(cursor, n):
        raise Exception("recorder gone")

    ue.rec.drain_events_json = boom
    tick(ue)
    out = call(v2, "events_stop", {"session": "p4"})["result"]
    assert any(g["source"] == "engine" and "recorder gone" in g["reason"] for g in out["gaps"]), out


def test_pie_ending_first_keeps_what_was_drained(v2, ue):
    call(v2, "events_start", {"session": "p5", "journal": JOURNAL})
    ue.rec.events = [{"seq": 1, "t": 10.5, "kind": "damage"}]
    tick(ue)
    ue.pie_actors = None  # PIE ended: the recorder unbound itself with its game instance
    tick(ue)
    out = call(v2, "events_stop", {"session": "p5"})["result"]
    assert [e["kind"] for e in out["events"]] == ["damage"] and out["engine"] == {"stopped_with": "pie_end"}, out
    assert any("PIE ended" in g["reason"] for g in out["gaps"]) and ue.rec.stopped == 0


def test_needs_pie_and_a_read_only_journal(v2, ue):
    ue.pie_actors = None
    assert call(v2, "events_start", {"session": "p6"})["code"] == "NOT_IN_PIE"
    ue.pie_actors = []
    bad = dict(JOURNAL, function="Mutate")
    env = call(v2, "events_start", {"session": "p6", "journal": bad})
    assert env["code"] == "BAD_VALUE" and "not BlueprintPure" in env["error"] and not ue.rec.recording, env
    assert call(v2, "events_start", {"session": "../x"})["code"] == "BAD_VALUE"


def test_seed_random_seeds_the_engine_streams(v2, ue):
    seeds = []
    ue.MCPCoreLibrary.seed_random_streams = seeds.append
    env = call(v2, "seed_random", {"seed": 42})
    assert env["ok"] and seeds == [42], env
    for bad in (True, 1.5, 2**31, "7"):
        assert call(v2, "seed_random", {"seed": bad})["code"] == "BAD_VALUE", bad
    ue.api["v"] = 7
    assert call(v2, "seed_random", {"seed": 1})["code"] == "PLUGIN_MISSING"
    ue.api["v"] = 8
    ue.pie_actors = None
    assert call(v2, "seed_random", {"seed": 1})["code"] == "NOT_IN_PIE" and seeds == [42]



def test_session_reports_the_declared_kinds_and_refuses_a_second_session(v2, ue):
    env = call(v2, "events_start", {"session": "k1", "journal": JOURNAL})
    assert env["result"]["journal_kinds"] == ["death", "hit", "kill", "sfx", "wave_start"], env
    env = call(v2, "events_start", {"session": "k2", "journal": JOURNAL})
    assert env["code"] == "CONFLICT" and ue.rec.generation == 1, env  # the recorder was not restarted under k1
    out = call(v2, "events_stop", {"session": "k1"})["result"]
    assert out["journal_kinds"] == ["death", "hit", "kill", "sfx", "wave_start"]


def test_journal_events_settle_before_they_are_read(v2, ue):
    # A hit's visual_t is stamped up to 1 s later: an event younger than that is read again later.
    call(v2, "events_start", {"session": "s1", "journal": JOURNAL})
    hit = {"seq": 1, "t": 10.6, "kind": "hit"}
    ue.journal["events"] = [hit]
    tick(ue, 1.0)  # now 11.0: the hit is 0.4 s old
    hit["data"] = {"visual_t": 10.7}  # the game stamps it afterwards
    tick(ue, 1.0)  # now 12.0: settled, read with its stamp
    out = call(v2, "events_stop", {"session": "s1"})["result"]
    hits = [e for e in out["events"] if e["kind"] == "hit"]
    assert len(hits) == 1 and hits[0]["data"] == {"visual_t": 10.7} and out["gaps"] == [], out


def test_a_gap_before_the_window_is_not_a_gap(v2, ue):
    ue.journal["gap_on"] = ""  # the journal lost events before the session started
    out = call(v2, "events_start", {"session": "b1", "journal": JOURNAL})["result"]
    out = call(v2, "events_stop", {"session": "b1"})["result"]
    assert out["gaps"] == [] and out["before_window"][0]["source"] == "journal", out


def test_a_lost_world_is_an_engine_gap_to_the_end(v2, ue):
    call(v2, "events_start", {"session": "w1", "journal": JOURNAL})
    ue.rec.events = [{"seq": 1, "t": 10.5, "kind": "damage"}]
    tick(ue)
    ue.rec.lost_t = 11.5  # a map travel tore the recorded world down
    tick(ue)
    tick(ue, 5.0)
    out = call(v2, "events_stop", {"session": "w1"})["result"]
    # The recording ends where the world went: both sources lost the rest (the new world's
    # clock is not this one's), nothing after is drained, the window ends at detection.
    assert out["end_t"] == 12.0 and {(g["source"], g["from_t"], g["to_t"]) for g in out["gaps"]} == {
        ("engine", 11.5, 12.0), ("journal", 10.0, 12.0)}, out  # the journal was taken up to 10.0 only
    assert ue.ticks[-1] is None


def test_a_journal_world_change_or_a_clock_going_back_ends_the_recording(v2, ue):
    call(v2, "events_start", {"session": "w2", "journal": JOURNAL})
    tick(ue)
    ue.journal["gap_on"] = "e:0"
    real = ue.journal.get("reason")
    m = v2["_mcp2"]
    sub = m._MCP_EVENT_SESSIONS["w2"]["journal"]["obj"]
    since = sub.functions["GetEventsSince"]
    sub.functions["GetEventsSince"] = lambda c: json.dumps(dict(json.loads(since(c)), gap=True, reason="world_changed"))
    tick(ue)
    out = call(v2, "events_stop", {"session": "w2"})["result"]
    assert all("world changed" in g["reason"] for g in out["gaps"]) and len(out["gaps"]) == 2 and real is None, out
    call(v2, "events_start", {"session": "w3", "journal": dict(JOURNAL, function="GetEventsSince")})
    sub.functions["GetEventsSince"] = since
    ue.journal["gap_on"] = None
    tick(ue, 2.0)
    ue.clock["t"] = 0.5  # RestartLevel: a new world, its clock from 0
    ue.ticks[-1](0.6)
    out = call(v2, "events_stop", {"session": "w3"})["result"]
    assert out["end_t"] == 14.0 and all("clock went back" in g["reason"] for g in out["gaps"]) and len(out["gaps"]) == 2, out


def test_a_recorder_restarted_elsewhere_is_a_gap(v2, ue):
    call(v2, "events_start", {"session": "g1", "journal": JOURNAL})
    ue.rec.generation += 1  # someone else restarted the engine recorder
    tick(ue)
    out = call(v2, "events_stop", {"session": "g1"})["result"]
    assert any(g["source"] == "engine" and "restarted" in g["reason"] for g in out["gaps"]), out


def test_a_full_session_is_a_gap_on_every_source_until_the_end(v2, ue):
    m = v2["_mcp2"]
    old = m._EVENTS_MAX
    m._EVENTS_MAX = 3
    try:
        call(v2, "events_start", {"session": "f1", "journal": JOURNAL})
        ue.rec.events = [{"seq": i, "t": 10.0, "kind": "damage"} for i in range(1, 6)]
        tick(ue)
        ue.journal["events"] = [{"seq": 1, "t": 10.0, "kind": "kill"}]
        tick(ue, 3.0)
        out = call(v2, "events_stop", {"session": "f1"})["result"]
    finally:
        m._EVENTS_MAX = old
    gaps = {g["source"]: g for g in out["gaps"]}
    assert set(gaps) >= {"engine", "journal"} and gaps["journal"]["to_t"] == out["end_t"] == 14.0, out


def test_pie_end_unregisters_the_tick(v2, ue):
    call(v2, "events_start", {"session": "e1", "journal": JOURNAL})
    ue.pie_actors = None
    tick(ue)  # the drain sees PIE gone
    cb = ue.ticks[-1]
    tick(ue)  # the next tick unregisters itself
    assert ue.ticks[-1] is None and cb is not None
    out = call(v2, "events_stop", {"session": "e1"})["result"]
    assert {g["source"] for g in out["gaps"]} == {"engine", "journal"}



def test_gaps_start_where_a_source_was_last_read_completely(v2, ue):
    # The journal's newest second is held back: a world change loses it, so the gap starts there.
    call(v2, "events_start", {"session": "t1", "journal": JOURNAL})
    tick(ue, 2.0)  # now 12: the journal is taken up to 11
    ue.journal["events"] = [{"seq": 1, "t": 11.6, "kind": "hit"}]  # held back
    tick(ue, 0.5)  # now 12.5: settle 11.5, the hit is still held
    ue.clock["t"] = 0.2  # restart: the held hit is gone with its world
    ue.ticks[-1](0.6)
    out = call(v2, "events_stop", {"session": "t1"})["result"]
    j = [g for g in out["gaps"] if g["source"] == "journal"][0]
    assert j["from_t"] == 11.5 and j["to_t"] == 12.5 and not any(e["kind"] == "hit" for e in out["events"]), out


def test_a_full_session_gap_starts_at_the_first_dropped_event(v2, ue):
    m = v2["_mcp2"]
    old = m._EVENTS_MAX
    m._EVENTS_MAX = 2
    try:
        call(v2, "events_start", {"session": "c1", "journal": JOURNAL})
        ue.rec.events = [{"seq": i, "t": 10.0 + 0.1 * i, "kind": "damage"} for i in range(1, 6)]
        tick(ue)
        out = call(v2, "events_stop", {"session": "c1"})["result"]
    finally:
        m._EVENTS_MAX = old
    g = {x["source"]: x for x in out["gaps"]}
    assert abs(g["engine"]["from_t"] - 10.3) < 1e-9 and g["journal"]["from_t"] == 10.0, out


def test_capabilities_are_read_only_and_their_failure_is_said(v2, ue):
    ue.MCPCoreLibrary.is_pure_or_const = lambda cls, fn: str(fn) == "GetEventsSince"
    out = call(v2, "events_start", {"session": "p1", "journal": JOURNAL})["result"]
    assert out["journal_kinds"] is None and "not BlueprintPure" in out["source_why"]["journal_kinds"], out
    call(v2, "events_stop", {"session": "p1"})


def test_a_session_whose_world_is_gone_is_evicted(v2, ue):
    call(v2, "events_start", {"session": "old", "journal": JOURNAL})
    m = v2["_mcp2"]
    m._MCP_EVENT_SESSIONS["old"]["world"] = "OLD_PIE"  # its stop never came; PIE restarted
    env = call(v2, "events_start", {"session": "new", "journal": JOURNAL})
    assert env["ok"] and env["result"]["evicted"] == ["old"], env
    call(v2, "events_stop", {"session": "new"})


def test_a_journal_without_next_cursor_is_a_gap(v2, ue):
    call(v2, "events_start", {"session": "n1", "journal": JOURNAL})
    sub = v2["_mcp2"]._MCP_EVENT_SESSIONS["n1"]["journal"]["obj"]
    sub.functions["GetEventsSince"] = lambda c: json.dumps({"events": [{"seq": 1, "t": 10.9, "kind": "hit"}], "gap": True})
    tick(ue, 1.0)
    out = call(v2, "events_stop", {"session": "n1"})["result"]
    assert any("next_cursor" in g["reason"] for g in out["gaps"]), out
