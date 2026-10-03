"""Regression tests for companion defects found in the P7 live run
(docs/validation/T4-2026-10-03.md): each models the UE 5.7 Python surface that
differed from what the op assumed."""
import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Fake, installed


@pytest.fixture
def ue(v2):
    fake = Fake()
    with installed(v2["_mcp2"], fake):
        yield fake


def call(v2, op, args):
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


def test_pie_console_routes_through_the_controller_via_system_library(v2, ue):
    # 5.7 does not expose PlayerController.console_command; the fake controller has
    # no such method, so the old call fails here as it did live.
    ue.pie_actors = []
    pc = object()
    ran = []
    ue.GameplayStatics.get_player_controller = lambda world, i: pc
    ue.SystemLibrary.execute_console_command = lambda world, cmd, player=None: ran.append((world, cmd, player))
    env = call(v2, "console", {"command": "stat fps", "world": "pie"})
    assert env["ok"], env
    assert env["result"]["via"] == "player_controller" and ran == [("PIE", "stat fps", pc)]


def test_game_scene_capture_finds_the_enum_without_its_e_prefix(v2, ue, tmp_path):
    # Python names UENUM EMCPCaptureCamera "MCPCaptureCamera"; only that name exists.
    ue.pie_actors = []
    ue.Paths.convert_relative_path_to_full = lambda p: str(tmp_path / "Saved")
    ue.MCPCaptureCamera = _NS(PLAYER="player", FIXED="fixed", ACTOR="actor")
    started = []

    class Sub:
        def start_capture(self, session, out_dir, prefix, w, h, interval, frames, secs, cam, loc, rot, fov, actor, ui):
            started.append(cam)
            return True

    ue.MCPCaptureSubsystem = _NS(get=lambda world: Sub())
    env = call(v2, "capture_start", {"session": "s1", "source": "game_scene", "world": "pie"})
    assert env["ok"], env
    assert started == ["player"] and env["result"]["backend"] == "plugin"


def test_pie_highres_frames_are_written_where_they_are_read(v2, ue, tmp_path):
    # Live, the recorder read Saved/Screenshots while HighResShot wrote a bare name under
    # the engine's screenshot folder. Frames now go to absolute paths in the session dir.
    ue.pie_actors = []
    ue.Paths.convert_relative_path_to_full = lambda p: str(tmp_path / "Saved")
    ticks, shots = [], []
    ue.register_slate_post_tick_callback = lambda fn: ticks.append(fn) or 1
    ue.AutomationLibrary = _NS(take_high_res_screenshot=lambda w, h, f: shots.append(f))
    v2["_mcp2"]._recorder_observe = lambda rec: {}
    env = call(v2, "capture_start", {"session": "hr", "source": "pie_highres", "world": "pie"})
    assert env["ok"], env
    d = env["result"]["dir"]
    assert d == (tmp_path / "Saved" / "MCP" / "capture" / "hr").as_posix() + "/"
    ticks[0](0.5)
    assert shots and shots[0].startswith(d)
