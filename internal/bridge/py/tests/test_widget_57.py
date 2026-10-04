"""UE 5.7 widget authoring (plan G.5 / plugin API 4): 5.7 hides UWidgetBlueprint.WidgetTree,
UWidgetTree.RootWidget and UWidget.bIsVariable from Python, so widget_create /
widget_compose reach the tree through the MCPAuthoring plugin and register every widget
they create (the compiler ensures "Widget was added but did not get a GUID" otherwise)."""
import pytest
from conftest import run_dispatch
from fakeunreal import _NS, Fake, installed


class Widget:
    def __init__(self, cls, outer, name):
        self.cls, self.outer, self.name = cls, outer, str(name)
        self.children, self.slot, self.parent, self.props = [], None, None, {}

    def get_name(self):
        return self.name

    def get_outer(self):
        return self.outer

    def get_class(self):
        return self.cls

    def set_editor_property(self, k, v):
        if k == "is_variable":  # hidden in 5.7
            raise Exception("TextBlock: Failed to find property 'is_variable'")
        self.props[k] = v

    def remove_from_parent(self):
        if self.parent:
            self.parent.children.remove(self)
            self.parent = None


class Panel(Widget):
    def add_child(self, w):
        self.children.append(w)
        w.parent = self
        w.slot = Slot()
        return w.slot

    def get_children_count(self):
        return len(self.children)

    def get_child_at(self, i):
        return self.children[i]


class Slot:
    pass


class CanvasPanel(Panel):
    pass


class TextBlock(Widget):
    pass


class Tree:
    def __init__(self, wbp):
        self.wbp, self.root = wbp, None

    def get_outer(self):
        return self.wbp

    def get_editor_property(self, k):  # hidden in 5.7
        raise Exception("WidgetTree: Failed to find property '%s'" % k)

    set_editor_property = get_editor_property


class WBP:
    def __init__(self):
        self.tree = Tree(self)
        self.guids = set()

    def get_editor_property(self, k):  # hidden in 5.7
        raise Exception("WidgetBlueprint: Failed to find property '%s' for attribute '%s'" % (k, k))


class Authoring:
    """The plugin's MCPAuthoring editor subsystem (API 4)."""

    def __init__(self):
        self.registered, self.unregistered, self.variables = [], [], []

    def get_widget_tree(self, wbp):
        return wbp.tree

    def get_root_widget(self, wbp):
        return wbp.tree.root

    def set_root_widget(self, wbp, w):
        if w.get_outer() is not wbp.tree:
            return False
        wbp.tree.root = w
        return True

    def register_widget(self, wbp, w):
        wbp.guids.add(w.get_name())
        self.registered.append(w.get_name())
        return True

    def unregister_widget(self, wbp, name):
        wbp.guids.discard(str(name))
        self.unregistered.append(str(name))
        return True

    def set_widget_is_variable(self, w, v):
        self.variables.append(w.get_name())
        return True


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.CanvasPanel, fake.TextBlock = CanvasPanel, TextBlock
    fake.PanelWidget, fake.ContentWidget, fake.UserWidget = Panel, type("ContentWidget", (), {}), type("UserWidget", (), {})
    fake.WidgetBlueprint = WBP
    fake.CanvasPanelSlot = Slot
    fake.HorizontalBoxSlot = fake.VerticalBoxSlot = fake.OverlaySlot = fake.BorderSlot = type("NoSlot", (), {})
    fake.GridSlot = fake.UniformGridSlot = type("NoGrid", (), {})
    fake.Name = str
    fake.Text = str
    fake.new_object = lambda cls, outer=None, name=None: cls(cls, outer, name)
    fake.auth = Authoring()
    fake.MCPAuthoringSubsystem = "MCPAuthoringSubsystem"
    fake.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 4)
    fake.wbp = WBP()
    fake.WidgetBlueprintFactory = lambda: _NS(set_editor_property=lambda k, v: None)
    fake.AssetToolsHelpers = _NS(get_asset_tools=lambda: _NS(create_asset=lambda name, path, cls, factory: fake.wbp))
    fake.BlueprintEditorLibrary = _NS(compile_blueprint=lambda bp: None)
    fake.EditorAssetLibrary = _NS(save_asset=lambda p: True, load_asset=lambda p: fake.wbp,
                                  does_asset_exist=lambda p: True)
    fake.load_asset = lambda p: fake.wbp
    with installed(v2["_mcp2"], fake):
        v2["_mcp2"].unreal.get_editor_subsystem = lambda cls: fake.auth
        yield fake


def call(v2, op, args):
    if op == "widget_create":  # reached through asset_create kind=widget_blueprint
        try:
            return {"ok": True, "result": v2["_mcp2"]._op_widget_create(args)}
        except v2["_mcp2"]._V2Error as e:
            return {"ok": False, "code": e.code, "error": str(e)}
    return run_dispatch(v2["_mcp2_dispatch"], op, args)


def test_create_and_compose_through_the_plugin(v2, ue):
    env = call(v2, "widget_create", {"dest": "/Game/UI/WBP_X", "root_panel": "CanvasPanel"})
    assert env["ok"], env
    assert ue.wbp.tree.root.get_name() == "RootPanel" and "RootPanel" in ue.wbp.guids
    env = call(v2, "widget_compose", {"blueprint": "/Game/UI/WBP_X", "tree": {
        "name": "RootPanel", "class": "CanvasPanel", "children": [
            {"name": "WaveText", "class": "TextBlock", "is_variable": True, "props": {"Text": "WAVE ?"}}]}})
    assert env["ok"], env
    out = env["result"]
    assert out["issues"] == [], out["issues"]
    assert [c.get_name() for c in ue.wbp.tree.root.children] == ["WaveText"]
    assert ue.wbp.guids == {"RootPanel", "WaveText"}  # every created widget registered
    assert ue.auth.variables == ["WaveText"]           # is_variable through the plugin
    assert ue.wbp.tree.root.children[0].props["text"] == "WAVE ?"


def test_prune_unregisters_removed_widgets(v2, ue):
    call(v2, "widget_create", {"dest": "/Game/UI/WBP_X", "root_panel": "CanvasPanel"})
    call(v2, "widget_compose", {"blueprint": "/Game/UI/WBP_X", "tree": {"name": "RootPanel", "class": "CanvasPanel",
                                                                         "children": [{"name": "Old", "class": "TextBlock"}]}})
    env = call(v2, "widget_compose", {"blueprint": "/Game/UI/WBP_X", "prune": True,
                                      "tree": {"name": "RootPanel", "class": "CanvasPanel", "children": []}})
    assert env["ok"] and env["result"]["removed"] == ["Old"], env
    assert ue.auth.unregistered == ["Old"] and "Old" not in ue.wbp.guids


def test_unknown_slot_keys_are_reported_not_ignored(v2, ue):
    call(v2, "widget_create", {"dest": "/Game/UI/WBP_X", "root_panel": "CanvasPanel"})
    env = call(v2, "widget_compose", {"blueprint": "/Game/UI/WBP_X", "tree": {
        "name": "RootPanel", "class": "CanvasPanel",
        "children": [{"name": "T", "class": "TextBlock", "slot": {"position": [40, 160]}}]}})
    assert env["ok"]
    assert any(i["code"] == "SLOT_KEY_UNKNOWN" and i["label"] == "position" for i in env["result"]["issues"]), env


def test_hidden_tree_without_the_plugin_is_plugin_missing(v2, ue):
    ue.auth = None
    v2["_mcp2"].unreal.get_editor_subsystem = lambda cls: (_ for _ in ()).throw(Exception("no MCPAuthoringSubsystem"))
    env = call(v2, "widget_create", {"dest": "/Game/UI/WBP_X", "root_panel": "CanvasPanel"})
    assert not env["ok"] and env["code"] == "PLUGIN_MISSING", env
