"""Class references: widget_describe takes the resolver's short names, and a /Script
path in the wrong module names the class's real path (final eval aesir_hud_wave:
/Script/UnrealMCP.MCPHUDWidget, then three python calls to find the module)."""
import pytest
from conftest import run_dispatch
from fakeunreal import Fake, installed


@pytest.fixture
def ue(v2):
    fake = Fake()
    with installed(v2["_mcp2"], fake):
        fake.add_class("MCPHUDWidget", "/Script/MCPCapture.MCPHUDWidget")
        yield fake


def test_wrong_module_path_names_the_real_one(v2, ue):
    with pytest.raises(v2["_mcp2"]._V2Error) as e:
        v2["_mcp2"]._resolve_class_v2("/Script/UnrealMCP.MCPHUDWidget")
    assert e.value.code == "CLASS_UNRESOLVED" and "/Script/MCPCapture.MCPHUDWidget" in str(e.value)


def test_describe_resolves_a_short_name(v2, ue):
    seen = {}
    v2["_mcp2"]._op_reflect_class = lambda a: seen.setdefault("path", a["class_path"]) and {"class_path": a["class_path"]}
    run_dispatch(v2["_mcp2_dispatch"], "widget_describe", {"widget_class": "MCPHUDWidget"})
    assert seen["path"] == "/Script/MCPCapture.MCPHUDWidget"
