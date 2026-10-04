"""R1 companion ops: object references, actor_call parse=json, observe_paths (pure/const
getters only, plugin-checked) and the game_api read/command ops."""
import json

import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Class, Fake, installed


class Obj:
    """A UObject-ish fake: functions by name, properties by snake_case name."""

    def __init__(self, name, cls, functions=None, props=None):
        self.name, self.cls = name, cls
        self.functions, self.props = functions or {}, props or {}

    def get_name(self):
        return self.name

    def get_path_name(self):
        return "/Engine/Transient." + self.name

    def get_class(self):
        return self.cls

    def call_method(self, fn, args=(), kwargs=None):
        if fn not in self.functions:
            raise Exception("Failed to find function '%s'" % fn)
        return self.functions[fn](*args, **(kwargs or {}))

    def get_editor_property(self, k):
        if k not in self.props:
            raise Exception("Failed to find property '%s'" % k)
        return self.props[k]


SUB_CLS = Class("AgentSubsystem", "/Script/Game.AgentSubsystem")
PURE = {"PeekSnapshotJson", "GetEventsSince", "GetCapabilitiesJson", "GetWave"}


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = []
    snap = {"wave_number": 3, "player": {"ammo": [5, 6]}}
    sub = Obj("AgentSubsystem_0", SUB_CLS, functions={
        "PeekSnapshotJson": lambda: json.dumps(snap),
        "GetWave": lambda: 3,
        "Mutate": lambda: "x",
        "GetEventsSince": lambda cursor: json.dumps({"events": [], "next_cursor": "e:0", "gap": cursor == "old:1"}),
        "ExecuteCommandJson": lambda req: json.dumps({"accepted": True, "echo": json.loads(req)}),
        "NotJson": lambda: "nope",
    }, props={"difficulty": "hard"})
    fake.sub = sub
    fake.classes["/Script/Game.AgentSubsystem"] = SUB_CLS
    fake.EditorSubsystem = _NS(static_class=lambda: Class("EditorSubsystem", "/Script/EditorSubsystem.EditorSubsystem"))
    fake.EngineSubsystem = _NS(static_class=lambda: Class("EngineSubsystem", "/Script/Engine.EngineSubsystem"))
    fake.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 3,
                              find_game_subsystem=lambda world, cls: sub if cls is SUB_CLS else None,
                              is_pure_or_const=lambda cls, fn: str(fn) in PURE)
    fake.GameplayStatics.get_game_instance = lambda world: Obj("GI", Class("GameInstance", "/Script/Engine.GameInstance"))
    fake.GameplayStatics.get_player_state = lambda world, i: Obj("PS%d" % i, Class("PlayerState", "/Script/Engine.PlayerState")) if i == 0 else None
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, op, args):
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


def test_object_refs(v2, ue):
    m = v2["_mcp2"]
    assert m._resolve_object("PIE", "pie", "@gameinstance").get_name() == "GI"
    assert m._resolve_object("PIE", "pie", "@playerstate").get_name() == "PS0"
    with pytest.raises(m._V2Error) as e:
        m._resolve_object("PIE", "pie", "@playerstate:1")
    assert e.value.code == "NOT_FOUND"
    with pytest.raises(m._V2Error) as e:
        m._resolve_object("PIE", "pie", "@playerstate:x")
    assert e.value.code == "BAD_VALUE"
    with pytest.raises(m._V2Error) as e:
        m._resolve_object("EDITOR", "editor", "@gameinstance")  # PIE only
    assert e.value.code == "NOT_FOUND"
    assert m._resolve_object("PIE", "pie", "@subsystem:/Script/Game.AgentSubsystem") is ue.sub


def test_editor_subsystems_only_from_reflect(v2, ue):
    m = v2["_mcp2"]
    ed = Class("LevelEditorSubsystem", "/Script/LevelEditor.LevelEditorSubsystem")
    ue.classes[ed.get_path_name()] = ed
    ue.MathLibrary.class_is_child_of = lambda c, base: c is ed and base.get_name() == "EditorSubsystem"
    with pytest.raises(m._V2Error) as e:
        m._resolve_object("PIE", "pie", "@subsystem:/Script/LevelEditor.LevelEditorSubsystem")
    assert e.value.code == "BAD_VALUE" and "reflect" in str(e.value)
    ue.LevelEditorSubsystem = "LevelEditorSubsystemType"
    m.unreal.get_editor_subsystem = lambda t: "the-editor-subsystem" if t == "LevelEditorSubsystemType" else None
    assert m._resolve_object("PIE", "pie", "@subsystem:/Script/LevelEditor.LevelEditorSubsystem", editor_subsystems=True) == "the-editor-subsystem"


def test_actor_call_parse_json(v2, ue):
    env = call(v2, "actor_call", {"actor": "@subsystem:/Script/Game.AgentSubsystem", "function": "PeekSnapshotJson", "parse": "json"})
    assert env["ok"] and env["result"]["result"]["wave_number"] == 3, env
    env = call(v2, "actor_call", {"actor": "@subsystem:/Script/Game.AgentSubsystem", "function": "NotJson", "parse": "json"})
    assert env["code"] == "BAD_VALUE"
    env = call(v2, "actor_call", {"actor": "@subsystem:/Script/Game.AgentSubsystem", "function": "GetWave", "parse": "yaml"})
    assert env["code"] == "BAD_VALUE"


def test_observe_paths_pure_getters_only(v2, ue):
    p = "@subsystem:/Script/Game.AgentSubsystem.PeekSnapshotJson().player.ammo.1"
    env = call(v2, "observe_paths", {"paths": [p, "@subsystem:/Script/Game.AgentSubsystem.difficulty"]})
    assert env["ok"], env
    assert env["result"]["values"] == {p: 6, "@subsystem:/Script/Game.AgentSubsystem.difficulty": "hard"}
    env = call(v2, "observe_paths", {"paths": ["@subsystem:/Script/Game.AgentSubsystem.Mutate()"]})
    assert env["code"] == "BAD_VALUE" and "not BlueprintPure or const" in env["error"], env
    env = call(v2, "observe_paths", {"paths": ["@subsystem:/Script/Game.AgentSubsystem.NotJson().x"]})
    assert env["code"] == "BAD_VALUE"  # a non-JSON string the path continues into
    env = call(v2, "observe_paths", {"paths": ["@subsystem:/Script/Game.AgentSubsystem.missing_prop"]})
    assert env["ok"] and "missing_prop" in json.dumps(env["result"]["errors"])  # not there yet: an error, not a value
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 2)
    assert call(v2, "observe_paths", {"paths": [p]})["code"] == "PLUGIN_MISSING"


def test_game_read_and_command(v2, ue):
    cls = "/Script/Game.AgentSubsystem"
    env = call(v2, "game_read", {"class": cls, "function": "GetEventsSince", "args": ["old:1"]})
    assert env["ok"] and env["result"]["result"]["gap"] is True, env
    env = call(v2, "game_read", {"class": cls, "function": "ExecuteCommandJson", "args": ["{}"]})
    assert env["code"] == "BAD_VALUE" and "not BlueprintPure or const" in env["error"]
    req = json.dumps({"command": "start_wave", "request_id": "r1", "world_epoch": "e"})
    env = call(v2, "game_command", {"class": cls, "function": "ExecuteCommandJson", "request": req})
    assert env["ok"] and env["result"]["result"]["echo"]["request_id"] == "r1", env
    ue.pie_actors = None  # PIE stopped
    assert call(v2, "game_read", {"class": cls, "function": "PeekSnapshotJson"})["code"] == "NOT_IN_PIE"
