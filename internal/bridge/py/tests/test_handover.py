"""v1 ↔ v2 sequential handover on one running editor (plan §2.8), with the REAL v1
companion (frozen from tag v1-final) and the v2 companion in one __main__."""
import importlib
import sys

from conftest import V2_CLAIM, companion_source, run_dispatch, v1_hotload, v1_source, v2_hotload_boot


def _main_with_v1():
    main = {"__name__": "__main__"}
    v1_hotload(main, v1_source())
    assert main["_MCP_BRIDGE_VERSION"] == 37
    return main


def test_v2_install_leaves_v1_untouched():
    main = _main_with_v1()
    v1_dispatch, v1_native, v1_ops = main["_mcp_dispatch"], main["_mcp_dispatch_native"], main["_OPS"]
    exec(v2_hotload_boot(companion_source()), main)
    # v1's entry points, sentinel and op table are exactly as they were.
    assert main["_mcp_dispatch"] is v1_dispatch
    assert main["_mcp_dispatch_native"] is v1_native
    assert main["_OPS"] is v1_ops
    assert main["_MCP_BRIDGE_VERSION"] == 37
    # Both companions answer.
    assert run_dispatch(main["_mcp_dispatch"], "no_such_op", {})["code"] == "UNKNOWN_OP"
    assert run_dispatch(main["_mcp2_dispatch"], "no_such_op", {})["code"] == "UNKNOWN_OP"


def test_claim_then_v1_rollback_reinstalls_and_both_work():
    main = _main_with_v1()
    exec(v2_hotload_boot(companion_source()), main)
    exec(V2_CLAIM, main)
    # The plugin's fixed entry point now reaches v2, and v1 is marked stale.
    assert main["_mcp_dispatch_native"] is main["_mcp2"]._mcp2_dispatch_native
    assert main["_MCP_BRIDGE_VERSION"] == 0

    # Rollback: a v1 server reconnects. Its version check reads 0 != 37, so it
    # reinstalls its companion (v1's hotload), reclaiming its own names.
    assert main.get("_MCP_BRIDGE_VERSION", 0) != 37
    v1_hotload(main, v1_source())
    assert main["_MCP_BRIDGE_VERSION"] == 37
    assert main["_mcp_dispatch_native"] is not main["_mcp2"]._mcp2_dispatch_native
    assert run_dispatch(main["_mcp_dispatch"], "no_such_op", {})["code"] == "UNKNOWN_OP"

    # v2's module was never touched by the rollback: it still serves.
    assert main["_MCP2_BRIDGE_VERSION"] >= 1
    assert run_dispatch(main["_mcp2_dispatch"], "no_such_op", {})["code"] == "UNKNOWN_OP"


def test_ondisk_modules_are_distinct(tmp_path, monkeypatch):
    """On-disk mode: v1 imports Intermediate/PyMCP/mcp_bridge, v2 imports
    Intermediate/PyMCP2/mcp2_bridge — two module objects, never one reloaded."""
    d1, d2 = tmp_path / "PyMCP", tmp_path / "PyMCP2"
    d1.mkdir()
    d2.mkdir()
    (d1 / "mcp_bridge.py").write_text(v1_source(), encoding="utf-8")
    (d2 / "mcp2_bridge.py").write_text(companion_source(), encoding="utf-8")
    monkeypatch.syspath_prepend(str(d1))
    monkeypatch.syspath_prepend(str(d2))
    for name in ("mcp_bridge", "mcp2_bridge"):
        sys.modules.pop(name, None)
    v1 = importlib.import_module("mcp_bridge")
    v2 = importlib.import_module("mcp2_bridge")
    importlib.reload(v2)  # v2 reinstalling must not disturb v1
    assert v1 is not v2
    assert v1._MCP_BRIDGE_VERSION == 37 and v2._MCP2_BRIDGE_VERSION >= 1
    assert not hasattr(v1, "_mcp2_dispatch") and not hasattr(v2, "_mcp_dispatch")
