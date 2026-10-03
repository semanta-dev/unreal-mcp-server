# --- P3 structured authoring (the Python-feasible 70%: logic in C++, Blueprints
# carry data + component composition; graph/node authoring is plugin-only, P7) --

def _resolve_class(path):
    if not path:
        return None
    if path.startswith("/Script/"):
        return unreal.load_class(None, path)
    asset = unreal.load_asset(path)
    if isinstance(asset, unreal.Blueprint):
        return asset.generated_class()
    return asset if isinstance(asset, unreal.Class) else None


def _maybe_asset(v):
    """Coerce an asset/class PATH string to the loaded UObject so it can be set on
    an object-valued property (e.g. a UStaticMesh* 'HeadMesh'): set_editor_property
    needs the object, not the path. Non-path values pass through unchanged."""
    if isinstance(v, str) and (v.startswith("/Game/") or v.startswith("/Script/")):
        obj = unreal.load_class(None, v) if v.startswith("/Script/") else None
        if obj is None:
            try:
                obj = unreal.load_asset(v)
            except Exception:
                obj = None
        if obj is not None:
            return obj
    return v


def _op_blueprint_create(args):
    parent = args["parent_class_path"]
    dest = args["dest"]
    parent_cls = _resolve_class(parent)
    if not parent_cls:
        return {"error": "parent class not found: " + str(parent), "code": "CLASS_UNRESOLVED"}
    pkg_path, name = dest.rsplit("/", 1)
    factory = unreal.BlueprintFactory()
    factory.set_editor_property("parent_class", parent_cls)
    dt = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, unreal.Blueprint, factory)
    if not dt:
        return {"error": "blueprint create failed", "code": "SPAWN_FAILED"}
    unreal.EditorAssetLibrary.save_asset(dest)
    return {"created": dest, "parent": parent}


def _op_blueprint_set_defaults(args):
    bp_path = args["blueprint"]
    bp = unreal.load_asset(bp_path)
    if not isinstance(bp, unreal.Blueprint):
        return {"error": "not a blueprint: " + str(bp_path), "code": "ASSET_NOT_FOUND"}
    cdo = unreal.get_default_object(bp.generated_class())
    errors = []
    defaults = args.get("defaults") or {}
    for k, v in defaults.items():
        try:
            cdo.set_editor_property(k, _maybe_asset(v))
        except Exception as e:
            errors.append(_issue("PROPERTY_SET_FAILED", k, str(e)))
    unreal.BlueprintEditorLibrary.compile_blueprint(bp)
    unreal.EditorAssetLibrary.save_asset(bp_path)
    return {"blueprint": bp_path, "set": list(defaults.keys()), "errors": errors}


def _op_assign_subclass(args):
    bp = unreal.load_asset(args["target"])
    if not isinstance(bp, unreal.Blueprint):
        return {"error": "target not a blueprint: " + str(args["target"]), "code": "ASSET_NOT_FOUND"}
    cls = _resolve_class(args["class_path"])
    if not cls:
        return {"error": "class not found: " + str(args["class_path"]), "code": "CLASS_UNRESOLVED"}
    cdo = unreal.get_default_object(bp.generated_class())
    try:
        cdo.set_editor_property(args["prop"], cls)
    except Exception as e:
        return {"error": "set " + str(args["prop"]) + ": " + str(e), "code": "PROPERTY_READONLY"}
    unreal.BlueprintEditorLibrary.compile_blueprint(bp)
    unreal.EditorAssetLibrary.save_asset(args["target"])
    return {"target": args["target"], "prop": args["prop"], "class": args["class_path"]}


def _op_blueprint_add_component(args):
    bp = unreal.load_asset(args["blueprint"])
    if not isinstance(bp, unreal.Blueprint):
        return {"error": "not a blueprint", "code": "ASSET_NOT_FOUND"}
    comp_cls = _resolve_class(args["component_class"])
    if not comp_cls:
        return {"error": "component class not found: " + str(args["component_class"]), "code": "CLASS_UNRESOLVED"}
    sub = unreal.get_engine_subsystem(unreal.SubobjectDataSubsystem)
    handles = sub.k2_gather_subobject_data_for_blueprint(bp)
    if not handles:
        return {"error": "no subobject data for blueprint", "code": "EDITOR_ERROR"}
    params = unreal.AddNewSubobjectParams()
    params.set_editor_property("parent_handle", handles[0])
    params.set_editor_property("new_class", comp_cls)
    params.set_editor_property("blueprint_context", bp)
    new_handle, fail = sub.add_new_subobject(params)
    if fail and str(fail):
        return {"error": "add component failed: " + str(fail), "code": "SPAWN_FAILED"}
    name = args.get("name") or ""
    if name:
        try:
            sub.rename_subobject(new_handle, unreal.Text(name))
        except Exception:
            pass
    unreal.SubobjectDataBlueprintFunctionLibrary.get_data(new_handle)  # no-op access to validate handle
    unreal.BlueprintEditorLibrary.compile_blueprint(bp)
    unreal.EditorAssetLibrary.save_asset(args["blueprint"])
    return {"blueprint": args["blueprint"], "component": args["component_class"], "name": name}


def _op_datatable_create(args):
    dest = args["dest"]
    rs = args["row_struct"]
    struct = unreal.load_object(None, rs) if rs.startswith("/Script/") else unreal.load_asset(rs)
    if not struct:
        return {"error": "row struct not found: " + str(rs), "code": "NOT_FOUND"}
    pkg_path, name = dest.rsplit("/", 1)
    factory = unreal.DataTableFactory()
    factory.set_editor_property("struct", struct)
    dt = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, unreal.DataTable, factory)
    if not dt:
        return {"error": "datatable create failed", "code": "SPAWN_FAILED"}
    unreal.EditorAssetLibrary.save_asset(dest)
    return {"created": dest, "row_struct": rs}


def _op_datatable_import(args):
    dt = unreal.load_asset(args["datatable"])
    if not isinstance(dt, unreal.DataTable):
        return {"error": "not a datatable: " + str(args["datatable"]), "code": "ASSET_NOT_FOUND"}
    # fill_data_table_from_{json,csv}_string return a BOOL (True=success), not a
    # problems list; per-row errors are logged by the editor (UE_LOG), not returned.
    ok = True
    if args.get("json"):
        ok = bool(unreal.DataTableFunctionLibrary.fill_data_table_from_json_string(dt, args["json"]))
    elif args.get("csv"):
        ok = bool(unreal.DataTableFunctionLibrary.fill_data_table_from_csv_string(dt, args["csv"]))
    unreal.EditorAssetLibrary.save_asset(args["datatable"])
    if not ok:
        return {"error": "datatable import reported failure (row/column errors in the editor log)",
                "code": "IMPORT_FAILED", "datatable": args["datatable"], "rows": len(dt.get_row_names())}
    return {"datatable": args["datatable"], "imported": True, "rows": len(dt.get_row_names())}


def _op_dataasset_create(args):
    cls = _resolve_class(args["class"])
    if not cls:
        return {"error": "class not found: " + str(args["class"]), "code": "CLASS_UNRESOLVED"}
    pkg_path, name = args["dest"].rsplit("/", 1)
    factory = unreal.DataAssetFactory()
    try:
        factory.set_editor_property("data_asset_class", cls)
    except Exception:
        pass
    da = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, cls, factory)
    if not da:
        return {"error": "dataasset create failed", "code": "SPAWN_FAILED"}
    unreal.EditorAssetLibrary.save_asset(args["dest"])
    return {"created": args["dest"], "class": args["class"]}
