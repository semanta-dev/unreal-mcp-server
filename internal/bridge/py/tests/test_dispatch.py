"""Dispatch contract of the v2 companion (plan §2.2 R5, §2.7 item 6)."""
import pathlib
import re

from conftest import companion_source, run_dispatch

REPO = pathlib.Path(__file__).resolve().parents[4]


def test_version_sentinel_and_entry_points(v2):
    assert v2["_MCP2_BRIDGE_VERSION"] >= 1
    for name in ("_mcp2", "_mcp2_dispatch", "_mcp2_dispatch_native"):
        assert name in v2
    # Nothing else from the companion leaks into __main__.
    leaked = [k for k in v2 if k.startswith("_op_") or k in ("_OPS", "_emit", "_V2Error")]
    assert leaked == []


def test_unknown_op(v2):
    env = run_dispatch(v2["_mcp2_dispatch"], "no_such_op", {})
    assert env == {**env, "ok": False, "code": "UNKNOWN_OP"}


def test_error_key_rule_uses_the_ops_code(v2):
    mod = v2["_mcp2"]
    mod._OPS["t_coded"] = lambda a: {"error": "pie is off", "code": "NOT_IN_PIE"}
    mod._OPS["t_uncoded"] = lambda a: {"error": "asset not found: /Game/X"}
    mod._OPS["t_partial"] = lambda a: {"errors": ["one item failed"], "done": 3}
    coded = run_dispatch(v2["_mcp2_dispatch"], "t_coded", {})
    assert coded["ok"] is False and coded["code"] == "NOT_IN_PIE"  # v1 returned ok:true here
    uncoded = run_dispatch(v2["_mcp2_dispatch"], "t_uncoded", {})
    assert uncoded["ok"] is False and uncoded["code"] == "ASSET_NOT_FOUND"
    partial = run_dispatch(v2["_mcp2_dispatch"], "t_partial", {})
    assert partial["ok"] is True and partial["result"]["done"] == 3


def test_v2_error_carries_code_and_details_without_traceback(v2):
    mod = v2["_mcp2"]

    def boom(a):
        raise mod._V2Error("CONFLICT", "2 actors are labeled 'Cube'", candidates=["/a", "/b"])

    mod._OPS["t_conflict"] = boom
    env = run_dispatch(v2["_mcp2_dispatch"], "t_conflict", {})
    assert env["ok"] is False and env["code"] == "CONFLICT"
    assert env["details"] == {"candidates": ["/a", "/b"]}
    assert "traceback" not in env


def test_python_exception_has_traceback(v2):
    def bad(a):
        raise RuntimeError("kaboom")

    v2["_mcp2"]._OPS["t_bad"] = bad
    env = run_dispatch(v2["_mcp2_dispatch"], "t_bad", {})
    assert env["ok"] is False and "RuntimeError" in env["traceback"]


def test_every_op_registered_once_and_defined():
    src = companion_source()
    block = re.search(r"\n_OPS\s*=\s*\{(.*?)\n\}", src, re.S).group(1)
    keys = re.findall(r'^\s*"([a-z_][a-z0-9_]*)"\s*:', block, re.M)
    assert len(keys) == len(set(keys)), "duplicate _OPS keys"
    for fn in re.findall(r":\s*(_op_[a-z0-9_]+)", block):
        assert re.search(r"^def %s\(" % fn, src, re.M), "_OPS references undefined %s" % fn


def test_boot_matches_go():
    """The hotload bootstrap in conftest must be the one Go sends (install.go)."""
    go = (REPO / "internal" / "bridge" / "install.go").read_text(encoding="utf-8")
    for line in ('_mcp2 = types.ModuleType(\\"mcp2\\")',
                 '\\"mcp2_bridge\\", \\"exec\\"), _mcp2.__dict__)',
                 "_mcp2_dispatch = _mcp2._mcp2_dispatch",
                 "_mcp2_dispatch_native = _mcp2._mcp2_dispatch_native",
                 "_MCP2_BRIDGE_VERSION = _mcp2._MCP2_BRIDGE_VERSION",
                 "globals()['_mcp_dispatch_native'] = _mcp2._mcp2_dispatch_native",
                 "globals()['_MCP_BRIDGE_VERSION'] = 0"):
        assert line in go, "install.go no longer contains: " + line
