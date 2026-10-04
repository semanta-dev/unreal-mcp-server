"""R2 companion ops: axis input, cursor and widget clicks through the plugin's control
subsystem (API 5), and spawning into the running game."""
import json

import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Fake, installed


class Ctrl:
    """The plugin's UMCPControlSubsystem as Python sees it (API 5)."""

    def __init__(self):
        self.calls = []

    def _rec(self, *a):
        self.calls.append(a)

    def inject_axis(self, key, value, dur):
        self._rec("axis", key, value, dur)
        return "" if key in ("MouseX", "Gamepad_LeftX") else "'%s' is not a 1-D axis key" % key

    def tap_key(self, key):
        self._rec("tap", key)
        return key != "Nope"

    def release_all(self):
        self._rec("release_all")

    def move_cursor(self, x, y):
        self._rec("move", x, y)
        return json.dumps({"ok": True, "viewport": [x, y], "screen": [x + 100, y + 50], "handled": True})

    def click_at(self, x, y, button):
        self._rec("click", x, y, button)
        if button == "W":
            return json.dumps({"ok": False, "error": "'W' is not a mouse button"})
        return json.dumps({"ok": True, "viewport": [x, y], "screen": [x, y], "handled": True})

    def drag_cursor(self, x0, y0, x1, y1, dur, button):
        self._rec("drag", x0, y0, x1, y1, dur, button)
        return json.dumps({"ok": True, "viewport": [x0, y0], "screen": [x0, y0], "handled": False})

    def release_cursor(self):
        self._rec("release")
        return True

    def click_widget(self, name, button):
        self._rec("widget", name, button)
        return json.dumps({
            "StartButton": {"ok": True, "viewport": [10, 20], "screen": [10, 20], "handled": True, "widget": "/x.StartButton"},
            "Hidden": {"ok": False, "error": "no visible live widget named 'Hidden'"},
            "Twice": {"ok": False, "error": "2 visible widgets are named 'Twice': a, b"},
            "Under": {"ok": False, "error": "'Under' is covered at its centre by SBorder"},
        }[name])


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = []
    fake.ctrl = Ctrl()
    fake.MCPControlSubsystem = _NS(get=lambda world: fake.ctrl if world == "PIE" else None)
    fake.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 5)
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, op, args):
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


def test_axis_input(v2, ue):
    env = call(v2, "pie_input", {"key": "MouseX", "action": "axis", "value": 2.5, "duration_s": 0.5})
    assert env["ok"] and env["result"]["value"] == 2.5, env
    assert ue.ctrl.calls[-1] == ("axis", "MouseX", 2.5, 0.5)
    env = call(v2, "pie_input", {"key": "W", "action": "axis", "value": 1})
    assert env["code"] == "BAD_VALUE" and "axis" in env["error"], env
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 4)
    assert call(v2, "pie_input", {"key": "MouseX", "action": "axis", "value": 1})["code"] == "PLUGIN_MISSING"


def test_key_input_errors_are_v2_errors(v2, ue):
    assert call(v2, "pie_input", {"key": "Nope"})["code"] == "BAD_VALUE"
    assert call(v2, "pie_input", {"key": "W", "action": "wiggle"})["code"] == "BAD_VALUE"
    assert call(v2, "pie_input", {"action": "release_all"})["ok"]
    ue.pie_actors = None
    assert call(v2, "pie_input", {"key": "W"})["code"] == "NOT_IN_PIE"


def test_cursor(v2, ue):
    env = call(v2, "pie_cursor", {"action": "move", "position": [10, 20]})
    assert env["ok"] and env["result"]["screen"] == [110, 70] and env["result"]["action"] == "move", env
    env = call(v2, "pie_cursor", {"action": "drag", "position": [0, 0], "to": [50, 60], "duration_s": 1})
    assert env["ok"] and ue.ctrl.calls[-1] == ("drag", 0.0, 0.0, 50.0, 60.0, 1.0, ""), env
    assert call(v2, "pie_cursor", {"action": "drag", "position": [0, 0]})["code"] == "BAD_VALUE"  # no `to`
    env = call(v2, "pie_cursor", {"action": "release"})
    assert env["ok"] and env["result"] == {"action": "release", "was_pinned": True}, env
    assert call(v2, "pie_cursor", {"action": "click", "position": [1]})["code"] == "BAD_VALUE"
    env = call(v2, "pie_cursor", {"action": "click", "position": [1, 2], "button": "W"})
    assert env["code"] == "BAD_VALUE" and "mouse button" in env["error"], env


def test_ui_click(v2, ue):
    env = call(v2, "pie_ui_click", {"widget": "StartButton"})
    assert env["ok"] and env["result"]["widget"] == "/x.StartButton", env
    assert call(v2, "pie_ui_click", {"widget": "Hidden"})["code"] == "NOT_FOUND"
    assert call(v2, "pie_ui_click", {"widget": "Twice"})["code"] == "CONFLICT"
    assert call(v2, "pie_ui_click", {"widget": "Under"})["code"] == "CONFLICT"
    assert call(v2, "pie_ui_click", {})["code"] == "BAD_VALUE"


def test_pie_time(v2, ue):
    ue.GameplayStatics.get_time_seconds = lambda world: 12.5
    ue.GameplayStatics.is_game_paused = lambda world: False
    env = call(v2, "pie_time", {})
    assert env["ok"] and env["result"] == {"world_time_s": 12.5, "paused": False}, env
    ue.pie_actors = None
    assert call(v2, "pie_time", {})["code"] == "NOT_IN_PIE"


def test_spawn_into_pie_through_the_plugin(v2, ue):
    spawned = []

    class A:
        def __init__(self, cls, loc, rot):
            self.cls, self.loc, self.rot, self.label, self.destroyed = cls, loc, rot, "", False

        def set_actor_label(self, label):
            self.label = label

        def destroy_actor(self):
            self.destroyed = True

    def spawn_in_game(cls, loc, rot):
        a = A(cls, loc, rot)
        spawned.append(a)
        return a

    ue.ctrl.spawn_in_game = spawn_in_game
    ue.add_class("Drop", "/Script/Game.Drop")
    m = v2["_mcp2"]
    m._actor_view = lambda a, name, full=False: {"label": a.label, "world": name}
    env = call(v2, "actor_spawn", {"world": "pie", "class": "/Script/Game.Drop", "location": [1, 2, 3], "rotation": [10, 20, 30], "label": "D1"})
    assert env["ok"] and env["result"]["spawned"] == {"label": "D1", "world": "pie"}, env
    a = spawned[0]
    assert (a.loc.x, a.loc.y, a.loc.z) == (1, 2, 3) and (a.rot.pitch, a.rot.yaw, a.rot.roll) == (10, 20, 30)
    # A failed follow-up (an unknown property) destroys the half-made actor.
    m._set_props = lambda actor, props: (_ for _ in ()).throw(m._V2Error("BAD_VALUE", "no such property"))
    env = call(v2, "actor_spawn", {"world": "pie", "class": "/Script/Game.Drop", "properties": {"x": 1}})
    assert env["code"] == "BAD_VALUE" and spawned[-1].destroyed, env
