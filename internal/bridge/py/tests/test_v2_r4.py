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


def _lower_first(d):
    """What UE's FJsonObjectConverter export does to every key (StandardizeCase)."""
    return {k[0].lower() + k[1:]: v for k, v in d.items()}


_FIELDS = ("TargetWidget", "TargetField", "Source", "SourceLabel", "Path", "MaxPath", "Conversion", "Format")


@pytest.fixture
def hud(v2, ue):  # noqa: F811
    ue.Text = Text
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 7)
    root = CanvasPanel(CanvasPanel, ue.wbp.tree, "Root")
    ue.wbp.tree.root = root
    for w in (TextBlockW(Class("TextBlock", "/Script/UMG.TextBlock"), ue.wbp.tree, "WaveText"),
              TextBlockW(Class("TextBlock", "/Script/UMG.TextBlock"), ue.wbp.tree, "AmmoText"),
              ProgressBarW(Class("ProgressBar", "/Script/UMG.ProgressBar"), ue.wbp.tree, "HealthBar")):
        root.add_child(w)
    # Stored as the engine stores them; read back as it exports them (lowerCamel keys).
    store = {"value": [
        {"TargetWidget": "HealthBar", "TargetField": "Percent", "Source": "OwningPawn", "SourceLabel": "", "Path": "Health",
         "MaxPath": "MaxHealth", "Conversion": "Ratio", "Format": ""},
        {"TargetWidget": "AmmoText", "TargetField": "Text", "Source": "OwningPawn", "SourceLabel": "", "Path": "Ammo",
         "MaxPath": "", "Conversion": "IntToText", "Format": ""},
        {"TargetWidget": "WaveText", "TargetField": "Text", "Source": "GameState", "SourceLabel": "", "Path": "Wave",
         "MaxPath": "", "Conversion": "IntToText", "Format": ""}]}
    status = {"status": "up_to_date"}
    ue.auth.get_class_default_json = lambda bp, prop: json.dumps({"ok": True, "value": [_lower_first(b) for b in store["value"]]})
    ue.auth.describe_blueprint_json = lambda bp, compile: json.dumps({"status": status["status"]})

    def set_cdj(bp, prop, raw):
        assert prop == "FieldSourceBindings"
        # The import matches keys without case, as FJsonObjectConverter does.
        store["value"] = [{f: {k.lower(): v for k, v in b.items()}.get(f.lower(), "") for f in _FIELDS}
                          for b in json.loads(raw)]
        return json.dumps({"ok": True})

    ue.auth.set_class_default_json = set_cdj
    ue.auth.get_game_world = lambda: None  # no PIE
    ue.store, ue.status = store, status
    return ue


def _keys(hud):
    return [(b["TargetWidget"], b["TargetField"]) for b in hud.store["value"]]


def test_bind_merges_by_widget_and_field(v2, hud):
    # A fourth binding keeps the three stored ones (UE exports them as targetWidget...).
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "HealthBar", "field": "Visibility", "source": "pawn", "path": "bAlive", "conversion": "bool_to_visibility"}]})
    assert env["ok"], env
    assert _keys(hud) == [("HealthBar", "Percent"), ("AmmoText", "Text"), ("WaveText", "Text"), ("HealthBar", "Visibility")]
    # The answer speaks bind's vocabulary.
    assert env["result"]["bindings"][0] == {"widget": "HealthBar", "field": "Percent", "source": "pawn", "path": "Health",
                                            "conversion": "ratio", "max_path": "MaxHealth"}, env
    # A rebind replaces the binding on that field, in place; the others stay.
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "WaveText", "field": "Text", "source": "game_state", "path": "WaveNumber", "conversion": "int_to_text"}]})
    assert env["ok"] and len(hud.store["value"]) == 4, env
    got = {(b["TargetWidget"], b["TargetField"]): b for b in hud.store["value"]}
    assert got[("WaveText", "Text")]["Path"] == "WaveNumber" and got[("AmmoText", "Text")]["Path"] == "Ammo"
    # Remove drops one stored binding; a retried remove succeeds and says nothing was there.
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [{"widget": "AmmoText", "field": "Text", "remove": True}]})
    assert env["ok"] and env["result"]["removed"] == [{"widget": "AmmoText", "field": "Text", "removed": True}], env
    assert ("AmmoText", "Text") not in _keys(hud) and len(_keys(hud)) == 3
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [{"widget": "AmmoText", "field": "Text", "remove": True}]})
    assert env["ok"] and env["result"]["removed"][0]["removed"] is False and len(_keys(hud)) == 3, env


def test_bind_refuses_a_key_twice_in_one_call(v2, hud):
    before = json.dumps(hud.store["value"])
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "WaveText", "field": "Text", "source": "pawn", "path": "A", "conversion": "int_to_text"},
        {"widget": "WaveText", "field": "Text", "source": "pawn", "path": "B", "conversion": "int_to_text"}]})
    assert env["code"] == "BAD_VALUE" and "twice" in env["error"] and json.dumps(hud.store["value"]) == before, env


def test_bind_restores_the_bindings_when_the_blueprint_does_not_compile(v2, hud):
    before = json.dumps(hud.store["value"])
    hud.status["status"] = "error"
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
        {"widget": "WaveText", "field": "Text", "source": "pawn", "path": "Kills", "conversion": "int_to_text"}]})
    assert env["code"] == "EDITOR_ERROR" and "nothing changed" in env["error"], env
    assert json.dumps(hud.store["value"]) == before


def test_bind_refused_during_pie(v2, hud):
    v2["_mcp2"]._pie_running = lambda: True
    try:
        env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [
            {"widget": "WaveText", "field": "Text", "source": "pawn", "path": "Kills", "conversion": "int_to_text"}]})
        assert env["code"] == "PRECONDITION", env
    finally:
        del v2["_mcp2"]._pie_running


def test_bind_subsystem_label_must_be_a_native_subsystem(v2, hud):
    base = hud.add_class("GameInstanceSubsystem", "/Script/Engine.GameInstanceSubsystem")
    hud.GameInstanceSubsystem = base
    hud.WorldSubsystem = hud.add_class("WorldSubsystem", "/Script/Engine.WorldSubsystem")
    hud.LocalPlayerSubsystem = hud.add_class("LocalPlayerSubsystem", "/Script/Engine.LocalPlayerSubsystem")
    sub = hud.add_class("AesirAgentSubsystem", "/Script/AesirWaveDefense.AesirAgentSubsystem")
    sub.parent = base
    hud.add_class("Actor", "/Script/Engine.Actor")
    ok = {"widget": "WaveText", "field": "Text", "source": "subsystem", "label": "/Script/AesirWaveDefense.AesirAgentSubsystem",
          "path": "WaveNumber", "conversion": "int_to_text"}
    env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [ok]})
    assert env["ok"], env
    for label, words in (("/Script/Engine.Actor", "is not a"), ("/Game/BP_Sub", "not a native"), ("/Script/X.Nope", "not a native")):
        env = call(v2, "widget_bind", {"blueprint": "/Game/UI/WBP_Hud", "bindings": [dict(ok, label=label)]})
        assert env["code"] == "BAD_VALUE" and words in env["error"], (label, env)


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
    ({"widget": "WaveText", "field": "Text", "source": "ability_system", "path": "X", "conversion": "int_to_text"}, "BAD_VALUE", "ability system"),
    ({"widget": "WaveText", "field": "text", "source": "pawn", "path": "X", "conversion": "int_to_text"}, "BAD_VALUE", "CamelCase"),
    ({"widget": "WaveText", "field": "render_opacity", "source": "pawn", "path": "X"}, "BAD_VALUE", "CamelCase"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "conversion": "bool_to_visibility"}, "BAD_VALUE", "writes Visibility"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "conversion": "format_text", "format": "{hp}"}, "BAD_VALUE", "{hp}"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "conversion": "format_text", "format": "{value}/{max}"},
     "BAD_VALUE", "{max} needs max_path"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "conversion": "int_to_text", "max_path": "M"},
     "BAD_VALUE", "does not read max_path"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "conversion": "int_to_text", "format": "{value}"},
     "BAD_VALUE", "format goes with"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "label": "L", "path": "X", "conversion": "int_to_text"},
     "BAD_VALUE", "label goes with"),
    ({"widget": "WaveText", "field": "Text", "source": "pawn", "path": "X", "remove": True}, "BAD_VALUE", "widget and field only"),
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
    assert env["ok"] and ("HealthBar", "Visibility") in _keys(hud), env


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
        hud.UserWidget = hud.add_class("UserWidget", "/Script/UMG.UserWidget")
        wc = hud.add_class("WBP_Hud_C", "/Game/UI/WBP_Hud.WBP_Hud_C")
        wc.parent = hud.UserWidget
        hud.classes["/Game/UI/WBP_Hud.WBP_Hud_C"] = wc
        hud.add_class("Actor", "/Script/Engine.Actor")
        m._resolve_class = lambda p: hud.classes.get(p)
        env = call(v2, "widget_mount", {"class": "/Game/UI/WBP_Hud", "z_order": 50})
        assert env["ok"] and env["result"]["mounted"] == "WBP_Hud_C_0" and ctrl.mounted[0][1] == 50, env
        assert call(v2, "widget_mount", {"class": "/Game/UI/WBP_Hud", "z_order": 0})["ok"] and ctrl.mounted[1][1] == 0
        env = call(v2, "widget_mount", {"class": "/Script/Engine.Actor"})
        assert env["code"] == "BAD_VALUE" and "not a widget" in env["error"], env
        assert call(v2, "widget_mount", {"class": "/Game/UI/WBP_Nope"})["code"] == "CLASS_UNRESOLVED"
        env = call(v2, "widget_live_tree", {})
        assert env["ok"] and env["result"]["widgets"][0]["nodes"][0]["text"] == "3", env
        assert call(v2, "widget_unmount", {})["result"] == {"unmounted": 2}
    finally:
        del m._pie_control


def test_ui_shot_takes_one_frame_now(v2, hud):
    m = v2["_mcp2"]
    shots = []

    def capture_ui_frame(path):
        shots.append(path)
        if len(shots) == 2:
            return json.dumps({"ok": False, "error": "the viewport has no pixels (is its window minimized?)"})
        return json.dumps({"ok": True, "file": path, "width": 1280, "height": 720})

    sub = _NS(capture_ui_frame=capture_ui_frame)
    m._mcp_capture_subsystem = lambda: sub
    m._pie_running = lambda: True
    m._saved_mcp_dir = lambda *parts: "C:/P/Saved/MCP/" + "/".join(parts)
    try:
        env = call(v2, "pie_ui_shot", {})
        assert env["ok"] and env["result"]["width"] == 1280 and shots[0].startswith("C:/P/Saved/MCP/Screenshots/ui_"), env
        env = call(v2, "pie_ui_shot", {})
        assert env["code"] == "EDITOR_ERROR" and "minimized" in env["error"], env
        hud.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 6)
        assert call(v2, "pie_ui_shot", {})["code"] == "PLUGIN_MISSING"
        hud.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 7)
        m._pie_running = lambda: False
        assert call(v2, "pie_ui_shot", {})["code"] == "NOT_IN_PIE"
    finally:
        for k in ("_mcp_capture_subsystem", "_pie_running", "_saved_mcp_dir"):
            delattr(m, k)
