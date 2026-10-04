"""v2 core resolvers (plan §2.2 R3, §2.7 items 1-4), exercised against fake actors."""
import pytest
from conftest import run_dispatch


class FakeActor:
    def __init__(self, label, path):
        self._label, self._path = label, path

    def get_actor_label(self):
        return self._label

    def get_path_name(self):
        return self._path


EDITOR = object()
PIE = object()


@pytest.fixture
def mod(v2):
    m = v2["_mcp2"]
    actors = {
        "editor": [FakeActor("Cube", "/Game/Maps/L.L:PersistentLevel.Cube_1"),
                   FakeActor("Twin", "/Game/Maps/L.L:PersistentLevel.Twin_1"),
                   FakeActor("Twin", "/Game/Maps/L.L:PersistentLevel.Twin_2")],
        "pie": [FakeActor("Cube", "/Game/Maps/UEDPIE_0_L.L:PersistentLevel.Cube_1")],
    }
    state = {"pie": True}
    m._editor_world = lambda: EDITOR
    m._game_world = lambda: PIE if state["pie"] else None
    m._world_actors = lambda world, name: actors[name]
    m._test_state = state
    return m


def test_world_vocabulary(mod):
    assert mod._v2_world({}, "editor")[1] == "editor"
    assert mod._v2_world({"world": "pie"}, "editor")[1] == "pie"
    assert mod._v2_world({"world": "auto"}, "editor")[1] == "pie"
    mod._test_state["pie"] = False
    assert mod._v2_world({"world": "auto"}, "editor")[1] == "editor"
    with pytest.raises(mod._V2Error) as e:
        mod._v2_world({"world": "pie"}, "editor")
    assert e.value.code == "NOT_IN_PIE"
    with pytest.raises(mod._V2Error) as e:
        mod._v2_world({"world": "everywhere"}, "editor")
    assert e.value.code == "BAD_VALUE"  # never a silent fallback


def test_resolve_actor_label_path_and_conflict(mod):
    assert mod._resolve_actor(EDITOR, "editor", "Cube").get_path_name().endswith("Cube_1")
    with pytest.raises(mod._V2Error) as e:
        mod._resolve_actor(EDITOR, "editor", "Twin")
    assert e.value.code == "CONFLICT" and len(e.value.details["candidates"]) == 2
    # An object path disambiguates.
    assert mod._resolve_actor(EDITOR, "editor", "/Game/Maps/L.L:PersistentLevel.Twin_2").get_actor_label() == "Twin"
    # An editor path resolves to the PIE copy (UEDPIE_<n>_ prefix ignored).
    pie_actor = mod._resolve_actor(PIE, "pie", "/Game/Maps/L.L:PersistentLevel.Cube_1")
    assert "UEDPIE_0_" in pie_actor.get_path_name()
    with pytest.raises(mod._V2Error) as e:
        mod._resolve_actor(EDITOR, "editor", "Nope")
    assert e.value.code == "NOT_FOUND"


def test_pseudo_targets_are_pie_only(mod):
    with pytest.raises(mod._V2Error) as e:
        mod._resolve_actor(EDITOR, "editor", "@gamestate")
    assert e.value.code == "NOT_FOUND"


def test_actor_edits_require_explicit_world(v2, mod):
    env = run_dispatch(v2["_mcp2_dispatch"], "actor_delete", {"actor": "Cube"})
    assert env["ok"] is False and env["code"] == "BAD_VALUE" and "explicit world" in env["error"]


def test_actor_call_in_editor_is_unsupported(v2, mod):
    env = run_dispatch(v2["_mcp2_dispatch"], "actor_call", {"world": "editor", "actor": "Cube", "function": "F"})
    assert env["ok"] is False and env["code"] == "UNSUPPORTED"
