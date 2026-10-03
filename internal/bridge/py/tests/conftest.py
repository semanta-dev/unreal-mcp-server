"""T2 contract tests for the companion module (docs/plans/OVERHAUL_PLAN.md §3.1).

The companion runs inside the Unreal Editor; here it runs in plain CPython against a
stub `unreal` module, which is enough to test everything that does not need a live
editor: the dispatch contract, error envelopes, the version sentinel, the op table,
and how the v1 and v2 companions coexist in one __main__ namespace.
"""
import base64
import io
import json
import pathlib
import sys
import types

import pytest

HERE = pathlib.Path(__file__).resolve().parent
PY_DIR = HERE.parent
FIXTURES = HERE / "fixtures"

MARKER = "__MCP_JSON__"


def companion_source():
    """The v2 companion exactly as Go embeds it: section files concatenated in
    filename order (internal/bridge/companion.go)."""
    parts = sorted(p for p in PY_DIR.glob("[0-9]*.py"))
    return "".join(p.read_text(encoding="utf-8") for p in parts)


def v1_source():
    return (FIXTURES / "mcp_bridge_v1.py").read_text(encoding="utf-8")


def v2_hotload_boot(src):
    """The v2 hotload bootstrap — must match internal/bridge/install.go
    (test_dispatch.py::test_boot_matches_go keeps the two in step)."""
    b64 = base64.b64encode(src.encode("utf-8")).decode("ascii")
    return ("import base64, types\n"
            "_mcp2 = types.ModuleType(\"mcp2\")\n"
            "exec(compile(base64.b64decode(\"%s\").decode(\"utf-8\"), \"mcp2_bridge\", \"exec\"), _mcp2.__dict__)\n"
            "_mcp2_dispatch = _mcp2._mcp2_dispatch\n"
            "_mcp2_dispatch_native = _mcp2._mcp2_dispatch_native\n"
            "_MCP2_BRIDGE_VERSION = _mcp2._MCP2_BRIDGE_VERSION" % b64)


V2_CLAIM = ("globals()['_mcp_dispatch_native'] = _mcp2._mcp2_dispatch_native\n"
            "globals()['_MCP_BRIDGE_VERSION'] = 0")


def v1_hotload(main, src):
    """v1's hotload: the module source exec'd straight into __main__ globals."""
    exec(compile(src, "mcp_bridge", "exec"), main)


def make_unreal_stub():
    m = types.ModuleType("unreal")

    class _Anything:
        def __init__(self, *a, **k):
            pass

        def __getattr__(self, name):
            return _Anything()

        def __call__(self, *a, **k):
            return _Anything()

    # Classes the module subclasses or references at import time.
    for name in ("Actor", "Blueprint", "Class", "Object", "Vector", "Rotator", "WorldSettings",
                 "StaticMeshComponent", "ActorComponent", "EditorActorSubsystem",
                 "UnrealEditorSubsystem", "LevelEditorSubsystem", "TopLevelAssetPath", "ARFilter"):
        setattr(m, name, type(name, (), {"__init__": lambda self, *a, **k: None}))
    m.__getattr__ = lambda name: _Anything()  # anything else resolves to a permissive stub
    return m


@pytest.fixture(autouse=True)
def unreal_stub(monkeypatch):
    monkeypatch.setitem(sys.modules, "unreal", make_unreal_stub())


def run_dispatch(fn, op, args=None):
    """Call a dispatch entry point and decode the envelope it prints."""
    b64 = base64.b64encode(json.dumps(args).encode()).decode() if args is not None else ""
    buf = io.StringIO()
    old = sys.stdout
    sys.stdout = buf
    try:
        fn(op, b64)
    finally:
        sys.stdout = old
    out = buf.getvalue()
    assert MARKER in out, "dispatch printed no envelope: %r" % out
    return json.loads(out[out.index(MARKER) + len(MARKER):].splitlines()[0])


@pytest.fixture
def v2():
    """A fresh __main__ with the v2 companion hot-loaded."""
    main = {"__name__": "__main__"}
    exec(v2_hotload_boot(companion_source()), main)
    return main
