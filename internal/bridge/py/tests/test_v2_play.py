"""P5c op bodies against the stateful fake: strict world vocabulary, snapshots (incl.
World Partition), path-matched restore, tag-scoped scenes and server-owned paths."""
import time

import pytest
from conftest import run_dispatch
from fakeunreal import Fake, installed


@pytest.fixture
def ue(v2):
    fake = Fake()
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, op, args):
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


def ok(v2, op, args):
    env = call(v2, op, args)
    assert env["ok"], env
    return env["result"]


def err(v2, op, args):
    env = call(v2, op, args)
    assert not env["ok"], env
    return env


def test_pick_world_is_strict(v2, ue):
    m = v2["_mcp2"]
    assert m._pick_world("editor") == "EDITOR" and m._pick_world("pie") is None
    ue.pie_actors = []
    assert m._pick_world("game") == "PIE" and m._pick_world("auto") == "PIE"
    with pytest.raises(m._V2Error) as e:
        m._pick_world("everywhere")
    assert e.value.code == "BAD_VALUE"


def test_pie_start_stop_are_idempotent(v2, ue):
    assert ok(v2, "pie_start", {"simulate": True})["pie"] == "starting" and ue.pie_requests == ["simulate"]
    ue.pie_actors = []
    assert ok(v2, "pie_start", {})["already_running"] is True and ue.pie_requests == ["simulate"]
    assert ok(v2, "pie_stop", {})["pie"] == "stopping"
    ue.pie_actors = None
    assert ok(v2, "pie_stop", {})["already_stopped"] is True


def test_pie_observe_needs_pie_and_reports_missing(v2, ue):
    assert err(v2, "pie_observe", {})["code"] == "NOT_IN_PIE"
    ue.pie_actors = []
    ue.add_actor("/Script/Engine.Actor", "Hero", world="pie")
    m = v2["_mcp2"]
    m._reflect_observe = lambda obj, **kw: {"class": "X", "properties": {}}
    res = ok(v2, "pie_observe", {"actors": ["Hero", "Ghost"]})
    assert [a["label"] for a in res["actors"]] == ["Hero"] and res["missing"] == ["Ghost"]
    assert res["counts"] == {"Actor": 1}


def test_snapshot_actors_records_path_class_tags(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "A")
    a.tags = ["keep"]
    res = ok(v2, "snapshot_actors", {})
    assert res["world_partition"] is False and res["count"] == 1
    snap = res["actors"][0]
    assert (snap["path"], snap["label"], snap["class"], snap["tags"]) == (a.get_path_name(), "A", "Actor", ["keep"])


def test_snapshot_actors_world_partition_unloaded(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "Near")
    ue.wp_descs = [a.get_path_name(), "/Game/Maps/L.L:PersistentLevel.Far_9"]
    res = ok(v2, "snapshot_actors", {})
    assert res["world_partition"] is True and res["unloaded"] == ["/Game/Maps/L.L:PersistentLevel.Far_9"]


def test_snapshot_restore_by_path_one_undo_step(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "Twin")
    b = ue.add_actor("/Script/Engine.Actor", "Twin")  # same label: v1 matched by label
    snap = [{"path": a.get_path_name(), "loc": [1, 2, 3], "rot": [10, 20, 30], "scale": [2, 2, 2]},
            {"path": "/Game/Maps/L.L:PersistentLevel.Gone_1", "loc": [0, 0, 0]}]
    res = ok(v2, "snapshot_restore", {"name": "s", "actors": snap})
    assert res["restored"] == 1 and (a.loc.x, a.loc.y, a.loc.z) == (1, 2, 3) and b.loc.x == 0
    assert (a.rot.pitch, a.rot.yaw, a.rot.roll) == (10, 20, 30) and a.modified == 1
    assert res["not_restored"] == {"added": [b.get_path_name()], "removed": ["/Game/Maps/L.L:PersistentLevel.Gone_1"]}
    assert [k for k, _ in ue.tx] == ["begin", "end"] and res["saved"] is True


def test_filtered_snapshot_restore_reports_only_its_own_actors(v2, ue):
    wp = ue.add_actor("/Script/Engine.Actor", "WP_A")
    ue.add_actor("/Script/Engine.Actor", "Landscape")  # outside the filter: not "added"
    extra = ue.add_actor("/Script/Engine.Actor", "WP_New")
    snap = [{"path": wp.get_path_name(), "loc": [1, 1, 1]}]
    res = ok(v2, "snapshot_restore", {"name": "s", "actors": snap, "class_filter": "WP_", "save": False})
    assert res["not_restored"]["added"] == [extra.get_path_name()]


SCENE = [{"label": "arena.hero", "kind": "class", "class_path": "/Script/Engine.Actor", "location": [5, 0, 0],
          "tags": ["mcp_scene:arena"]}]


def test_scene_apply_never_adopts_a_hand_placed_actor(v2, ue):
    mine = ue.add_actor("/Script/Engine.Actor", "arena.hero")  # hand-placed, untagged
    res = ok(v2, "scene_apply", {"scene_id": "arena", "placements": SCENE})
    assert res["spawned"] == 1 and res["updated"] == 0 and "pruned" not in res
    assert mine.loc.x == 0 and mine.tags == []  # untouched
    assert any("hand-placed" in w for w in res["warnings"])
    # The scene actor gets a distinct label, so the user's label stays unambiguous.
    scene_actor = [x for x in ue.editor_actors if x is not mine][0]
    assert scene_actor.get_actor_label() == "arena.hero (scene)" and "mcp_label:arena.hero" in scene_actor.tags
    # Re-applying updates the scene's own (tagged) actor only.
    res = ok(v2, "scene_apply", {"scene_id": "arena", "placements": SCENE})
    assert res["spawned"] == 0 and res["updated"] == 1


def test_scene_prune_is_tag_scoped(v2, ue):
    ok(v2, "scene_apply", {"scene_id": "arena", "placements": SCENE + [dict(SCENE[0], label="arena.old")]})
    hand = ue.add_actor("/Script/Engine.Actor", "arena.stray")  # untagged, label looks like the scene's
    res = ok(v2, "scene_prune", {"scene_id": "arena", "keep": ["arena.hero"]})
    assert res["pruned"] == ["arena.old"] and hand in ue.editor_actors
    assert sorted(x["label"] for x in ok(v2, "scene_actors", {"scene_id": "arena"})["actors"]) == ["arena.hero"]


def test_output_names_are_confined(v2, ue):
    for bad in ("../x.png", "a/b.png", "x.exe", ""):
        assert err(v2, "take_screenshot", {"filename": bad})["code"] == "BAD_VALUE", bad
    ue.pie_actors = []
    assert err(v2, "pie_screenshot", {"filename": "..\\x.png"})["code"] == "BAD_VALUE"
    assert err(v2, "capture_start", {"session": "../../evil"})["code"] == "BAD_VALUE"
    assert err(v2, "capture_poses", {"session": "a/b", "poses": []})["code"] == "BAD_VALUE"


def test_saved_dir_is_absolute(v2, ue, tmp_path, monkeypatch):
    m = v2["_mcp2"]
    ue.Paths.convert_relative_path_to_full = lambda p: str(tmp_path / "Saved")
    d = m._saved_mcp_dir("WidgetRenders")
    assert d.startswith(tmp_path.as_posix()) and d.endswith("WidgetRenders") and "\\" not in d


def test_audio_is_pie_only(v2, ue):
    assert err(v2, "audio_capture_start", {})["code"] == "NOT_IN_PIE"
    assert err(v2, "play_test_sound", {"sound": "/Game/S"})["code"] == "NOT_IN_PIE"


def test_filtered_snapshot_lists_only_truly_unloaded_actors(v2, ue):
    # A filter must neither call a loaded-but-filtered actor (Lamp) unloaded nor drop the
    # unloaded set: without it a filtered diff reported an unloaded actor as removed
    # (found live, P7).
    ue.add_actor("/Script/Engine.Actor", "Lamp")
    ue.add_actor("/Script/Engine.StaticMeshActor", "Rock")
    ue.wp_descs = [a.get_path_name() for a in ue.editor_actors] + ["/Game/Maps/L.L:PersistentLevel.Far_9"]
    res = ok(v2, "snapshot_actors", {"class_filter": "Static"})
    assert [a["label"] for a in res["actors"]] == ["Rock"] and res["class_filter"] == "Static"
    assert res["unloaded"] == ["/Game/Maps/L.L:PersistentLevel.Far_9"]
    res = ok(v2, "snapshot_actors", {})
    assert res["unloaded"] == ["/Game/Maps/L.L:PersistentLevel.Far_9"]


def test_restore_unloaded_is_unknown_and_parents_first(v2, ue):
    parent = ue.add_actor("/Script/Engine.Actor", "Parent")
    child = ue.add_actor("/Script/Engine.Actor", "Child")
    child.parent = parent
    seq = []

    def mover(label):
        return lambda v, sweep, teleport: seq.append(label)

    parent.set_actor_location, child.set_actor_location = mover("Parent"), mover("Child")
    far = "/Game/Maps/L.L:PersistentLevel.Far_9"  # exists, but its cell is not loaded NOW
    gone = "/Game/Maps/L.L:PersistentLevel.Gone_1"
    ue.wp_descs = [parent.get_path_name(), child.get_path_name(), far]
    snap = [{"path": child.get_path_name(), "loc": [1, 1, 1]}, {"path": parent.get_path_name(), "loc": [2, 2, 2]},
            {"path": far, "loc": [0, 0, 0]}, {"path": gone, "loc": [0, 0, 0]}]
    res = ok(v2, "snapshot_restore", {"actors": snap})
    assert seq == ["Parent", "Child"]
    assert res["not_restored"]["removed"] == [gone] and res["not_restored"]["unknown"] == [far]


def test_polyworld_is_pie_only(v2, ue):
    m = v2["_mcp2"]
    seen = []
    env = call(v2, "company_status", {})
    assert not env["ok"] and env["code"] == "NOT_IN_PIE"  # v1 fell back to the EDITOR level
    ue.pie_actors = []
    wrapped = m._pie_only(lambda args: seen.append(args) or {"capital": 1})
    assert wrapped({"world": "auto"}) == {"capital": 1} and seen == [{"world": "pie"}]


# --- PIE blueprint pre-flight (found live: a modal dialog froze the editor) ------

def _auth(ue, errored):
    from fakeunreal import _Subsystem
    calls = []

    class Auth:
        def prepare_blueprints_for_pie(self, acknowledge):
            calls.append(acknowledge)
            return list(errored)

    ue.MCPAuthoringSubsystem = Auth
    ue.get_editor_subsystem = lambda which: Auth() if which is Auth else _Subsystem(ue)
    return calls


def test_pie_preflight_refuses_blueprint_errors_before_the_modal(v2, ue):
    calls = _auth(ue, ["/Game/BP_Bad.BP_Bad"])
    res = err(v2, "pie_preflight", {})
    assert res["code"] == "PRECONDITION" and res["details"]["blueprints"] == ["/Game/BP_Bad.BP_Bad"]
    assert calls == [False] and ue.pie_requests == []


def test_pie_preflight_can_acknowledge_blueprint_errors(v2, ue):
    calls = _auth(ue, ["/Game/BP_Bad.BP_Bad"])
    res = ok(v2, "pie_preflight", {"ignore_blueprint_errors": True})
    assert calls == [True] and res["blueprint_errors_ignored"] == ["/Game/BP_Bad.BP_Bad"]
    assert ok(v2, "pie_start", {})["editor_pid"] > 0  # the start itself never compiles


def test_pie_preflight_reports_what_it_compiled(v2, ue):
    from fakeunreal import _Subsystem

    class Auth:
        def prepare_blueprints_for_pie(self, acknowledge):
            return ([], 3)  # 5.7 Python returns (return value, out param)

    ue.MCPAuthoringSubsystem = Auth
    ue.get_editor_subsystem = lambda which: Auth() if which is Auth else _Subsystem(ue)
    assert ok(v2, "pie_preflight", {})["blueprints_compiled"] == 3


def test_pie_preflight_without_the_plugin_says_so(v2, ue):
    res = ok(v2, "pie_preflight", {})
    assert res["blueprint_preflight"].startswith("unavailable")


def test_pie_screenshot_is_a_fresh_absolute_name(v2, ue, tmp_path):
    # A reused name returned the previous shot; a bare name resolves against the
    # project's configurable screenshot folder (found live / gate, P7).
    shots = []
    ue.AutomationLibrary = type("A", (), {"take_high_res_screenshot": staticmethod(lambda w, h, f: shots.append(f))})
    ue.Paths.convert_relative_path_to_full = lambda p: str(tmp_path / "Saved")
    v2["_mcp2"]._pie_running = lambda: True
    a = ok(v2, "pie_screenshot", {})
    time.sleep(0.002)
    b = ok(v2, "pie_screenshot", {})
    assert a["file"] != b["file"] and shots == [a["file"], b["file"]]
    assert a["file"].startswith((tmp_path / "Saved" / "MCP" / "Screenshots").as_posix() + "/")


def test_null_args_are_no_args(v2, ue):
    # Go sends a nil map as JSON null (playtest's wait_until polled pie_observe that way
    # and every poll failed on None.get — found live, P7).
    import base64
    import io
    import json
    import sys
    ue.pie_actors = []
    v2["_mcp2"]._pie_running = lambda: True
    buf, old = io.StringIO(), sys.stdout
    sys.stdout = buf
    try:
        v2["_mcp2_dispatch"]("pie_observe", base64.b64encode(b"null").decode())
    finally:
        sys.stdout = old
    env = json.loads(buf.getvalue().split("__MCP_JSON__", 1)[1])
    assert env["ok"], env


def test_widget_render_with_an_older_plugin_is_plugin_missing(v2, ue):
    # Found live (P7): a project plugin without CaptureWidget raised a Python error.
    from fakeunreal import _Subsystem
    ue.UserWidget = ue.add_class("UserWidget", "/Script/UMG.UserWidget")
    ue.assets["/Game/UI/WBP_X"] = ue.Blueprint(ue.add_class("WBP_X_C", "/Game/UI/WBP_X.WBP_X_C", ue.UserWidget))

    class OldAuth:  # no capture_widget
        pass

    ue.MCPAuthoringSubsystem = OldAuth
    ue.get_editor_subsystem = lambda which: OldAuth() if which is OldAuth else _Subsystem(ue)
    res = err(v2, "widget_render", {"widget_class": "/Game/UI/WBP_X"})
    assert res["code"] == "PLUGIN_MISSING" and "CaptureWidget" in res["error"]
