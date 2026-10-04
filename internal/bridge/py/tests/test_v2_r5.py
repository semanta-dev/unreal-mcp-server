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

    def start_recording(self, capacity):
        self.recording = True
        return json.dumps({"ok": True, "bound": 12, "capacity": capacity})

    def stop_recording(self):
        self.recording, self.stopped = False, self.stopped + 1
        return json.dumps({"ok": True, "unbound": 12, "still_bound": 0})

    def drain_events_json(self, cursor, max_events):
        after = [e for e in self.events if e["seq"] > cursor]
        oldest = self.events[0]["seq"] if self.events else cursor + 1
        lost = max(0, oldest - (cursor + 1))
        page = after[:2]  # pages of 2: the drain must keep reading while more
        nxt = page[-1]["seq"] if page else cursor
        return json.dumps({"events": page, "next_cursor": nxt, "gap": lost > 0, "dropped": lost,
                           "more": len(after) > len(page)})


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

    sub = Obj("AgentSubsystem_0", SUB_CLS, functions={"GetEventsSince": since})
    fake.classes["/Script/Game.AgentSubsystem"] = SUB_CLS
    fake.EditorSubsystem = _NS(static_class=lambda: Class("EditorSubsystem", "/Script/EditorSubsystem.EditorSubsystem"))
    fake.EngineSubsystem = _NS(static_class=lambda: Class("EngineSubsystem", "/Script/Engine.EngineSubsystem"))
    api = {"v": 8}
    fake.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: api["v"],
                              find_game_subsystem=lambda world, cls: sub if cls is SUB_CLS else None,
                              is_pure_or_const=lambda cls, fn: str(fn) == "GetEventsSince")
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


JOURNAL = {"class": "/Script/Game.AgentSubsystem", "function": "GetEventsSince"}


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
