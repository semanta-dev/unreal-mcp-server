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


def test_short_subsystem_names_resolve_through_the_python_module(v2, ue):
    # Live R1 defect: a short name in a module the companion does not list (the game's
    # own module, LevelEditor) was CLASS_UNRESOLVED.
    m = v2["_mcp2"]
    ue.AgentSubsystem = type("AgentSubsystem", (), {"static_class": staticmethod(lambda: SUB_CLS)})
    assert m._resolve_object("PIE", "pie", "@subsystem:AgentSubsystem") is ue.sub
    with pytest.raises(m._V2Error) as e:
        m._resolve_class_v2("NoSuchClass")
    assert e.value.code == "CLASS_UNRESOLVED"
    # A non-native class exposed under the name is not taken from the module.
    ue.Transient = type("Transient", (), {"static_class": staticmethod(lambda: Class("Transient", "/Game/X.Transient_C"))})
    with pytest.raises(m._V2Error):
        m._resolve_class_v2("Transient")


def test_editor_world_during_pie_is_the_pie_maps_source(v2, ue):
    # Live R1 defect: UE 5.7's get_editor_world() is None while PIE runs.
    m = v2["_mcp2"]
    editor_map = Obj("L_Arena", Class("World", "/Script/Engine.World"))
    pie_map = _NS(get_path_name=lambda: "/Game/Maps/UEDPIE_0_L_Arena.L_Arena")
    ue.classes["/Game/Maps/L_Arena.L_Arena"] = editor_map
    ue.get_editor_subsystem = lambda t: _NS(get_editor_world=lambda: None, get_game_world=lambda: pie_map)
    assert m._editor_world() is editor_map
    ue.get_editor_subsystem = lambda t: _NS(get_editor_world=lambda: None, get_game_world=lambda: None)
    assert m._editor_world() is None


def test_object_path_module_class_heads_and_caller_errors(v2, ue):
    # The game_api spelling @subsystem:Module.Class.prop (review R1 #5).
    env = call(v2, "observe_paths", {"paths": ["@subsystem:Game.AgentSubsystem.difficulty"]})
    assert env["ok"] and env["result"]["values"] == {"@subsystem:Game.AgentSubsystem.difficulty": "hard"}, env
    # An unresolvable class is the caller's error, not "not yet".
    env = call(v2, "observe_paths", {"paths": ["@subsystem:Nope.difficulty"]})
    assert env["code"] == "CLASS_UNRESOLVED", env
    # A getter on a value that is not an object (review R1 #6).
    ue.sub.props["pos"] = [1, 2]
    env = call(v2, "observe_paths", {"paths": ["@subsystem:Game.AgentSubsystem.pos.Length()"]})
    assert env["code"] == "BAD_VALUE" and "not an object" in env["error"], env


def test_object_path_properties_need_no_plugin(v2, ue):
    # R1.3: without the plugin, properties still work; getters and @subsystem do not.
    gi = Obj("GI", Class("GameInstance", "/Script/Engine.GameInstance"), props={"score": 7})
    ue.GameplayStatics.get_game_instance = lambda world: gi
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 0)
    env = call(v2, "observe_paths", {"paths": ["@gameinstance.score"]})
    assert env["ok"] and env["result"]["values"] == {"@gameinstance.score": 7}, env
    assert call(v2, "observe_paths", {"paths": ["@gameinstance.GetScore()"]})["code"] == "PLUGIN_MISSING"


class _StructBase:
    """A reflected struct: fields by snake_case name, unknown ones raise (as UE does)."""
    FIELDS = ()

    def __init__(self):
        self.f = {k: 0.0 for k in self.FIELDS}

    def get_editor_property(self, k):
        if k not in self.f:
            raise Exception("Failed to find property '%s'" % k)
        return self.f[k]

    def set_editor_property(self, k, v):
        self.get_editor_property(k)
        self.f[k] = v


class _Vec(_StructBase):
    FIELDS = ("x", "y", "z")


class _Hit(_StructBase):
    FIELDS = ("location", "damage")

    def __init__(self):
        super().__init__()
        self.f["location"] = _Vec()


def test_actor_call_struct_args_are_checked(v2, ue):
    # Review R1 #4 / live: UE's dict conversion drops unknown keys silently ({"X": 1} -> 0).
    ue.StructBase, ue.Vec, ue.Hit = _StructBase, _Vec, _Hit
    got = {}

    class Target(Obj):
        def set_scale(self):
            """x.set_scale(new_scale, hits) -> None

Args:
    new_scale (Vec): the scale
    hits (Array[Hit]): hits

Returns:
    None"""

    t = Target("T", Class("T", "/Script/Game.T"), functions={"SetScale": lambda **kw: got.update(kw)})
    ue.sub_target = t
    m = v2["_mcp2"]
    out = m._call_args(t, "SetScale", {"new_scale": {"x": 2, "y": 3, "z": 4}, "hits": [{"damage": 5, "location": {"z": 1}}]})
    assert out["new_scale"].f == {"x": 2, "y": 3, "z": 4}
    assert out["hits"][0].f["damage"] == 5 and out["hits"][0].f["location"].f["z"] == 1
    for bad, where in (({"new_scale": {"X": 1}}, "new_scale"), ({"hits": [{"dmg": 1}]}, "hits[0]"),
                       ({"new_scale": {"x": 1}, "scale2": {"x": 1}}, "scale2")):
        with pytest.raises(m._V2Error) as e:
            m._call_args(t, "SetScale", bad)
        assert e.value.code == "BAD_VALUE" and where in str(e.value), (bad, e.value)
    # Lists and scalars pass to UE's own conversion untouched.
    assert m._call_args(t, "SetScale", {"new_scale": [1, 2, 3]}) == {"new_scale": [1, 2, 3]}
    # No Python method to read the signature from: a struct argument is refused, not guessed.
    with pytest.raises(m._V2Error) as e:
        m._call_args(t, "Unknown", {"new_scale": {"x": 1}})
    assert e.value.code == "BAD_VALUE"
