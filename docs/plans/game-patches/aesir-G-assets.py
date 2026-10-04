# Phase G content for aesir-wave-defense (remediation plan G.3/G.4): run once in the editor
# (python tool, or the editor's Python console) after the G C++ patch is built. Creates
# /Game/Data/DT_Waves (the built-in wave formula as rows), C_DamageFalloff (flat 1.0),
# DA_AesirTuning (pointing at both) and /Game/Input/IA_Dash + IMC_Aesir (Dash on Left
# Shift). Idempotent: existing assets are reused.
import json
import os
import tempfile

import unreal

R = {}
at = unreal.AssetToolsHelpers.get_asset_tools()
eal = unreal.EditorAssetLibrary


def ensure_dir(d):
    if not eal.does_directory_exist(d):
        eal.make_directory(d)


ensure_dir("/Game/Data")
ensure_dir("/Game/Input")

# --- DT_Waves: the built-in formula as data (EnemyCount = min(5 + (n-1)*3, 40)) -------
struct = unreal.load_object(None, "/Script/AesirWaveDefense.AesirWaveRow")
R["struct"] = str(struct)
dt_path = "/Game/Data/DT_Waves"
dt = unreal.load_asset(dt_path) if eal.does_asset_exist(dt_path) else None
if not dt:
    f = unreal.DataTableFactory()
    f.set_editor_property("struct", struct)
    dt = at.create_asset("DT_Waves", "/Game/Data", unreal.DataTable, f)
rows = [{"Name": "Wave_%02d" % n, "EnemyCount": min(5 + (n - 1) * 3, 40), "SpawnInterval": 0.8,
         "BruteChance": 0.2, "SprinterChance": 0.25} for n in range(1, 11)]
R["dt_fill"] = unreal.DataTableFunctionLibrary.fill_data_table_from_json_string(dt, json.dumps(rows))
eal.save_asset(dt_path)
R["dt_rows"] = [str(n) for n in unreal.DataTableFunctionLibrary.get_data_table_row_names(dt)]

# --- C_DamageFalloff: CSV import (Python cannot edit FRichCurve keys) -----------------
curve_path = "/Game/Data/C_DamageFalloff"
if not eal.does_asset_exist(curve_path):
    csv = os.path.join(tempfile.gettempdir(), "C_DamageFalloff.csv")
    with open(csv, "w") as fh:
        fh.write("0,1\n5000,1\n")  # time,value rows, NO header (a header row becomes a key)
    task = unreal.AssetImportTask()
    task.set_editor_property("filename", csv)
    task.set_editor_property("destination_path", "/Game/Data")
    task.set_editor_property("destination_name", "C_DamageFalloff")
    task.set_editor_property("automated", True)
    task.set_editor_property("save", True)
    fac = unreal.CSVImportFactory()
    settings = fac.get_editor_property("automated_import_settings")
    settings.set_editor_property("import_type", unreal.CSVImportType.ECSV_CURVE_FLOAT)
    fac.set_editor_property("automated_import_settings", settings)
    task.set_editor_property("factory", fac)
    at.import_asset_tasks([task])
    R["curve_import"] = [str(p) for p in task.get_editor_property("imported_object_paths")]
curve = unreal.load_asset(curve_path)
R["curve"] = str(curve)
if curve:
    R["curve_at_2000"] = curve.get_float_value(2000.0)

# --- DA_AesirTuning ------------------------------------------------------------------
da_path = "/Game/Data/DA_AesirTuning"
da = unreal.load_asset(da_path) if eal.does_asset_exist(da_path) else None
if not da:
    f = unreal.DataAssetFactory()
    f.set_editor_property("data_asset_class", unreal.load_class(None, "/Script/AesirWaveDefense.AesirTuning"))
    da = at.create_asset("DA_AesirTuning", "/Game/Data", unreal.load_class(None, "/Script/AesirWaveDefense.AesirTuning"), f)
da.set_editor_property("waves", dt)
if curve:
    da.set_editor_property("damage_falloff", curve)
eal.save_asset(da_path)
R["da"] = {k: str(da.get_editor_property(k)) for k in ("player_damage_multiplier", "enemy_health_multiplier", "waves", "damage_falloff")}

# --- IA_Dash + IMC_Aesir ---------------------------------------------------------------
ia_path = "/Game/Input/IA_Dash"
ia = unreal.load_asset(ia_path) if eal.does_asset_exist(ia_path) else None
if not ia:
    f = unreal.DataAssetFactory()
    f.set_editor_property("data_asset_class", unreal.InputAction)
    ia = at.create_asset("IA_Dash", "/Game/Input", unreal.InputAction, f)
eal.save_asset(ia_path)
imc_path = "/Game/Input/IMC_Aesir"
imc = unreal.load_asset(imc_path) if eal.does_asset_exist(imc_path) else None
if not imc:
    f = unreal.DataAssetFactory()
    f.set_editor_property("data_asset_class", unreal.InputMappingContext)
    imc = at.create_asset("IMC_Aesir", "/Game/Input", unreal.InputMappingContext, f)
imc.unmap_all_keys_from_action(ia)
key = unreal.Key()
key.import_text("LeftShift")
imc.map_key(ia, key)
eal.save_asset(imc_path)
R["imc"] = [(m.get_editor_property("action").get_name(), str(m.get_editor_property("key").get_editor_property("key_name")))
            for m in imc.get_editor_property("default_key_mappings").get_editor_property("mappings")]
print("RESULT " + json.dumps(R, default=str))
