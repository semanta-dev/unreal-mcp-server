"""reflect op=object reads an asset (a data asset's properties) when actor is an asset
path; actor paths (Map.Map:PersistentLevel.X) still resolve in the world."""
from conftest import run_dispatch
from fakeunreal import Fake, installed
import pytest


class Tuning:
    def __init__(self):
        self.props = {"player_damage_multiplier": 1.0}

    def get_editor_property(self, k):
        return self.props[k]

    def get_class(self):
        return type("C", (), {"get_name": lambda s: "AesirTuning", "get_path_name": lambda s: "/Script/Game.AesirTuning"})()

    def get_path_name(self):
        return "/Game/Data/DA_AesirTuning.DA_AesirTuning"


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.assets["/Game/Data/DA_AesirTuning"] = Tuning()
    with installed(v2["_mcp2"], fake):
        yield fake


def test_reflect_reads_a_data_asset(v2, ue):
    env = run_dispatch(v2["_mcp2_dispatch"], "reflect", {"op": "object", "actor": "/Game/Data/DA_AesirTuning",
                                                         "properties": ["player_damage_multiplier"]})
    assert env["ok"] and env["result"]["world"] == "asset", env
    assert env["result"]["properties"]["player_damage_multiplier"] == 1.0, env


def test_an_actor_path_is_not_loaded_as_an_asset(v2, ue):
    ue.load_asset = lambda p: (_ for _ in ()).throw(AssertionError("loaded " + p))
    env = run_dispatch(v2["_mcp2_dispatch"], "reflect", {"op": "object", "actor": "/Game/Maps/L.L:PersistentLevel.Nope"})
    assert env.get("code") == "NOT_FOUND", env
