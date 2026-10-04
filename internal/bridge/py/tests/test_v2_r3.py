"""R3 companion ops (toolset `data`): DataTable keyed upsert/delete checked against the
table's columns, curve keys and Blueprint describe through the plugin (API 6),
set_properties on assets and settings classes, add_variable, Enhanced Input mappings.
The fakes model the engine's real behaviour, including what it does NOT check."""
import json

import pytest
from fakeunreal import _NS, Class, Fake, installed


class DataTable:
    """A DataTable as the engine's JSON export/import sees it: fill empties first, and
    the import matches only the columns' export names (an unknown key is ignored)."""
    COLUMNS = ("EnemyCount", "SpawnInterval")

    def __init__(self, rows):
        self.rows = {r["Name"]: {k: v for k, v in r.items() if k != "Name"} for r in rows}
        self.modified = 0
        self.fail_fill = False

    def get_editor_property(self, k):
        assert k == "row_struct"
        return _NS(get_name=lambda: "AesirWaveRow", get_path_name=lambda: "/Script/Game.AesirWaveRow")

    def modify(self):
        self.modified += 1

    def get_class(self):
        return Class("DataTable", "/Script/Engine.DataTable")


class CompositeDataTable(DataTable):
    pass


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


class Tx:
    """ScopedEditorTransaction: exiting commits, unless cancelled."""
    log = []

    def __init__(self, title):
        self.title, self.cancelled = title, False

    def __enter__(self):
        return self

    def __exit__(self, *a):
        Tx.log.append((self.title, "cancelled" if self.cancelled else "committed"))
        return False

    def cancel(self):
        self.cancelled = True


@pytest.fixture
def ue(v2):
    fake = Fake()
    fake.pie_actors = None
    Tx.log = []
    fake.ScopedEditorTransaction = Tx
    fake.dt = DataTable([{"Name": "Wave_01", "EnemyCount": 5, "SpawnInterval": 0.8},
                         {"Name": "Wave_02", "EnemyCount": 8, "SpawnInterval": 0.8}])
    fake.curve = Curve()
    fake.tuning = Asset({"player_damage_multiplier": 1.0})
    fake.DataTable, fake.CompositeDataTable, fake.CurveFloat = DataTable, CompositeDataTable, Curve
    fake.Blueprint = type("Blueprint", (), {})
    fake.assets.update({"/Game/Data/DT_Waves": fake.dt, "/Game/Data/C_Falloff": fake.curve, "/Game/Data/DA_Tuning": fake.tuning,
                        "/Game/Data/DT_Comp": CompositeDataTable([])})
    saved = fake.saved = []
    fake.save_ok = True
    fake.EditorAssetLibrary.save_asset = lambda p, only_if_is_dirty=True: (saved.append((p, only_if_is_dirty)) or fake.save_ok)

    def export(dt):
        return json.dumps([dict({"Name": n}, **r) for n, r in dt.rows.items()])

    def fill(dt, raw):
        dt.rows = {}
        if dt.fail_fill:
            return False
        for r in json.loads(raw):
            row = {"EnemyCount": 0, "SpawnInterval": 1.0}
            row.update({k: v for k, v in r.items() if k in DataTable.COLUMNS})  # like UE: unknown keys ignored
            dt.rows[r["Name"]] = row
        return True

    def remove(dt, name):
        dt.rows.pop(str(name), None)

    fake.DataTableFunctionLibrary = _NS(export_data_table_to_json_string=export, fill_data_table_from_json_string=fill,
                                        remove_data_table_row=remove,
                                        get_data_table_column_export_names=lambda dt: list(DataTable.COLUMNS))
    fake.keys = [{"time": 0, "value": 1, "interp": "linear", "arrive_tangent": 0, "leave_tangent": 0}]

    def set_keys(curve, raw):
        k = json.loads(raw)
        if any(x.get("interp") not in (None, "linear", "constant", "cubic", "none") for x in k):
            return "key 0: interp must be linear, constant, cubic or none"
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
    env = call(v2, "data_table_read", {"asset": "/Game/Data/DT_Waves", "rows": ["wave_02"]})  # FName: any case
    assert env["ok"] and env["result"]["rows"] == {"Wave_02": {"EnemyCount": 8, "SpawnInterval": 0.8}}, env
    assert env["result"]["columns"] == ["EnemyCount", "SpawnInterval"]
    assert call(v2, "data_table_read", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_99"]})["code"] == "NOT_FOUND"
    assert call(v2, "data_table_read", {"asset": "/Game/Data/DA_Tuning"})["code"] == "BAD_VALUE"
    assert call(v2, "data_table_read", {"asset": "/Game/Data/DT_Comp"})["code"] == "BAD_VALUE"  # composite
    assert len(call(v2, "data_table_read", {"asset": "/Game/Data/DT_Waves", "limit": -3})["result"]["rows"]) == 1


def test_table_upsert_is_keyed_and_checked(v2, ue):
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves",
                                         "rows": {"wave_02": {"enemy_count": 9}, "Wave_03": {"enemy_count": 12}}})
    assert env["ok"] and env["result"]["created"] == ["Wave_03"] and env["result"]["updated"] == ["Wave_02"], env
    # Wave_01 untouched; Wave_02 (named in another case) keeps its other field; Wave_03, a
    # NEW row given in snake_case, gets its value (the import matches export names only).
    assert ue.dt.rows == {"Wave_01": {"EnemyCount": 5, "SpawnInterval": 0.8}, "Wave_02": {"EnemyCount": 9, "SpawnInterval": 0.8},
                          "Wave_03": {"EnemyCount": 12, "SpawnInterval": 1.0}}
    assert ue.dt.modified == 1 and ue.saved == [("/Game/Data/DT_Waves", False)]
    assert Tx.log[-1][1] == "committed"
    # An unknown field is refused before anything changes (UE's import would ignore it).
    before = json.dumps(ue.dt.rows, sort_keys=True)
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves", "rows": {"Wave_01": {"EnemyCnt": 1}}})
    assert env["code"] == "BAD_VALUE" and "EnemyCnt" in env["error"] and json.dumps(ue.dt.rows, sort_keys=True) == before


def test_a_failed_fill_restores_the_rows_and_cancels_the_undo_step(v2, ue):
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
    assert Tx.log[-1][1] == "cancelled" and ue.saved == []  # no undo step, nothing saved
    # A restore that also fails says so (never "unchanged").
    ue.DataTableFunctionLibrary.fill_data_table_from_json_string = lambda dt, raw: False
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves", "rows": {"Wave_01": {"EnemyCount": 6}}})
    assert env["code"] == "EDITOR_ERROR" and "RESTORING THE TABLE FAILED" in env["error"], env


def test_table_delete_all_or_nothing(v2, ue):
    env = call(v2, "data_table_delete", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_01", "Wave_99"]})
    assert env["code"] == "NOT_FOUND" and len(ue.dt.rows) == 2, env
    env = call(v2, "data_table_delete", {"asset": "/Game/Data/DT_Waves", "rows": ["WAVE_01"]})
    assert env["ok"] and list(ue.dt.rows) == ["Wave_02"] and env["result"]["deleted"] == ["Wave_01"], env


def test_a_save_that_fails_is_an_error(v2, ue):
    ue.save_ok = False
    env = call(v2, "data_set_properties", {"asset": "/Game/Data/DA_Tuning", "properties": {"player_damage_multiplier": 2.0}})
    assert env["code"] == "EDITOR_ERROR" and "did not save" in env["error"], env


def test_curve_keys(v2, ue):
    env = call(v2, "data_curve_keys", {"asset": "/Game/Data/C_Falloff", "keys": [[0, 1], [5000, 0.25]]})
    assert env["ok"] and [k["value"] for k in env["result"]["keys"]] == [1, 0.25], env
    env = call(v2, "data_curve_keys", {"asset": "/Game/Data/C_Falloff", "keys": [{"time": 0, "value": 1, "interp": "smooth"}]})
    assert env["code"] == "BAD_VALUE" and "interp" in env["error"]
    assert Tx.log[-1][1] == "cancelled"  # refused: no undo step
    assert call(v2, "data_curve_read", {"asset": "/Game/Data/C_Falloff"})["result"]["keys"][1]["time"] == 5000
    ue.MCPCoreLibrary = _NS(get_plugin_api_version=lambda: 5)
    assert call(v2, "data_curve_read", {"asset": "/Game/Data/C_Falloff"})["code"] == "PLUGIN_MISSING"


def test_set_properties(v2, ue):
    env = call(v2, "data_set_properties", {"asset": "/Game/Data/DA_Tuning", "properties": {"player_damage_multiplier": 1.5, "nope": 1}})
    assert env["ok"] and env["result"]["values"] == {"player_damage_multiplier": 1.5}, env
    assert [e["property"] for e in env["result"]["property_errors"]] == ["nope"] and ue.tuning.modified == 1
    env = call(v2, "data_set_properties", {"asset": "/Game/Data/DA_Tuning", "properties": {"nope": 1}})
    assert env["code"] == "BAD_VALUE" and ue.tuning.modified == 1  # refused before any transaction


def test_settings_are_written_to_config(v2, ue):
    ue.add_class("GeneralProjectSettings", "/Script/EngineSettings.GeneralProjectSettings")

    def set_cfg(cls, raw):
        props = json.loads(raw)
        errors = [{"property": k, "error": "no property " + k} for k in props if k != "ProjectVersion"]
        values = {"ProjectVersion": props["ProjectVersion"]} if "ProjectVersion" in props else {}
        out = {"ok": bool(values), "values": values, "errors": errors}
        if not values:
            out["error"] = "no property was set"
        return json.dumps(out)

    ue.auth.set_config_defaults_json = set_cfg
    env = call(v2, "data_set_settings", {"class": "/Script/EngineSettings.GeneralProjectSettings",
                                         "properties": {"ProjectVersion": "9.9.9", "nope": 1}})
    assert env["ok"] and env["result"]["values"] == {"ProjectVersion": "9.9.9"}, env
    assert [e["property"] for e in env["result"]["property_errors"]] == ["nope"]
    env = call(v2, "data_set_settings", {"class": "/Script/EngineSettings.GeneralProjectSettings", "properties": {"nope": 1}})
    assert env["code"] == "BAD_VALUE", env


class Key:
    """unreal.Key: import_text takes ANY name (FKey::ImportTextItem never checks it)."""

    def __init__(self):
        self.name = ""

    def import_text(self, t):
        self.name = t

    def get_editor_property(self, k):
        return self.name


def test_input_key_names_are_checked(v2, ue):
    ue.Key = Key
    ue.InputLibrary = _NS(key_is_valid=lambda k: k.name in ("LeftShift", "E"))
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


def _bp_fixture(ue, state, rename=None):
    ue.EdGraphPinType = PinType
    bp = ue.Blueprint()
    ue.assets["/Game/R3/BP"] = bp
    removed = []
    ue.auth.describe_blueprint_json = lambda b, compile: json.dumps(state)
    # Kismet's validator: names of this Blueprint AND what it inherits (Tags on Actor).
    taken = {"health", "mesh", "fire", "tags"}
    ue.auth.check_member_name = lambda b, n: ("%s is already used" % n) if str(n).lower() in taken else ""
    ue.auth.remove_member_variable = lambda b, n: removed.append(str(n)) or True

    def add(b, n, t):
        state["variables"].append({"name": rename(str(n)) if rename else str(n)})
        return True

    ue.BlueprintEditorLibrary = _NS(add_member_variable=add, compile_blueprint=lambda b: None)
    return removed


def test_add_variable_refuses_a_taken_name_including_inherited(v2, ue):
    state = {"variables": [{"name": "Health"}], "components": [], "functions": [], "events": []}
    removed = _bp_fixture(ue, state)
    for taken in ("health", "Tags", "FIRE"):
        env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": taken, "type": "float"})
        assert env["code"] == "CONFLICT", env
    env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "Armor", "type": "int"})
    assert env["ok"] and env["result"]["added"]["name"] == "Armor" and removed == [], env


def test_add_variable_removes_a_renamed_stray(v2, ue):
    # Should the engine still rename it (Armor -> Armor_0), the stray is removed, not left.
    state = {"variables": [], "components": [], "functions": [], "events": []}
    removed = _bp_fixture(ue, state, rename=lambda n: n + "_0")
    env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "Armor", "type": "int"})
    assert env["code"] == "EDITOR_ERROR" and removed == ["Armor_0"], env


def test_add_variable_refuses_during_pie(v2, ue):
    _bp_fixture(ue, {"variables": [], "components": [], "functions": [], "events": []})
    ue.pie_actors = []
    env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "Armor", "type": "int"})
    assert env["code"] == "PRECONDITION", env


def test_stray_cleanup_never_touches_a_look_alike(v2, ue):
    # Armor_Max existed before; the engine renames the new Armor to Armor_0: only Armor_0 goes.
    state = {"variables": [{"name": "Armor_Max"}], "components": [], "functions": [], "events": []}
    removed = _bp_fixture(ue, state, rename=lambda n: n + "_0")
    env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "Armor", "type": "int"})
    assert env["code"] == "EDITOR_ERROR" and removed == ["Armor_0"], (env, removed)


# R6.2: dry_run checks everything a real call checks first, reports the change, and
# changes nothing (no transaction, no save, no asset created).

def test_dry_runs_change_nothing(v2, ue):
    before = json.dumps(ue.dt.rows, sort_keys=True)
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves", "dry_run": True,
                                         "rows": {"wave_02": {"enemy_count": 9}, "Wave_03": {"enemy_count": 12}}})
    r = env["result"]
    assert env["ok"] and r["dry_run"] and r["created"] == ["Wave_03"] and r["updated"] == ["Wave_02"], env
    assert r["changes"]["Wave_02"] == {"EnemyCount": {"from": 8, "to": 9}}
    env = call(v2, "data_table_upsert", {"asset": "/Game/Data/DT_Waves", "dry_run": True, "rows": {"Wave_01": {"EnemyCnt": 1}}})
    assert env["code"] == "BAD_VALUE"  # the same checks as the real call
    env = call(v2, "data_table_delete", {"asset": "/Game/Data/DT_Waves", "rows": ["wave_01"], "dry_run": True})
    assert env["ok"] and env["result"]["deleted"] == ["Wave_01"] and env["result"]["total"] == 1, env
    assert call(v2, "data_table_delete", {"asset": "/Game/Data/DT_Waves", "rows": ["Wave_09"], "dry_run": True})["code"] == "NOT_FOUND"
    env = call(v2, "data_set_properties", {"asset": "/Game/Data/DA_Tuning", "dry_run": True,
                                           "properties": {"player_damage_multiplier": 1.5, "nope": 1}})
    r = env["result"]
    assert env["ok"] and r["changes"] == {"player_damage_multiplier": {"from": 1.0, "to": 1.5}}, env
    assert [e["property"] for e in r["property_errors"]] == ["nope"]
    assert json.dumps(ue.dt.rows, sort_keys=True) == before and ue.dt.modified == 0 and ue.tuning.modified == 0
    assert ue.saved == [] and all(entry[1] != "committed" for entry in Tx.log)


def test_add_variable_and_input_mapping_dry_runs(v2, ue):
    state = {"variables": [{"name": "Health"}], "components": [], "functions": [], "events": []}
    _bp_fixture(ue, state)
    env = call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "Armor", "type": "int", "dry_run": True})
    assert env["ok"] and env["result"]["would_add"]["name"] == "Armor" and state["variables"] == [{"name": "Health"}], env
    assert call(v2, "data_add_variable", {"asset": "/Game/R3/BP", "name": "health", "type": "int", "dry_run": True})["code"] == "CONFLICT"
    ue.Key = Key
    ue.InputLibrary = _NS(key_is_valid=lambda k: k.name in ("LeftShift", "E"))
    registry = {"/Game/Input/IMC_Aesir": "InputMappingContext", "/Game/Input/IA_Sub": "MyInputAction"}
    ia = ue.add_class("InputAction", "/Script/EnhancedInput.InputAction")
    ue.add_class("InputMappingContext", "/Script/EnhancedInput.InputMappingContext")
    sub = ue.add_class("MyInputAction", "/Script/Game.MyInputAction")
    sub.parent = ia  # a subclass passes, as it does in the real call
    ue.InputAction, ue.InputMappingContext = ia, ue.classes["/Script/EnhancedInput.InputMappingContext"]
    pkgs = {"InputMappingContext": "/Script/EnhancedInput", "MyInputAction": "/Script/Game"}

    def class_path(name):
        return _NS(get_editor_property=lambda k: pkgs[name] if k == "package_name" else name)

    def by_package(pkg):
        if pkg not in registry:
            return []
        return [_NS(get_editor_property=lambda k, pkg=pkg: pkg.rsplit("/", 1)[-1] if k == "asset_name"
                    else class_path(registry[pkg]))]

    ue.AssetRegistryHelpers = _NS(get_asset_registry=lambda: _NS(get_assets_by_package_name=by_package))
    env = call(v2, "data_input_mapping", {"action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC_Aesir",
                                          "keys": ["LeftShift"], "dry_run": True})
    assert env["ok"] and env["result"]["creates"] == ["/Game/Input/IA_Dash"] and ue.saved == [], env
    # A context given as the action: refused like the real call (not "ok" in the dry run).
    env = call(v2, "data_input_mapping", {"action": "/Game/Input/IMC_Aesir", "context": "/Game/Input/IMC_Aesir",
                                          "keys": ["LeftShift"], "dry_run": True})
    assert env["code"] == "BAD_VALUE" and "not a InputAction" in env["error"], env
    env = call(v2, "data_input_mapping", {"action": "/Game/Input/IA_Sub", "context": "/Game/Input/IMC_Aesir",
                                          "keys": ["LeftShift"], "dry_run": True})
    assert env["ok"] and env["result"]["creates"] == [], env
    env = call(v2, "data_input_mapping", {"action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC_Aesir",
                                          "keys": ["LeftShfit"], "dry_run": True})
    assert env["code"] == "BAD_VALUE"
