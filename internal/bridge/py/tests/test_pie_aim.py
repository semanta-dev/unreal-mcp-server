"""pie_aim_state: the yaw/pitch from the player's view to a target (pie op=aim turns it
with mouse input). The eye is the camera, the target its bounds' centre."""
import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Fake, Rotator, Vector, installed


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = []
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, args):
    return run_dispatch(v2["_mcp2_dispatch"], "pie_aim_state", args)


def setup(ue, yaw=350.0, pitch=355.0):
    ue.add_class("EnemyCharacter", "/Script/Game.EnemyCharacter", ue.classes["/Script/Engine.Actor"])
    ue.pawn = ue.add_actor("/Script/Engine.Actor", "Hero", world="pie")
    cam = _NS(get_camera_location=lambda: Vector(0, 0, 100))
    pc = _NS(get_editor_property=lambda k: {"player_camera_manager": cam}[k],
             get_control_rotation=lambda: Rotator(0, pitch, yaw), is_look_input_ignored=lambda: False)
    ue.GameplayStatics.get_player_controller = lambda world, i: pc if world == "PIE" else None
    near = ue.add_actor("/Script/Game.EnemyCharacter", "Enemy_Near", world="pie")
    near.loc = Vector(1000, 1000, 100)
    far = ue.add_actor("/Script/Game.EnemyCharacter", "Enemy_Far", world="pie")
    far.loc = Vector(-5000, 0, 100)


def test_nearest_of_class_with_wrapped_angles(v2, ue):
    setup(ue)
    r = call(v2, {"class": "EnemyCharacter"})["result"]
    # Target at 45° yaw, level with the eye; the view at 350° yaw (= -10°), pitch 355° (= -5°).
    assert r["target"] == "Enemy_Near", r
    assert r["yaw_error"] == pytest.approx(55.0) and r["pitch_error"] == pytest.approx(5.0), r
    assert r["distance"] == pytest.approx(1414.2, abs=0.1) and r["look_ignored"] is False


def test_named_actor_and_errors(v2, ue):
    setup(ue)
    r = call(v2, {"actor": "Enemy_Far"})["result"]
    assert r["target"] == "Enemy_Far" and r["yaw_error"] == pytest.approx(-170.0), r
    assert call(v2, {}).get("code") == "BAD_VALUE"
    assert call(v2, {"actor": "Nobody"}).get("code") == "NOT_FOUND"


def test_a_dead_player_is_not_found(v2, ue):
    setup(ue)
    ue.pawn = None
    env = call(v2, {"class": "EnemyCharacter"})
    assert env.get("code") == "NOT_FOUND" and "dead" in env["error"], env
