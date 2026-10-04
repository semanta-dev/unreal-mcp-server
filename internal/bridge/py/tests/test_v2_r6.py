"""R6 companion ops: rollback — snapshot properties, sphere_overlap object types."""
import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Fake, installed


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = []
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, op, args):
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


def test_sphere_overlap_sees_every_object_type(v2, ue):
    # R6.3: the overlap asked for WorldStatic only, so a pawn standing in it was invisible.
    ue.ObjectTypeQuery = _NS(**{"OBJECT_TYPE_QUERY%d" % i: "OT%d" % i for i in range(1, 7)})
    ue.add_class("Pawn", "/Script/Engine.Pawn")
    pawn = ue.add_actor("/Script/Engine.Pawn", "Hero", world="pie")
    wall = ue.add_actor("/Script/Engine.StaticMeshActor", "Wall", world="pie")
    asked = []

    def overlap(world, center, radius, types, cls, ignore):
        asked.append(list(types))
        out = []
        if "OT1" in types:
            out.append(wall)
        if "OT3" in types:
            out.append(pawn)
        return out

    ue.SystemLibrary.sphere_overlap_actors = overlap
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie"})
    r = env["result"] if "result" in env else env
    assert sorted(r["actors"]) == ["Hero", "Wall"] and asked[-1] == ["OT%d" % i for i in range(1, 7)], env
    assert {h["class"] for h in r["hits"]} >= {"Pawn"}
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie", "object_types": ["pawn"]})
    r = env["result"] if "result" in env else env
    assert r["actors"] == ["Hero"] and asked[-1] == ["OT3"], env
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie", "object_types": ["pawns"]})
    assert env.get("code") == "BAD_VALUE" and "pawns" in env["error"], env


class EnumBase:
    pass


class EGait(EnumBase):
    def __init__(self, name):
        self.name = name


EGait.RUN, EGait.WALK = EGait("RUN"), EGait("WALK")


def test_snapshot_properties_round_trip(v2, ue):
    # R6.1: a snapshot keeps chosen properties with their types; restore sets them back.
    ue.EnumBase, ue.EGait = EnumBase, EGait
    a = ue.add_actor("/Script/Engine.Actor", "Turret")
    b = ue.add_actor("/Script/Engine.Actor", "Wall")
    a.props.update({"Health": 80.0, "bArmed": True, "Gait": EGait.RUN, "AimOffset": ue.Vector(1, 2, 3)})
    b.props.update({"Health": 500.0})
    res = call(v2, "snapshot_actors", {"properties": ["Health", "bArmed", "Gait", "AimOffset", "Nope"]})["result"]
    rows = {r["label"]: r for r in res["actors"]}
    assert rows["Turret"]["props"]["Health"] == {"t": "float", "v": 80.0}
    assert rows["Turret"]["props"]["Gait"] == {"t": "enum", "enum": "EGait", "v": "RUN"}
    assert rows["Turret"]["props"]["AimOffset"] == {"t": "Vector", "v": [1.0, 2.0, 3.0]}
    assert set(rows["Wall"]["props"]) == {"Health"} and res["properties_missing"] == ["Nope"], res
    # The level changes; restore puts every recorded value back in the one transaction.
    a.props.update({"Health": 5.0, "bArmed": False, "Gait": EGait.WALK, "AimOffset": ue.Vector(0, 0, 0)})
    b.props["Health"] = 1.0
    out = call(v2, "snapshot_restore", {"name": "s", "actors": res["actors"], "save": False})["result"]
    assert out["properties_restored"] == 5 and "property_errors" not in out, out
    assert a.props["Health"] == 80.0 and a.props["bArmed"] is True and a.props["Gait"] is EGait.RUN and b.props["Health"] == 500.0
    assert (a.props["AimOffset"].x, a.props["AimOffset"].z) == (1.0, 3.0)
    # A value that cannot be set back is reported, the others still restored.
    a.readonly.add("bArmed")
    a.props["bArmed"] = False
    out = call(v2, "snapshot_restore", {"name": "s", "actors": res["actors"], "save": False})["result"]
    assert out["property_errors"][0]["property"] == "bArmed" and out["properties_restored"] == 4, out


def test_snapshot_refuses_a_type_it_cannot_restore(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "Box")
    a.props["Weird"] = object()
    res = call(v2, "snapshot_actors", {"properties": ["Weird"]})["result"]
    assert res["property_errors"][0]["property"] == "Weird" and "cannot be restored" in res["property_errors"][0]["error"]
    assert "props" not in res["actors"][0]


def test_open_level_save_false_never_saves_or_asks(v2, ue):
    saved, loaded = [], []
    ue.EditorLoadingAndSavingUtils = _NS(save_dirty_packages=lambda maps, content: saved.append(1) or True,
                                         get_dirty_map_packages=lambda: [_NS(get_name=lambda: "/Game/Maps/L_Arena")],
                                         get_dirty_content_packages=lambda: [])
    ue.LevelEditorSubsystem = "LES"
    real = ue.get_editor_subsystem
    ue.get_editor_subsystem = lambda c: _NS(load_level=lambda p: loaded.append(p) or True) if c == "LES" else real(c)
    env = call(v2, "open_level", {"level_path": "/Game/Maps/L_Other", "save": False})
    assert env["code"] == "PRECONDITION" and env["details"]["unsaved"] == ["/Game/Maps/L_Arena"] and not saved and not loaded, env
    ue.EditorLoadingAndSavingUtils.get_dirty_map_packages = lambda: []
    assert call(v2, "open_level", {"level_path": "/Game/Maps/L_Other", "save": False})["ok"] and loaded and not saved
    assert call(v2, "open_level", {"level_path": "/Game/Maps/L_Arena"})["ok"] and saved  # default: save first


def test_a_dry_run_is_not_an_edit(v2, ue):
    # The undo journal: a dry run changed nothing, so it must not block undo.
    m = v2["_mcp2"]
    del m._MCP_EDITS[:]
    m._note_op("asset_create", {"dry_run": True})
    m._note_op("data_add_variable", {"dry_run": True})
    assert m._MCP_EDITS == []
    m._note_op("asset_create", {})
    assert m._MCP_EDITS == [("untracked", "asset_create")]
