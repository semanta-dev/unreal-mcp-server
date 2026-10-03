"""The v2 op BODIES (P5a/P5b) against a stateful fake `unreal` (tests/fakeunreal.py):
the actor_edit world x op matrix, undo transactions, actor_call, the class resolver's
branches, asset_create's overwrite rule, the compose root guard and the viewport
resolver. Each test goes through _mcp2_dispatch, so it also checks the envelope."""
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


# --- actor_query -------------------------------------------------------------------

def test_actor_query_class_filter_uses_class_is_child_of(v2, ue):
    pawn = ue.add_class("Pawn", "/Script/Engine.Pawn", ue.classes["/Script/Engine.Actor"])
    ue.add_class("Hero", "/Script/MyGame.Hero", pawn)
    ue.add_actor("/Script/Engine.Actor", "Rock")
    ue.add_actor("/Script/MyGame.Hero", "Player")
    res = ok(v2, "actor_query", {"op": "list", "class": "/Script/Engine.Pawn"})
    assert [a["label"] for a in res["actors"]] == ["Player"] and res["count"] == 1 and res["world"] == "editor"


def test_actor_query_short_class_name_and_where(v2, ue):
    ue.add_class("Hero", "/Script/MyGame.Hero", ue.classes["/Script/Engine.Actor"])
    a = ue.add_actor("/Script/MyGame.Hero", "P1")
    a.props["Team"] = "red"
    ue.add_actor("/Script/MyGame.Hero", "P2").props["Team"] = "blue"
    res = ok(v2, "actor_query", {"op": "find", "class": "Hero", "where": {"Team": "red"}, "properties": ["Team"]})
    assert [(x["label"], x["Team"]) for x in res["actors"]] == [("P1", "red")]


# --- actor_edit world x op matrix ---------------------------------------------------

def test_editor_spawn_is_one_undo_step(v2, ue):
    res = ok(v2, "actor_spawn", {"world": "editor", "class": "StaticMeshActor", "label": "Cube",
                                 "location": [1, 2, 3], "rotation": [10, 20, 30]})
    assert res["spawned"]["label"] == "Cube" and res["spawned"]["location"] == [1.0, 2.0, 3.0]
    assert res["spawned"]["rotation"] == [10.0, 20.0, 30.0]  # [pitch, yaw, roll] round-trips
    assert [k for k, _ in ue.tx] == ["begin", "end"]


def test_failed_spawn_leaves_the_level_unchanged(v2, ue):
    e = err(v2, "actor_spawn", {"world": "editor", "class": "StaticMeshActor", "static_mesh": "/Game/Nope"})
    assert e["code"] == "NOT_FOUND" and ue.editor_actors == []
    ue.assets["/Game/SM_Rock"] = object()
    # A class with no StaticMeshComponent: spawned, then destroyed before the error.
    e = err(v2, "actor_spawn", {"world": "editor", "class": "Actor", "static_mesh": "/Game/SM_Rock", "label": "X"})
    assert e["code"] == "BAD_VALUE" and ue.editor_actors == []
    res = ok(v2, "actor_spawn", {"world": "editor", "class": "StaticMeshActor", "static_mesh": "/Game/SM_Rock"})
    assert len(ue.editor_actors) == 1 and ue.editor_actors[0].comp.mesh is ue.assets["/Game/SM_Rock"]
    assert res["spawned"]["class"] == "StaticMeshActor"


def test_pie_spawn_unsupported(v2, ue):
    ue.pie_actors = []
    assert err(v2, "actor_spawn", {"world": "pie", "class": "Actor"})["code"] == "UNSUPPORTED"


def test_edit_world_must_be_explicit(v2, ue):
    for w in (None, "auto"):
        args = {"actor": "X"} if w is None else {"actor": "X", "world": w}
        assert err(v2, "actor_delete", args)["code"] == "BAD_VALUE"


def test_editor_delete_transform_set_are_undoable(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "A")
    ok(v2, "actor_transform", {"world": "editor", "actor": "A", "location": [5, 6, 7]})
    assert (a.loc.x, a.loc.y, a.loc.z) == (5, 6, 7) and a.modified == 1
    ok(v2, "actor_set_properties", {"world": "editor", "actor": "A", "properties": {"Health": 3}})
    assert a.props["Health"] == 3 and a.modified == 2
    ok(v2, "actor_delete", {"world": "editor", "actor": "A"})
    assert a.destroyed and a not in ue.editor_actors and a.modified == 3
    assert [k for k, _ in ue.tx] == ["begin", "end"] * 3


def test_pie_delete_transform_set_are_transient(v2, ue):
    ue.pie_actors = []
    a = ue.add_actor("/Script/Engine.Actor", "A", world="pie")
    ok(v2, "actor_transform", {"world": "pie", "actor": "A", "scale": [2, 2, 2]})
    ok(v2, "actor_set_properties", {"world": "pie", "actor": "A", "properties": {"Health": 1}})
    res = ok(v2, "actor_delete", {"world": "pie", "actor": "A"})
    assert a.scale.x == 2 and a.props["Health"] == 1 and a.destroyed and res["world"] == "pie"
    assert ue.tx == [] and a.modified == 0  # PIE edits are not undo transactions


def test_pie_resolves_editor_path(v2, ue):
    ue.pie_actors = []
    a = ue.add_actor("/Script/Engine.Actor", "A", world="pie")
    editor_path = a.get_path_name().replace("UEDPIE_0_", "")
    assert ok(v2, "actor_transform", {"world": "pie", "actor": editor_path, "location": [1, 1, 1]})["actor"]["label"] == "A"


def test_pie_transform_of_a_static_actor_moves_its_pie_copy(v2, ue):
    ue.pie_actors = []
    a = ue.add_actor("/Script/Engine.Actor", "Wall", world="pie")
    a.game, a.root.mobility = True, ue.ComponentMobility.STATIC
    res = ok(v2, "actor_transform", {"world": "pie", "actor": "Wall", "location": [0, 0, 1500]})
    assert a.loc.z == 1500 and res["mobility"].startswith("static->movable")
    assert a.root.mobility == ue.ComponentMobility.MOVABLE


def test_transform_that_does_not_land_is_an_error(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "Stuck")
    a.set_actor_location = lambda v, sweep, teleport: False  # e.g. attached/constrained
    res = err(v2, "actor_transform", {"world": "editor", "actor": "Stuck", "location": [9, 9, 9]})
    assert res["code"] == "EDITOR_ERROR" and "did not move" in res["error"]


def test_set_properties_all_failing_is_an_error(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "A")
    a.readonly = {"Locked"}
    e = err(v2, "actor_set_properties", {"world": "editor", "actor": "A", "properties": {"Locked": 1}})
    assert e["code"] == "BAD_VALUE" and e["details"]["property_errors"][0]["property"] == "Locked"
    res = ok(v2, "actor_set_properties", {"world": "editor", "actor": "A", "properties": {"Locked": 1, "Open": 2}})
    assert [x["property"] for x in res["property_errors"]] == ["Locked"]


# --- @pawn / @gamestate / actor_call -------------------------------------------------

def test_pawn_and_gamestate_only_in_pie(v2, ue):
    assert err(v2, "actor_query", {"op": "get", "actor": "@pawn"})["code"] == "NOT_FOUND"
    ue.pie_actors = []
    ue.pawn = ue.add_actor("/Script/Engine.Actor", "PlayerPawn", world="pie")
    res = ok(v2, "actor_query", {"op": "get", "world": "pie", "actor": "@pawn"})
    assert res["actor"]["label"] == "PlayerPawn" and res["world"] == "pie"


def test_actor_call(v2, ue):
    ue.pie_actors = []
    ue.gamestate = ue.add_actor("/Script/Engine.Actor", "GS", world="pie", functions={"GetWave": lambda: 3})
    res = ok(v2, "actor_call", {"actor": "@gamestate", "function": "GetWave"})
    assert res["result"] == 3 and res["world"] == "pie"
    assert err(v2, "actor_call", {"actor": "@gamestate", "function": "Nope"})["code"] == "NOT_FOUND"
    assert err(v2, "actor_call", {"actor": "GS", "function": "GetWave", "world": "editor"})["code"] == "UNSUPPORTED"


# --- class resolver ----------------------------------------------------------------

def test_class_resolver_branches(v2, ue):
    m = v2["_mcp2"]
    hero = ue.add_class("Hero", "/Script/MyGame.Hero")
    assert m._resolve_class_v2("Hero") is hero                     # project module
    assert m._resolve_class_v2("MyGame.Hero") is hero              # Module.Class
    plug = ue.add_class("Tool", "/Script/SomePlugin.Tool")
    assert m._resolve_class_v2("SomePlugin.Tool") is plug          # plugin module
    bp = ue.add_blueprint("/Game/BP/BP_Door", hero)
    assert m._resolve_class_v2("BP_Door") is bp.generated_class()  # Blueprint by short name
    assert m._resolve_class_v2("BP_Door_C") is bp.generated_class()
    ue.add_blueprint("/Game/Other/Hero", None)                     # a BP also named Hero
    with pytest.raises(m._V2Error) as e:
        m._resolve_class_v2("Hero")
    assert e.value.code == "CONFLICT" and len(e.value.details["candidates"]) == 2
    with pytest.raises(m._V2Error) as e:
        m._resolve_class_v2("Nothing")
    assert e.value.code == "CLASS_UNRESOLVED"


# --- asset_create ------------------------------------------------------------------

def test_asset_create_conflict_and_replace(v2, ue):
    m = v2["_mcp2"]
    made = []
    m._ASSET_KINDS["blueprint"] = lambda args, dest: (
        lambda: made.append(dest) or ue.assets.__setitem__(dest, object()) or {"created": dest})
    ue.assets["/Game/BP/BP_A"] = object()
    e = err(v2, "asset_create", {"kind": "blueprint", "dest": "/Game/BP/BP_A", "class": "Actor"})
    assert e["code"] == "CONFLICT" and made == [] and ue.deleted == []
    res = ok(v2, "asset_create", {"kind": "blueprint", "dest": "/Game/BP/BP_A", "class": "Actor", "replace": True})
    assert res["replaced"] is True and ue.deleted == ["/Game/BP/BP_A"] and made == ["/Game/BP/BP_A"]
    res = ok(v2, "asset_create", {"kind": "blueprint", "dest": "/Game/BP/BP_B", "class": "Actor"})
    assert res["replaced"] is False and res["asset"] == "/Game/BP/BP_B"


def test_replace_validates_everything_before_deleting(v2, ue):
    ue.assets["/Game/BP/BP_Hero"] = object()
    e = err(v2, "asset_create", {"kind": "blueprint", "dest": "/Game/BP/BP_Hero", "class": "BP_Heroo", "replace": True})
    assert e["code"] == "CLASS_UNRESOLVED" and ue.deleted == [] and "/Game/BP/BP_Hero" in ue.assets
    ue.assets["/Game/M/MI"] = object()
    e = err(v2, "asset_create", {"kind": "material_instance", "dest": "/Game/M/MI", "parent": "/Game/M/Nope", "replace": True})
    assert e["code"] == "NOT_FOUND" and ue.deleted == []
    ue.assets["/Game/DT/DT"] = object()
    e = err(v2, "asset_create", {"kind": "data_table", "dest": "/Game/DT/DT", "row_struct": "/Script/Game.Nope", "replace": True})
    assert e["code"] == "NOT_FOUND" and ue.deleted == []


def test_asset_create_validates_dest_and_kind(v2, ue):
    for dest in ("Game/BP/X", "/Game/BP/", "/Game/BP/X.X", "/Game"):
        assert err(v2, "asset_create", {"kind": "blueprint", "dest": dest})["code"] == "BAD_VALUE", dest
    assert err(v2, "asset_create", {"kind": "texture", "dest": "/Game/T"})["code"] == "BAD_VALUE"


def test_material_instance_requires_existing_parent(v2, ue):
    e = err(v2, "asset_create", {"kind": "material_instance", "dest": "/Game/M/MI_A", "parent": "/Game/M/Missing"})
    assert e["code"] == "NOT_FOUND"


# --- widget compose root guard ------------------------------------------------------

def test_compose_refuses_to_orphan_an_authored_root(v2, ue):
    m = v2["_mcp2"]
    ue.assets["/Game/UI/WBP"] = ue.WidgetBlueprint(None)
    ue.assets["/Game/UI/WBP"].get_editor_property = lambda k: "TREE"
    composed = []
    m._op_widget_compose = lambda args: composed.append(args) or {"digest": "d"}
    m._widget_canon = lambda wt: {"name": "Root", "class": "CanvasPanel", "children": [{"name": "Bar"}]}
    e = err(v2, "widget_compose", {"blueprint": "/Game/UI/WBP", "tree": {"name": "NewRoot", "class": "Overlay"}})
    assert e["code"] == "CONFLICT" and composed == []
    ok(v2, "widget_compose", {"blueprint": "/Game/UI/WBP", "tree": {"name": "Root", "class": "CanvasPanel"}})
    ok(v2, "widget_compose", {"blueprint": "/Game/UI/WBP", "tree": {"name": "NewRoot"}, "prune": True})
    m._widget_canon = lambda wt: {"name": "RootPanel", "class": "CanvasPanel"}  # an empty shell may be replaced
    ok(v2, "widget_compose", {"blueprint": "/Game/UI/WBP", "tree": {"name": "NewRoot"}})
    assert len(composed) == 3


# --- viewport ----------------------------------------------------------------------

def test_select_resolves_refs_strictly(v2, ue):
    a = ue.add_actor("/Script/Engine.Actor", "A")
    ue.add_actor("/Script/Engine.Actor", "B")
    res = ok(v2, "select_actors", {"actors": ["A"]})
    assert res["count"] == 1 and res["selected"][0]["path"] == a.get_path_name()
    assert err(v2, "select_actors", {"actors": ["Ghost"]})["code"] == "NOT_FOUND"  # v1 ignored it silently
    assert ok(v2, "select_actors", {"actors": ["B"], "mode": "add"})["count"] == 2
    assert ok(v2, "select_actors", {"mode": "none"})["count"] == 0
    assert err(v2, "select_actors", {"mode": "toggle"})["code"] == "BAD_VALUE"


def test_viewport_set_has_no_console_passthrough(v2, ue):
    res = ok(v2, "viewport_set", {"location": [1, 2, 3], "rotation": [-10, 90, 0], "console": ["quit"]})
    assert res["camera"]["location"] == [1.0, 2.0, 3.0] and res["camera"]["rotation"] == [-10.0, 90.0, 0.0]


# --- asset_query (registry) -----------------------------------------------------

def test_asset_search_builds_the_filter_in_its_constructor(v2, ue):
    ue.assets["/Game/BP/BP_A"] = ue.Blueprint(ue.classes["/Script/Engine.Actor"])
    res = ok(v2, "asset_query", {"package_paths": ["/Game"], "class_paths": ["/Script/Engine.Blueprint"]})
    assert res["total"] == 1 and res["assets"][0]["name"] == "BP_A"


def test_asset_tags_accepts_a_package_path(v2, ue):
    ue.assets["/Game/BP/BP_A"] = ue.Blueprint(ue.classes["/Script/Engine.Actor"])
    assert ok(v2, "asset_tags", {"asset": "/Game/BP/BP_A"})["tags"]["ParentClass"] == "/Script/Engine.Actor"


# --- widget_query op=render ------------------------------------------------------

def test_widget_render_resolves_a_blueprint_asset_path_to_its_class(v2, ue):
    from fakeunreal import _Subsystem
    ue.UserWidget = ue.add_class("UserWidget", "/Script/UMG.UserWidget")
    gen = ue.add_class("WBP_Banner_C", "/Game/UI/WBP_Banner.WBP_Banner_C", ue.UserWidget)
    ue.assets["/Game/UI/WBP_Banner"] = ue.Blueprint(gen)
    seen = []

    class Auth:
        def capture_widget(self, path, w, h, out):
            seen.append(path)
            return out

    ue.MCPAuthoringSubsystem = Auth
    ue.get_editor_subsystem = lambda which: Auth() if which is Auth else _Subsystem(ue)
    res = ok(v2, "widget_render", {"widget_class": "/Game/UI/WBP_Banner", "width": 64, "height": 32})
    assert seen == ["/Game/UI/WBP_Banner.WBP_Banner_C"] and res["ok"]
    assert err(v2, "widget_render", {"widget_class": "/Script/Engine.Actor"})["code"] == "BAD_VALUE"
