import unreal
ar = unreal.AssetRegistryHelpers.get_asset_registry()
p = "/Game/T4/BP_T4.BP_T4"
def tryit(name, fn):
    try:
        d = fn()
        print(name, "->", d.is_valid() if d is not None else None, str(d.get_editor_property("asset_name")) if d is not None and d.is_valid() else "")
    except Exception as e:
        print(name, "EXC", str(e)[:120])
tryit("SoftObjectPath(str)", lambda: ar.get_asset_by_object_path(unreal.SoftObjectPath(p)))
print("sop repr", repr(unreal.SoftObjectPath(p)))
def sop_import():
    s = unreal.SoftObjectPath()
    s.import_text(p)
    return ar.get_asset_by_object_path(s)
tryit("import_text", sop_import)
tryit("helpers", lambda: unreal.AssetRegistryHelpers.get_asset_registry().get_asset_by_object_path(unreal.SoftObjectPath(asset_path_name=p)) if False else None)
def by_pkg():
    lst = ar.get_assets_by_package_name("/Game/T4/BP_T4")
    return lst[0] if lst else None
tryit("by_package", by_pkg)
