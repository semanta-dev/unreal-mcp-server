"""P5c op bodies against the stateful fake: strict world vocabulary, snapshots (incl.
World Partition), path-matched restore, tag-scoped scenes and server-owned paths."""
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
    assert d.startswith(str(tmp_path)) and d.endswith("WidgetRenders")


def test_audio_is_pie_only(v2, ue):
    assert err(v2, "audio_capture_start", {})["code"] == "NOT_IN_PIE"
    assert err(v2, "play_test_sound", {"sound": "/Game/S"})["code"] == "NOT_IN_PIE"


def test_filtered_snapshot_has_no_wp_unloaded(v2, ue):
    ue.add_actor("/Script/Engine.Actor", "Lamp")
    ue.add_actor("/Script/Engine.StaticMeshActor", "Rock")
    ue.wp_descs = [a.get_path_name() for a in ue.editor_actors] + ["/Game/Maps/L.L:PersistentLevel.Far_9"]
    res = ok(v2, "snapshot_actors", {"class_filter": "Static"})
    assert [a["label"] for a in res["actors"]] == ["Rock"] and res["class_filter"] == "Static"
    assert "unloaded" not in res  # Lamp is filtered out, not unloaded
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
