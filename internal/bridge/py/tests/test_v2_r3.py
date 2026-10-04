"""R3 companion ops (toolset `data`): DataTable keyed upsert/delete with field checks,
curve keys and Blueprint describe through the plugin (API 6), set_properties on assets,
add_variable, Enhanced Input mappings."""
import json

import pytest
from fakeunreal import _NS, Class, Fake, installed


class Struct:
    pass


class DataTable:
    """A DataTable as the engine's JSON export/import sees it: fill empties first."""

    def __init__(self, rows, row_struct):
        self.rows = {r["Name"]: {k: v for k, v in r.items() if k != "Name"} for r in rows}
        self.row_struct = row_struct
        self.modified = 0
        self.fail_fill = False

    def get_editor_property(self, k):
        assert k == "row_struct"
        return self.row_struct

    def modify(self):
        self.modified += 1

    def get_class(self):
        return Class("DataTable", "/Script/Engine.DataTable")


class AesirWaveRow:
    """**Editor Properties:**

- ``enemy_count`` (int32):  [Read-Write]
- ``spawn_interval`` (float):  [Read-Write]
"""


class Curve:
    def get_class(self):
        return Class("CurveFloat", "/Script/Engine.CurveFloat")


class Asset:
    def __init__(self, props):
        self.props = dict(props)
        self.modified = 0

    def get_class(self):
        return Class("AesirTuning", "/Script/Game.AesirTuning")

    def get_editor_property(self, k):
        if k not in self.props:
            raise Exception("Failed to find property '%s'" % k)
        return self.props[k]

    def set_editor_property(self, k, v):
        self.get_editor_property(k)
        if isinstance(v, str) and isinstance(self.props[k], float):
            raise TypeError("expected float")
        self.props[k] = v

    def modify(self):
        self.modified += 1


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = None
    row_struct = _NS(get_name=lambda: "AesirWaveRow", get_path_name=lambda: "/Script/Game.AesirWaveRow")
    fake.AesirWaveRow = AesirWaveRow
    fake.dt = DataTable([{"Name": "Wave_01", "EnemyCount": 5, "SpawnInterval": 0.8},
                         {"Name": "Wave_02", "EnemyCount": 8, "SpawnInterval": 0.8}], row_struct)
    fake.curve = Curve()
    fake.tuning = Asset({"player_damage_multiplier": 1.0})
    fake.DataTable, fake.CurveFloat, fake.Blueprint = DataTable, Curve, type("Blueprint", (), {})
    fake.assets.update({"/Game/Data/DT_Waves": fake.dt, "/Game/Data/C_Falloff": fake.curve, "/Game/Data/DA_Tuning": fake.tuning})
    saved = fake.saved = []
    fake.EditorAssetLibrary.save_asset = lambda p: saved.append(p) or True

    def export(dt):
        return json.dumps([dict({"Name": n}, **r) for n, r in dt.rows.items()])

    def fill(dt, raw):
        dt.rows = {}
        if dt.fail_fill:
            return False
        for r in json.loads(raw):
            row = {"EnemyCount": 0, "SpawnInterval": 1.0}
            row.update({k: v for k, v in r.items() if k != "Name" and k in ("EnemyCount", "SpawnInterval")})
            dt.rows[r["Name"]] = row
        return True

    def remove(dt, name):
        dt.rows.pop(str(name), None)

    fake.DataTableFunctionLibrary = _NS(export_data_table_to_json_string=export, fill_data_table_from_json_string=fill,
                                        remove_data_table_row=remove)
    fake.keys = [{"time": 0, "value": 1, "interp": "linear", "arrive_tangent": 0, "leave_tangent": 0}]

    def set_keys(curve, raw):
        k = json.loads(raw)
        if any(x.get("interp") not in (None, "linear", "constant", "cubic") for x in k):
            return "key 0: interp must be linear, constant or cubic"
        fake.keys = [dict({"interp": "linear", "arrive_tangent": 0, "leave_tangent": 0}, **x) for x in k]
        return ""

    fake.auth = _NS(get_curve_keys_json=lambda c: json.dumps(fake.keys), set_curve_keys_json=set_keys,
                    describe_blueprint_json=lambda bp, compile: json.dumps({"status": "up_to_date", "variables": []}))
    fake.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 6, peek_undo_title=lambda: "")
    with installed(v2["_mcp2"], fake):
        v2["_mcp2"]._mcp_authoring = lambda: fake.auth
        yield fake


def call(v2, op, args):
    m = v2["_mcp2"]
    try:
        return {"ok": True, "result": getattr(m, "_op_" + op)(args)}
    except m._V2Error as e:
        return {"ok": False, "code": e.code, "error": str(e)}


def test_table_read(v2, ue):
    env = call(v2, "data_table_read", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_02"]})
    assert env["ok"] and env["result"]["rows"] == {"Wave_02": {"EnemyCount": 8, "SpawnInterval": 0.8}}, env
    assert call(v2, "data_table_read", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_99"]})["code"] == "NOT_FOUND"
    assert call(v2, "data_table_read", {"asset": "/Game/Data/DA_Tuning"})["code"] == "BAD_VALUE"


def test_table_upsert_is_keyed_and_checked(v2, ue):
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves",
                                         "rows": {"Wave_02": {"enemy_count": 9}, "Wave_03": {"EnemyCount": 12}}})
    assert env["ok"] and env["result"]["created"] == ["Wave_03"] and env["result"]["updated"] == ["Wave_02"], env
    # Wave_01 untouched, Wave_02 keeps its other field, Wave_03 has defaults for the rest.
    assert ue.dt.rows == {"Wave_01": {"EnemyCount": 5, "SpawnInterval": 0.8}, "Wave_02": {"EnemyCount": 9, "SpawnInterval": 0.8},
                          "Wave_03": {"EnemyCount": 12, "SpawnInterval": 1.0}}
    assert ue.dt.modified == 1 and ue.saved == ["/Game/Data/DT_Waves"]
    # An unknown field is refused before anything changes (UE's import would ignore it).
    before = json.dumps(ue.dt.rows, sort_keys=True)
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves", "rows": {"Wave_01": {"EnemyCnt": 1}}})
    assert env["code"] == "BAD_VALUE" and "EnemyCnt" in env["error"] and json.dumps(ue.dt.rows, sort_keys=True) == before


def test_a_failed_fill_restores_the_rows(v2, ue):
    # The engine empties the table before filling it: a failure must never leave it empty.
    real_fill = ue.DataTableFunctionLibrary.fill_data_table_from_json_string
    calls = []

    def flaky(dt, raw):
        calls.append(raw)
        dt.fail_fill = len(calls) == 1  # the upsert fails, the restore succeeds
        return real_fill(dt, raw)

    ue.DataTableFunctionLibrary.fill_data_table_from_json_string = flaky
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves", "rows": {"Wave_01": {"EnemyCount": 6}}})
    assert env["code"] == "BAD_VALUE" and "unchanged" in env["error"], env
    assert ue.dt.rows["Wave_01"]["EnemyCount"] == 5 and len(ue.dt.rows) == 2


def test_table_delete_all_or_nothing(v2, ue):
    env = call(v2, "data_table_delete", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_01", "Wave_99"]})
    assert env["code"] == "NOT_FOUND" and len(ue.dt.rows) == 2, env
    env = call(v2, "data_table_delete", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_01"]})
    assert env["ok"] and list(ue.dt.rows) == ["Wave_02"] and env["result"]["total"] == 1, env


def test_curve_keys(v2, ue):
    env = call(v2, "data_curve_keys", {"asset": "/Game/Data/C_Falloff", "keys": [[0, 1], [5000, 0.25]]})
    assert env["ok"] and [k["value"] for k in env["result"]["keys"]] == [1, 0.25], env
    env = call(v2, "data_curve_keys", {"asset": "/Game/Data/C_Falloff", "keys": [{"time": 0, "value": 1, "interp": "smooth"}]})
    assert env["code"] == "BAD_VALUE" and "interp" in env["error"]
    assert call(v2, "data_curve_read", {"asset": "/Game/Data/C_Falloff"})["result"]["keys"][1]["time"] == 5000
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 5)
    assert call(v2, "data_curve_read", {"asset": "/Game/Data/C_Falloff"})["code"] == "PLUGIN_MISSING"


def test_set_properties(v2, ue):
    env = call(v2, "data_set_properties", {"asset": "/Game/Data/DA_Tuning", "properties": {"player_damage_multiplier": 1.5, "nope": 1}})
    assert env["ok"] and env["result"]["values"] == {"player_damage_multiplier": 1.5}, env
    assert [e["property"] for e in env["result"]["property_errors"]] == ["nope"] and ue.tuning.modified == 1
    env = call(v2, "data_set_properties", {"asset": "/Game/Data/DA_Tuning", "properties": {"nope": 1}})
    assert env["code"] == "BAD_VALUE" and ue.tuning.modified == 1  # refused before any transaction


def test_input_key_names_are_checked(v2, ue):
    class Key:
        def __init__(self):
            self.name = ""

        def import_text(self, t):
            self.name = t if t in ("LeftShift", "SpaceBar") else ""

        def get_editor_property(self, k):
            return self.name

    ue.Key = Key
    env = call(v2, "data_input_mapping", {"action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC_Aesir", "keys": ["LeftShfit"]})
    assert env["code"] == "BAD_VALUE" and "LeftShfit" in env["error"], env
    env = call(v2, "data_input_mapping", {"action": "IA_Dash", "context": "/Game/Input/IMC_Aesir", "keys": []})
    assert env["code"] == "BAD_VALUE"


class PinType:
    """EdGraphPinType as UE 5.7 Python has it: fields hidden, import/export text only."""

    def __init__(self, text='(PinCategory="int",PinSubCategory="")'):
        self.text = text

    def import_text(self, t):
        self.text = t
        return True

    def export_text(self):
        return self.text


def test_pin_types(v2, ue):
    m = v2["_mcp2"]
    ue.EdGraphPinType = PinType
    ue.BlueprintEditorLibrary = _NS(
        # Like 5.7: any name it does not know comes back as an int pin.
        get_basic_type_by_name=lambda n: PinType('(PinCategory="int")'),
        get_array_type=lambda t: PinType(t.text[:-1] + ',ContainerType=Array)'),
        get_set_type=lambda t: PinType(t.text[:-1] + ',ContainerType=Set)'),
        get_object_reference_type=lambda c: PinType('(PinCategory="object",PinSubCategoryObject=%s)' % c.get_name()),
        get_class_reference_type=lambda c: PinType('(PinCategory="class")'),
        get_struct_type=lambda s: PinType('(PinCategory="struct")'))
    assert m._pin_type("float").text == '(PinCategory="real",PinSubCategory="float")'
    assert "ContainerType=Array" in m._pin_type("array:float").text
    ue.add_class("Actor", "/Script/Engine.Actor")
    assert 'PinCategory="object"' in m._pin_type("object:/Script/Engine.Actor").text
    with pytest.raises(m._V2Error) as e:
        m._pin_type("floaty")
    assert e.value.code == "BAD_VALUE"
    # An engine that builds a different pin than asked is caught, not trusted.
    ue.BlueprintEditorLibrary.get_struct_type = lambda s: PinType('(PinCategory="int")')
    ue.add_class("Vector", "/Script/CoreUObject.Vector")
    with pytest.raises(m._V2Error) as e:
        m._pin_type("struct:/Script/CoreUObject.Vector")
    assert e.value.code == "EDITOR_ERROR"


def test_add_variable_refuses_a_taken_name(v2, ue):
    ue.EdGraphPinType = PinType
    added = []
    bp = type("Blueprint", (), {})()
    ue.Blueprint = type(bp)
    ue.assets["/Game/R3/BP"] = bp
    state = {"variables": [{"name": "Health"}], "components": [{"name": "Mesh"}], "functions": ["Fire"], "events": []}
    ue.auth.describe_blueprint_json = lambda b, compile: json.dumps(state)
    ue.BlueprintEditorLibrary = _NS(add_member_variable=lambda b, n, t: added.append(str(n)) or state["variables"].append({"name": str(n)}) or True,
                                    compile_blueprint=lambda b: None)
    for taken in ("health", "Mesh", "FIRE"):
        env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": taken, "type": "float"})
        assert env["code"] == "CONFLICT", env
    assert added == []
    env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "Armor", "type": "int"})
    assert env["ok"] and env["result"]["added"]["name"] == "Armor" and added == ["Armor"], env
