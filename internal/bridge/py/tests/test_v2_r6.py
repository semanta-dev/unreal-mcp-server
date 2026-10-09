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
    r = env["result"]
    assert sorted(r["actors"]) == ["Hero", "Wall"] and asked[-1] == ["OT%d" % i for i in range(1, 7)], env
    assert {h["class"] for h in r["hits"]} >= {"Pawn"}
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie", "object_types": ["pawn"]})
    r = env["result"]
    assert r["actors"] == ["Hero"] and asked[-1] == ["OT3"], env
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 9, object_type_by_channel_name=lambda n: 0)
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie", "object_types": ["pawns"]})
    assert env.get("code") == "BAD_VALUE" and "pawns" in env["error"], env


class EnumBase:
    pass


class EGait(EnumBase):
    def __init__(self, name):
        self.name = name


EGait.RUN, EGait.WALK = EGait("RUN"), EGait("WALK")


class NameT(str):
    """unreal.Name: a str subclass in name only."""


class TextT(str):
    pass


class Struct:
    FIELDS = ()

    def __init__(self, **kw):
        for f in self.FIELDS:
            setattr(self, f, kw.get(f, 0.0))

    def __eq__(self, o):
        return type(o) is type(self) and all(getattr(o, f) == getattr(self, f) for f in self.FIELDS)


class Color(Struct):
    FIELDS = ("b", "g", "r", "a")  # stored B, G, R, A like FColor: built by name, never by position


class LinearColor(Struct):
    FIELDS = ("r", "g", "b", "a")


@pytest.fixture
def types(ue):
    ue.EnumBase, ue.EGait, ue.Name, ue.Text, ue.Color, ue.LinearColor = EnumBase, EGait, NameT, TextT, Color, LinearColor
    ue.Vector2D = type("Vector2D", (), {})
    ue.pie_actors = None  # editor edits: no PIE
    ue.not_settable = {}  # (label, property) -> why the engine would refuse a set
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 9,
                            why_not_settable=lambda a, n: ue.not_settable.get((a.get_actor_label(), n), ""))
    return ue


def test_every_kept_type_round_trips(v2, types):
    ue = types
    target = ue.add_actor("/Script/Engine.Actor", "Target")
    a = ue.add_actor("/Script/Engine.Actor", "Turret")
    ue.classes[target.get_path_name()] = target  # load_object finds the level actor by path
    vals = {"Health": 80.5, "Ammo": 7, "Seed": 2 ** 60 + 1, "bArmed": True, "Gait": EGait.RUN, "Tag": NameT("Boss"),
            "Title": TextT("Gate"), "Note": "plain", "AimOffset": ue.Vector(1, 2, 3), "Facing": ue.Rotator(5, 10, 20),
            "Tint": Color(r=255, g=128, b=0, a=255), "Glow": LinearColor(r=0.5, g=0.25, b=1.0, a=1.0), "Follow": target,
            "Owner2": None}
    a.props.update(vals)
    res = call(v2, "snapshot_actors", {"properties": list(vals)})["result"]
    assert res["property_errors"] == [] and res["properties_missing"] == [], res
    props = {r["label"]: r for r in res["actors"]}["Turret"]["props"]
    assert props["Seed"] == {"t": "int", "v": str(2 ** 60 + 1)} and props["Tag"]["t"] == "name" and props["Title"]["t"] == "text"
    assert props["Tint"] == {"t": "Color", "v": [255, 128, 0, 255]} and props["Follow"] == {"t": "object", "v": target.get_path_name()}
    for k in vals:  # the level changes everywhere
        a.props[k] = None
    out = call(v2, "snapshot_restore", {"name": "s", "actors": res["actors"], "save": False})["result"]
    assert out["properties_restored"] == len(vals), out
    for k, v in vals.items():
        got = a.props[k]
        if k == "Facing":
            assert (got.pitch, got.yaw, got.roll) == (10, 20, 5)
        elif k == "AimOffset":
            assert (got.x, got.y, got.z) == (1, 2, 3)
        else:
            assert got == v and type(got) is type(v), (k, got, v)


def test_take_refuses_what_would_not_come_back(v2, types):
    ue = types
    a = ue.add_actor("/Script/Engine.Actor", "Box")
    a.props.update({"Weird": object(), "Bad": float("nan"), "Ghost": EGait("NOPE")})
    ue.EGait.NOPE = None
    res = call(v2, "snapshot_actors", {"properties": ["Weird", "Bad", "Ghost"]})["result"]
    errs = {e["property"]: e["error"] for e in res["property_errors"]}
    assert set(errs) == {"Weird", "Bad", "Ghost"} and "props" not in res["actors"][0], res
    assert "finite" in errs["Bad"] and "Ghost" in errs
    # A property the engine would not let a restore set (VisibleAnywhere, EditConst...).
    a.props["Armor"] = 5.0
    ue.not_settable[("Box", "Armor")] = "read-only (EditConst)"
    res = call(v2, "snapshot_actors", {"properties": ["Armor"]})["result"]
    assert res["property_errors"][0]["error"] == "cannot be set back: read-only (EditConst)", res
    # A read that fails for another reason than "no such property" is an error, not a skip.
    real = a.get_editor_property
    a.get_editor_property = lambda k: (_ for _ in ()).throw(Exception("access denied")) if k == "Secret" else real(k)
    res = call(v2, "snapshot_actors", {"properties": ["Secret"]})["result"]
    assert res["property_errors"][0]["error"].startswith("unreadable") and res["properties_missing"] == [], res
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 8)
    assert call(v2, "snapshot_actors", {"properties": ["Armor"]})["code"] == "PLUGIN_MISSING"
    assert call(v2, "snapshot_actors", {})["ok"]  # without properties: no plugin needed


def test_restore_is_all_or_nothing(v2, types):
    ue = types
    a = ue.add_actor("/Script/Engine.Actor", "Turret")
    b = ue.add_actor("/Script/Engine.Actor", "Wall")
    a.props.update({"Health": 80.0, "bArmed": True})
    b.props.update({"Health": 500.0})
    snap = call(v2, "snapshot_actors", {"properties": ["Health", "bArmed"]})["result"]["actors"]
    a.props.update({"Health": 5.0, "bArmed": False})
    b.props["Health"] = 1.0
    a.loc = ue.Vector(9, 9, 9)
    # A value the engine refuses to set (read-only): everything this restore did is put back.
    b.readonly.add("Health")
    env = call(v2, "snapshot_restore", {"name": "s", "actors": snap})
    assert env["code"] == "EDITOR_ERROR" and "nothing was restored" in env["error"], env
    assert a.props == {"Health": 5.0, "bArmed": False} and (a.loc.x, a.loc.y) == (9, 9) and b.props["Health"] == 1.0
    # A put-back that fails too is said (half-restored), never "nothing was restored".
    real_set = a.set_editor_property
    a.set_editor_property = lambda k, v: (_ for _ in ()).throw(Exception("locked")) if v == 5.0 else real_set(k, v)
    env = call(v2, "snapshot_restore", {"name": "s", "actors": snap})
    assert env["code"] == "EDITOR_ERROR" and "half-restored" in env["error"] and env["details"]["not_reverted"], env
    a.set_editor_property = real_set
    a.props["Health"] = 5.0
    # A stored value that cannot be rebuilt now (its object is gone): refused before any change.
    b.readonly.clear()
    snap[0]["props"]["Health"] = {"t": "object", "v": "/Game/Gone.Gone"}
    env = call(v2, "snapshot_restore", {"name": "s", "actors": snap})
    assert env["code"] == "PRECONDITION" and env["details"]["property_errors"][0]["property"] == "Health", env
    assert a.props["Health"] == 5.0 and a.modified == 2  # only the two failed transactions above touched it


def test_restore_refused_during_pie(v2, ue):
    ue.pie_actors = []
    assert call(v2, "snapshot_restore", {"name": "s", "actors": []})["code"] == "PRECONDITION"


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


def test_level_revert_drops_unsaved_changes_of_a_saved_level(v2, ue):
    ue.pie_actors = None
    loaded = []
    ue.EditorLoadingAndSavingUtils = _NS(get_dirty_map_packages=lambda: [_NS(get_name=lambda: "/Game/Maps/L_Arena")],
                                         get_dirty_content_packages=lambda: [_NS(get_name=lambda: "/Game/Data/DA_X")])
    ue.LevelEditorSubsystem, ue.UnrealEditorSubsystem = "LES", "UES"
    real = ue.get_editor_subsystem
    world = _NS(get_path_name=lambda: "/Game/Maps/L_Arena.L_Arena")
    ues = _NS(get_editor_world=lambda: world, get_game_world=lambda: "PIE" if ue.pie_actors is not None else None)
    ue.get_editor_subsystem = lambda c: (_NS(load_level=lambda p: loaded.append(p) or True) if c == "LES"
                                         else ues if c == "UES" else real(c))
    ue.EditorAssetLibrary.does_asset_exist = lambda p: p == "/Game/Maps/L_Arena"
    env = call(v2, "level_revert", {})
    assert env["ok"] and loaded == ["/Game/Maps/L_Arena"] and env["result"]["discarded"] == ["/Game/Maps/L_Arena"], env
    assert env["result"]["content_unsaved"] == ["/Game/Data/DA_X"]
    # A level never saved has nothing on disk to go back to.
    world = _NS(get_path_name=lambda: "/Temp/Untitled_1.Untitled_1")
    assert call(v2, "level_revert", {})["code"] == "PRECONDITION" and loaded == ["/Game/Maps/L_Arena"]
    ue.pie_actors = []
    assert call(v2, "level_revert", {"level_path": "/Game/Maps/L_Arena"})["code"] == "PRECONDITION"


def test_sphere_overlap_takes_a_project_channel_by_name(v2, ue):
    ue.ObjectTypeQuery = _NS(**{"OBJECT_TYPE_QUERY%d" % i: "OT%d" % i for i in range(1, 33)})
    asked = []
    ue.SystemLibrary.sphere_overlap_actors = lambda w, c, r, types, cls, ig: asked.append(list(types)) or []
    # The project's collision settings: an object channel "Enemy" (type 7), a trace channel "Interact".
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 9, object_type_by_channel_name=lambda n: {"Enemy": 7}.get(n, 0))
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie", "object_types": ["pawn", "Enemy"]})
    assert env["ok"] and asked[-1] == ["OT3", "OT7"], env
    env = call(v2, "world_query", {"kind": "sphere_overlap", "center": [0, 0, 0], "world": "pie", "object_types": ["Interact"]})
    assert env["code"] == "BAD_VALUE" and "trace channel" in env["error"], env


def test_only_implemented_dry_runs_skip_the_journal(v2, ue):
    m = v2["_mcp2"]
    del m._MCP_EDITS[:]
    m._note_op("widget_compose", {"dry_run": True})  # does not implement dry_run: a real edit
    assert m._MCP_EDITS == [("untracked", "widget_compose")]


def test_revert_is_the_open_level_only(v2, ue):
    ue.pie_actors = None
    world = _NS(get_path_name=lambda: "/Game/Maps/L_Arena.L_Arena")
    ue.UnrealEditorSubsystem = "UES"
    real = ue.get_editor_subsystem
    ue.get_editor_subsystem = lambda c: _NS(get_editor_world=lambda: world, get_game_world=lambda: None) if c == "UES" else real(c)
    env = call(v2, "level_revert", {"level_path": "/Game/Maps/L_Other"})
    assert env["code"] == "BAD_VALUE" and "open level" in env["error"], env


def test_input_mapping_read_lists_each_actions_keys(v2, ue):
    # data_query op=input_mapping: the final eval read IMC_Aesir's mappings with python.
    def mapping(action, key):
        return _NS(get_editor_property=lambda k: {"action": action, "key": _NS(get_editor_property=lambda n: key)}[k])

    dash = _NS(get_path_name=lambda: "/Game/Input/IA_Dash.IA_Dash")
    fire = _NS(get_path_name=lambda: "/Game/Input/IA_Fire.IA_Fire")
    maps = [mapping(dash, "LeftShift"), mapping(fire, "LeftMouseButton"), mapping(dash, "Gamepad_FaceButton_Right")]
    imc_cls = type("InputMappingContext", (), {})
    ue.InputMappingContext = imc_cls
    imc = imc_cls()
    imc.get_editor_property = lambda k: {"default_key_mappings": _NS(get_editor_property=lambda n: maps)}[k]
    ue.assets["/Game/Input/IMC_Aesir"] = imc
    env = call(v2, "data_input_mapping_read", {"asset": "/Game/Input/IMC_Aesir"})
    assert env.get("ok"), env
    assert env["result"]["mappings"] == [{"action": "/Game/Input/IA_Dash", "keys": ["LeftShift", "Gamepad_FaceButton_Right"]},
                                         {"action": "/Game/Input/IA_Fire", "keys": ["LeftMouseButton"]}], env
