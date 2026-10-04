"""A dict for a struct property updates the struct in place: assigning it whole reset the
fields it did not name (gameeval aesir_hud_wave: font {size} dropped the font object, so
the HUD text drew as placeholder boxes)."""
import pytest
from fakeunreal import Fake, StructBase, installed


class Font(StructBase):
    pass


class Outline(StructBase):
    pass


class Widget:
    def __init__(self):
        self.props = {"font": Font(font_object="/Game/Fonts/Roboto", typeface_font_name="Bold", size=24,
                                   outline_settings=Outline(outline_size=0, outline_color="black")),
                      "visibility": "Visible"}

    def get_editor_property(self, k):
        if k not in self.props:
            raise Exception("Failed to find property '%s'" % k)
        return self.props[k]

    def set_editor_property(self, k, v):
        self.get_editor_property(k)
        self.props[k] = v


@pytest.fixture
def ue(v2):
    fake = Fake()
    with installed(v2["_mcp2"], fake):
        yield fake


def test_font_size_keeps_the_font_object(v2, ue):
    w = Widget()
    issues = v2["_mcp2"]._widget_apply_props(w, {"font": {"size": 26, "OutlineSettings": {"outline_size": 2}}})
    f = w.props["font"]
    assert issues == []
    assert f.get_editor_property("font_object") == "/Game/Fonts/Roboto" and f.get_editor_property("size") == 26
    assert f.get_editor_property("outline_settings").get_editor_property("outline_size") == 2
    assert f.get_editor_property("outline_settings").get_editor_property("outline_color") == "black"


def test_unknown_struct_field_is_reported_not_dropped(v2, ue):
    w = Widget()
    issues = v2["_mcp2"]._widget_apply_props(w, {"font": {"sise": 26}})
    assert [i["code"] for i in issues] == ["PROPERTY_SET_FAILED"], issues


def test_set_props_merges_structs_too(v2, ue):
    w = Widget()
    assert v2["_mcp2"]._set_props(w, {"font": {"size": 30}}) == []
    assert w.props["font"].get_editor_property("font_object") == "/Game/Fonts/Roboto"
    assert w.props["font"].get_editor_property("size") == 30
