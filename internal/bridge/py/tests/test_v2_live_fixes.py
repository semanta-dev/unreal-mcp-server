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


def test_recorder_reports_its_own_tick_not_fps(v2, ue, tmp_path):
    # R0.7: the per-sample timing is the recorder's tick (slowed by its own captures),
    # so it is published as recorder.tick_ms, never as the game's perf.fps.
    ue.pie_actors = []
    ue.Paths.convert_relative_path_to_full = lambda p: str(tmp_path / "Saved")
    ticks = []
    ue.register_slate_post_tick_callback = lambda fn: ticks.append(fn) or 1
    ue.AutomationLibrary = _NS(take_high_res_screenshot=lambda w, h, f: None)
    ue.GameplayStatics.get_time_seconds = lambda world: 1.0
    v2["_mcp2"]._recorder_observe = lambda rec: {}
    assert call(v2, "capture_start", {"session": "tk", "source": "pie_highres", "world": "pie"})["ok"]
    ticks[0](0.04)
    assert v2["_mcp2"]._MCP_RECORDERS["tk"].get("last_error") is None, v2["_mcp2"]._MCP_RECORDERS["tk"]["last_error"]
    env = call(v2, "capture_stop", {"session": "tk"})
    assert env["ok"] and env["result"]["frames"], env
    st = env["result"]["frames"][0]["state"]
    assert "perf" not in st
    assert st["recorder"]["tick_ms"] == pytest.approx(40.0)
    assert st["recorder"]["max_tick_ms"] == pytest.approx(40.0)


def test_plugin_api_handshake(v2, ue):
    # R0.5: API 3+ answers GetPluginApiVersion; a pre-handshake plugin (subsystems but no
    # library) is 2; no plugin is 0. _need_plugin refuses older ones with both versions.
    m = v2["_mcp2"]
    assert m._plugin_api() == 0
    ue.MCPControlSubsystem = object()
    assert m._plugin_api() == 2
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 3)
    assert m._plugin_api() == 3
    assert m._need_plugin(3, "x") is ue.MCPCoreLibrary
    with pytest.raises(m._V2Error) as e:
        m._need_plugin(5, "axis input")
    assert e.value.code == "PLUGIN_MISSING" and e.value.details == {"needed": 5, "have": 3}
    assert call(v2, "editor_ping", {})["result"]["plugin_api"] == 3


def test_editor_undo_maps_the_plugin_verdicts(v2, ue):
    # R0.9: the plugin does check + step in one call; the op maps its verdict.
    calls = []

    def step(kind):
        def f(prefix):
            calls.append((kind, prefix))
            return ue._undo_result
        return f
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 3, undo_if_titled=step("undo"), redo_if_titled=step("redo"))
    ue._undo_result = '{"ok": true, "title": "MCP: spawn A"}'
    assert call(v2, "editor_undo", {})["result"] == {"undone": "MCP: spawn A"}
    assert call(v2, "editor_undo", {"redo": True})["result"] == {"redone": "MCP: spawn A"}
    assert calls == [("undo", "MCP: "), ("redo", "MCP: ")]
    for reason, code in (("title_mismatch", "CONFLICT"), ("pie", "PRECONDITION"), ("empty", "PRECONDITION"), ("failed", "EDITOR_ERROR")):
        ue._undo_result = '{"ok": false, "title": "Move", "reason": "%s"}' % reason
        env = call(v2, "editor_undo", {})
        assert not env["ok"] and env["code"] == code, (reason, env)
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 2)
    assert call(v2, "editor_undo", {})["code"] == "PLUGIN_MISSING"
