"""R4 companion ops: HUD value bindings (checked against the widget's real fields, keyed
by widget+field, written all or nothing), mounting widgets in PIE and the live tree."""
import json

import pytest
from fakeunreal import _NS, Class
from test_widget_57 import CanvasPanel, Widget, call, ue  # noqa: F401  (ue is the fixture)


class Text(str):
    """unreal.Text as a value: its type name is Text."""


class TextBlockW(Widget):
    def get_editor_property(self, k):
        if k == "text":
            return Text("0")
        raise Exception("TextBlock: Failed to find property '%s'" % k)


class ProgressBarW(Widget):
    def get_editor_property(self, k):
        if k == "percent":
            return 0.5
        raise Exception("ProgressBar: Failed to find property '%s'" % k)


@pytest.fixture
def hud(v2, ue):  # noqa: F811
    ue.Text = Text
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 7)
    root = CanvasPanel(CanvasPanel, ue.wbp.tree, "Root")
    ue.wbp.tree.root = root
    for w in (TextBlockW(Class("TextBlock", "/Script/UMG.TextBlock"), ue.wbp.tree, "WaveText"),
              ProgressBarW(Class("ProgressBar", "/Script/UMG.ProgressBar"), ue.wbp.tree, "HealthBar")):
        root.add_child(w)
    store = {"value": [{"TargetWidget": "HealthBar", "TargetField": "Percent", "Source": "OwningPawn", "Path": "Health",
                        "MaxPath": "MaxHealth", "Conversion": "Ratio"}]}
    ue.auth.get_class_default_json = lambda bp, prop: json.dumps({"ok": True, "value": store["value"]})

    def set_cdj(bp, prop, raw):
        assert prop == "FieldSourceBindings"
        store["value"] = json.loads(raw)
        return json.dumps({"ok": True})

    ue.auth.set_class_default_json = set_cdj
    ue.store = store
    return ue


def test_bind_merges_by_widget_and_field(v2, hud):
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "WaveText", "field": "Text", "source": "game_state", "path": "WaveNumber", "conversion": "int_to_text"}]})
    assert env["ok"], env
    got = {(b["TargetWidget"], b["TargetField"]): b for b in hud.store["value"]}
    assert set(got) == {("HealthBar", "Percent"), ("WaveText", "Text")}  # the old binding stays
    assert got[("WaveText", "Text")]["Source"] == "GameState" and got[("WaveText", "Text")]["Conversion"] == "IntToText"
    # remove drops one; removing a binding that is not there is NOT_FOUND
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [{"widget": "HealthBar", "field": "Percent", "remove": True}]})
    assert env["ok"] and [b["TargetWidget"] for b in hud.store["value"]] == ["WaveText"], env
    assert call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "HealthBar", "field": "Percent", "remove": True}]})["code"] == "NOT_FOUND"


@pytest.mark.parametrize("binding,code,words", [
    ({"widget": "Nope", "field": "Text", "source": "pawn", "path": "X"}, "NOT_FOUND", "no widget"),
    ({"widget": "WaveText", "field": "Txt", "source": "pawn", "path": "X", "conversion": "int_to_text"}, "BAD_VALUE", "no field"),
    # a text conversion onto a float field, and a float write onto a text field: applied as nothing
    ({"widget": "HealthBar", "field": "Percent", "source": "pawn", "path": "X", "conversion": "int_to_text"}, "BAD_VALUE", "writes a text"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X"}, "BAD_VALUE", "writes a float"),
    ({"widget": "WaveText", "field": "Text", "source": "galaxy", "path": "X", "conversion": "int_to_text"}, "BAD_VALUE", "source must be"),
    ({"widget": "WaveText", "field": "Text", "source": "subsystem", "path": "X", "conversion": "int_to_text"}, "BAD_VALUE", "needs label"),
    ({"widget": "HealthBar", "field": "Percent", "source": "pawn", "path": "X", "conversion": "ratio"}, "BAD_VALUE", "max_path"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "conversion": "format_text"}, "BAD_VALUE", "format"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "conversion": "int_to_text"}, "BAD_VALUE", "needs path"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "colour": "red"}, "BAD_VALUE", "unknown keys"),
])
def test_bind_checks_every_binding_before_writing(v2, hud, binding, code, words):
    before = json.dumps(hud.store["value"])
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [binding]})
    assert env["code"] == code and words in env["error"], env
    assert json.dumps(hud.store["value"]) == before  # nothing written


def test_new_conversions_need_plugin_7(v2, hud):
    hud.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 6)
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "WaveText", "field": "Text", "source": "pawn", "path": "Speed", "conversion": "float_to_text"}]})
    assert env["code"] == "PLUGIN_MISSING", env
    hud.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 7)
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "HealthBar", "source": "pawn", "path": "bAlive", "conversion": "bool_to_visibility"}]})
    assert env["ok"] and ("HealthBar", "Visibility") in {(b["TargetWidget"], b["TargetField"]) for b in hud.store["value"]}, env


class Ctrl:
    def __init__(self):
        self.mounted = []

    def mount_widget(self, cls, z):
        self.mounted.append((cls, z))
        return _NS(get_name=lambda: "WBP_Hud_C_0")

    def unmount_widget(self, cls):
        n = len(self.mounted)
        self.mounted = []
        return n

    def describe_live_widgets(self, cls):
        return json.dumps([{"widget": "WBP_Hud_C_0", "in_viewport": True, "nodes": [{"name": "WaveText", "text": "3"}]}])


def test_mount_unmount_live_tree(v2, hud):
    m = v2["_mcp2"]
    ctrl = Ctrl()
    m._pie_control = lambda: ("PIE", ctrl)
    try:
        hud.classes["/Game/UI/WBP_Hud.WBP_Hud_C"] = hud.add_class("WBP_Hud_C", "/Game/UI/WBP_Hud.WBP_Hud_C")
        m._resolve_class = lambda p: hud.classes.get(p)
        env = call(v2, "widget_mount", {"class": "/Game/UI/WBP_Hud", "z_order": 50})
        assert env["ok"] and env["result"]["mounted"] == "WBP_Hud_C_0" and ctrl.mounted[0][1] == 50, env
        assert call(v2, "widget_mount", {"class": "/Game/UI/WBP_Nope"})["code"] == "CLASS_UNRESOLVED"
        env = call(v2, "widget_live_tree", {})
        assert env["ok"] and env["result"]["widgets"][0]["nodes"][0]["text"] == "3", env
        assert call(v2, "widget_unmount", {})["result"] == {"unmounted": 1}
    finally:
        del m._pie_control
