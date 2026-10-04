# --- R3: data and logic authoring (toolset `data`) ---------------------------------------
# DataTables (typed rows, keyed upsert/delete — never replace-all), float curves (plugin
# API 6: Python cannot read FRichCurve keys), Blueprint describe (plugin API 6) and
# add_variable, property edits on any asset, Enhanced Input actions and mapping contexts.


def _data_asset(args, kind=None):
    path = args.get("asset") or ""
    if not path:
        raise _V2Error("BAD_VALUE", "asset is required")
    obj = unreal.load_asset(path)
    if obj is None:
        raise _V2Error("NOT_FOUND", "no asset %s" % path)
    if kind is not None and not isinstance(obj, kind):
        raise _V2Error("BAD_VALUE", "%s is a %s, not a %s" % (path, obj.get_class().get_name(), kind.__name__))
    return path, obj


def _norm(name):
    return str(name).lower().replace("_", "")


def _table_rows(dt):
    """The table's rows as {row name: {field: value}} (the engine's own JSON export, so
    field names are the struct's C++ names and values its JSON forms)."""
    raw = unreal.DataTableFunctionLibrary.export_data_table_to_json_string(dt)
    rows = {}
    for r in json.loads(raw or "[]"):
        name = r.pop("Name", None)
        if name is not None:
            rows[str(name)] = r
    return rows


def _row_fields(dt):
    """{normalized field name: C++ field name} of the table's row struct, from its
    UE-generated docstring (Python names are snake_case; the JSON uses the C++ names)."""
    st = dt.get_editor_property("row_struct")
    pytype = getattr(unreal, st.get_name(), None) if st is not None else None
    fields = {}
    for name, _type in _FIELD_DOC.findall(getattr(pytype, "__doc__", None) or ""):
        fields[_norm(name)] = name
    return st, fields


def _op_data_table_read(args):
    path, dt = _data_asset(args, unreal.DataTable)
    rows = _table_rows(dt)
    want = args.get("rows")
    if want:
        missing = [r for r in want if r not in rows]
        if missing:
            raise _V2Error("NOT_FOUND", "%s has no rows %s" % (path, ", ".join(missing)), rows=sorted(rows)[:50])
        rows = {r: rows[r] for r in want}
    limit = int(args.get("limit") or 200)
    names = list(rows)[:limit]
    st = dt.get_editor_property("row_struct")
    return {"asset": path, "row_struct": st.get_path_name() if st else None, "total": len(rows),
            "truncated": len(rows) > limit, "rows": {n: rows[n] for n in names}}


def _check_row_fields(path, dt, rows):
    """Every field named in rows exists on the row struct (UE's JSON import ignores an
    unknown field: a typo would silently keep the old value). Returns the rows with
    field names normalized to the struct's JSON names."""
    st, fields = _row_fields(dt)
    if not fields:
        raise _V2Error("BAD_VALUE", "%s: the row struct %s documents no fields, so a row cannot be checked"
                       % (path, st.get_name() if st else "?"))
    out = {}
    for name, row in rows.items():
        if not isinstance(row, dict):
            raise _V2Error("BAD_VALUE", "row %s must be an object of field -> value" % name)
        fixed = {}
        for k, v in row.items():
            if _norm(k) not in fields:
                raise _V2Error("BAD_VALUE", "row %s: %s has no field %r" % (name, st.get_name(), k),
                               fields=sorted(fields.values()))
            fixed[k] = v
        out[str(name)] = fixed
    return out


def _json_field(existing, key):
    """The existing row's key for a field given in any spelling (the export uses the
    C++ name: EnemyCount; a caller may write enemy_count)."""
    for k in existing:
        if _norm(k) == _norm(key):
            return k
    return key


def _op_data_table_upsert(args):
    """Insert or update the named rows; every other row is written back unchanged (the
    engine has no single-row add, so the table is re-filled from its own export with
    only these rows changed). Fields not given keep their values (new rows: the
    struct's defaults). One undo step."""
    path, dt = _data_asset(args, unreal.DataTable)
    rows = args.get("rows") or {}
    if not isinstance(rows, dict) or not rows:
        raise _V2Error("BAD_VALUE", "rows must be {row name: {field: value}}")
    rows = _check_row_fields(path, dt, rows)
    current = _table_rows(dt)
    created, updated = [], []
    for name, fields in rows.items():
        base = current.get(name)
        if base is None:
            created.append(name)
            base = {}
        else:
            updated.append(name)
        for k, v in fields.items():
            base[_json_field(base, k)] = v
        current[name] = base
    original = unreal.DataTableFunctionLibrary.export_data_table_to_json_string(dt)
    payload = [dict({"Name": n}, **r) for n, r in current.items()]
    fill = unreal.DataTableFunctionLibrary.fill_data_table_from_json_string
    with _transaction("MCP: upsert rows in " + path.rsplit("/", 1)[-1]):
        dt.modify()
        # The engine empties the table before filling it: any failure restores the
        # original rows inside this same transaction — never an emptied table.
        problem = None if fill(dt, json.dumps(payload)) else "%s rejected the rows (the editor log names the field)" % path
        if problem is None:
            after = _table_rows(dt)
            for name, fields in rows.items():  # read back: the import must have kept every value
                got = after.get(name) or {}
                for k, v in fields.items():
                    have = got.get(_json_field(got, k))
                    if have != v and not _same_json(have, v):
                        problem = "row %s: %s did not take the value %r (read back %r)" % (name, k, v, have)
        if problem is not None:
            fill(dt, original)
            raise _V2Error("BAD_VALUE", problem + "; the table is unchanged")
    unreal.EditorAssetLibrary.save_asset(path)
    return {"asset": path, "created": created, "updated": updated, "total": len(after)}


def _same_json(a, b):
    """Numbers compare as numbers (the export writes 0.8 as 0.80000001192092896)."""
    if isinstance(a, (int, float)) and isinstance(b, (int, float)) and not isinstance(a, bool):
        return abs(float(a) - float(b)) <= 1e-5 * max(1.0, abs(float(b)))
    return a == b


def _op_data_table_delete(args):
    """Delete the named rows — all of them or none (a missing name is NOT_FOUND)."""
    path, dt = _data_asset(args, unreal.DataTable)
    names = [str(n) for n in (args.get("rows") or [])]
    if not names:
        raise _V2Error("BAD_VALUE", "rows must name the rows to delete")
    current = _table_rows(dt)
    missing = [n for n in names if n not in current]
    if missing:
        raise _V2Error("NOT_FOUND", "%s has no rows %s (nothing deleted)" % (path, ", ".join(missing)))
    with _transaction("MCP: delete rows from " + path.rsplit("/", 1)[-1]):
        dt.modify()
        for n in names:
            unreal.DataTableFunctionLibrary.remove_data_table_row(dt, unreal.Name(n))
    unreal.EditorAssetLibrary.save_asset(path)
    return {"asset": path, "deleted": names, "total": len(_table_rows(dt))}


def _op_data_curve_read(args):
    path, curve = _data_asset(args, unreal.CurveFloat)
    _need_plugin(6, "curve keys")
    return {"asset": path, "keys": json.loads(_authoring().get_curve_keys_json(curve) or "[]")}


def _authoring():
    auth = _mcp_authoring()
    if auth is None:
        raise _V2Error("PLUGIN_MISSING", "the UnrealMCP plugin's MCPAuthoring module is not loaded")
    return auth


def _op_data_curve_keys(args):
    """Replace a float curve's keys (all or nothing: the plugin validates them first).
    One undo step."""
    path, curve = _data_asset(args, unreal.CurveFloat)
    _need_plugin(6, "curve keys")
    keys = args.get("keys")
    if not isinstance(keys, list) or not keys:
        raise _V2Error("BAD_VALUE", "keys must be [{time, value, interp?}, ...]")
    keys = [{"time": k[0], "value": k[1]} if isinstance(k, (list, tuple)) and len(k) == 2 else k for k in keys]
    with _transaction("MCP: set keys of " + path.rsplit("/", 1)[-1]):
        why = _authoring().set_curve_keys_json(curve, json.dumps(keys))
        if why:
            raise _V2Error("BAD_VALUE", "%s: %s" % (path, why))
    unreal.EditorAssetLibrary.save_asset(path)
    return {"asset": path, "keys": json.loads(_authoring().get_curve_keys_json(curve) or "[]")}


def _op_data_blueprint(args):
    """Describe a Blueprint (plugin API 6): components, variables, functions, events,
    status; it is compiled in memory first (not saved) so status and messages are current."""
    path, bp = _data_asset(args, unreal.Blueprint)
    _need_plugin(6, "Blueprint describe")
    if _pie_running():
        raise _V2Error("PRECONDITION", "describing compiles the Blueprint, which would change the running game: stop PIE first")
    out = json.loads(_authoring().describe_blueprint_json(bp, True))
    if out.get("error"):
        raise _V2Error("PRECONDITION", "%s: %s" % (path, out["error"]))
    return out


def _op_data_set_properties(args):
    """Set properties on any asset (DataAsset, PrimaryDataAsset, curve, sound class…),
    per-property errors; one undo step; saved."""
    path, obj = _data_asset(args)
    if isinstance(obj, unreal.Blueprint):
        raise _V2Error("BAD_VALUE", "%s is a Blueprint: set its class defaults with asset_edit op=set_defaults" % path)
    props = args.get("properties") or {}
    if not isinstance(props, dict) or not props:
        raise _V2Error("BAD_VALUE", "properties must be {property: value}")
    unknown = []
    for k in props:
        try:
            obj.get_editor_property(k)
        except Exception as e:
            unknown.append({"property": k, "error": str(e)})
    if len(unknown) == len(props):  # before any transaction: nothing would change
        raise _V2Error("BAD_VALUE", "%s has none of these properties" % path, property_errors=unknown)
    with _transaction("MCP: set properties on " + path.rsplit("/", 1)[-1]):
        obj.modify()
        errors = _set_props(obj, {k: v for k, v in props.items() if k not in {u["property"] for u in unknown}}) + unknown
    unreal.EditorAssetLibrary.save_asset(path)
    values = {}
    for k in props:
        try:
            values[k] = _coerce_prop(obj.get_editor_property(k), 512)
        except Exception:
            pass
    return {"asset": path, "values": values, "property_errors": errors}


# Variable types for add_variable: (pin category, sub-category). UE 5.7's
# get_basic_type_by_name turns any name it does not know — "float" and "double" among
# them — into an int pin, silently (live R3), so the pin is built from its exact
# category and checked through export_text.
_BASIC_PIN_TYPES = {"bool": ("bool", ""), "byte": ("byte", ""), "int": ("int", ""), "int64": ("int64", ""),
                    "float": ("real", "float"), "double": ("real", "double"), "name": ("name", ""),
                    "string": ("string", ""), "text": ("text", "")}


def _checked_pin(t, category, container=None, what=""):
    txt = t.export_text()
    if 'PinCategory="%s"' % category not in txt or (container and "ContainerType=%s" % container not in txt):
        raise _V2Error("EDITOR_ERROR", "the engine built a different pin type for %s: %s" % (what, txt[:160]))
    return t


def _pin_type(spec):
    L = unreal.BlueprintEditorLibrary
    s = str(spec or "").strip()
    if s.startswith("array:"):
        inner = _pin_type(s[6:])
        return _checked_pin(L.get_array_type(inner), _pin_category(inner), "Array", s)
    if s.startswith("set:"):
        inner = _pin_type(s[4:])
        return _checked_pin(L.get_set_type(inner), _pin_category(inner), "Set", s)
    if s in _BASIC_PIN_TYPES:
        category, sub = _BASIC_PIN_TYPES[s]
        t = unreal.EdGraphPinType()
        t.import_text('(PinCategory="%s",PinSubCategory="%s")' % (category, sub))
        return _checked_pin(t, category, what=s)
    kind, _, ref = s.partition(":")
    if kind in ("object", "class", "struct") and ref:
        target = unreal.load_object(None, ref) if ref.startswith("/") else _resolve_class_v2(ref)
        if target is None:
            raise _V2Error("CLASS_UNRESOLVED", "no %s %s" % (kind, ref))
        if kind == "struct":
            return _checked_pin(L.get_struct_type(target), "struct", what=s)
        if kind == "object":
            return _checked_pin(L.get_object_reference_type(target), "object", what=s)
        return _checked_pin(L.get_class_reference_type(target), "class", what=s)
    raise _V2Error("BAD_VALUE", "type must be one of %s, object:<Class>, class:<Class>, struct:<Struct>, "
                   "array:<type> or set:<type> (got %r)" % (", ".join(_BASIC_PIN_TYPES), spec))


def _pin_category(t):
    m = re.search(r'PinCategory="(\w+)"', t.export_text())
    return m.group(1) if m else ""


def _op_data_add_variable(args):
    """Add a member variable to a Blueprint (graph editing stays a non-goal), compile and
    save. instance_editable / expose_on_spawn as given. Not an undo step (it recompiles)."""
    path, bp = _data_asset(args, unreal.Blueprint)
    name = str(args.get("name") or "")
    if not re.match(r"^[A-Za-z_][A-Za-z0-9_]*$", name):
        raise _V2Error("BAD_VALUE", "name must be an identifier (got %r)" % name)
    L = unreal.BlueprintEditorLibrary
    _need_plugin(6, "add_variable")
    pin = _pin_type(args.get("type"))
    # The engine renames a clash silently (a second Health became Health_0, live R3):
    # refuse a name any variable, component or function already has.
    before = json.loads(_authoring().describe_blueprint_json(bp, False))
    taken = {str(v.get("name")).lower() for v in before.get("variables") or []}
    taken |= {str(c.get("name")).lower() for c in before.get("components") or []}
    taken |= {str(f).lower() for f in (before.get("functions") or []) + (before.get("events") or [])}
    if name.lower() in taken:
        raise _V2Error("CONFLICT", "%s already has a variable, component or function named %s" % (path, name))
    if not L.add_member_variable(bp, unreal.Name(name), pin):
        raise _V2Error("EDITOR_ERROR", "%s could not add %s" % (path, name))
    after = json.loads(_authoring().describe_blueprint_json(bp, False))
    if name not in [v.get("name") for v in after.get("variables") or []]:
        raise _V2Error("EDITOR_ERROR", "%s: the engine did not add a variable named %s" % (path, name),
                       variables=[v.get("name") for v in after.get("variables") or []])
    if args.get("instance_editable") is not None:
        L.set_blueprint_variable_instance_editable(bp, unreal.Name(name), bool(args["instance_editable"]))
    if args.get("expose_on_spawn") is not None:
        L.set_blueprint_variable_expose_on_spawn(bp, unreal.Name(name), bool(args["expose_on_spawn"]))
    L.compile_blueprint(bp)
    errors = []
    if args.get("default") is not None:
        cdo = unreal.get_default_object(bp.generated_class())
        errors = _set_props(cdo, {name: args["default"]})
    unreal.EditorAssetLibrary.save_asset(path)
    added = next((v for v in json.loads(_authoring().describe_blueprint_json(bp, False)).get("variables") or []
                  if v.get("name") == name), {"name": name})
    return {"asset": path, "added": added, "property_errors": errors}


_VALUE_TYPES = {"digital": "BOOLEAN", "bool": "BOOLEAN", "axis1d": "AXIS1D", "axis2d": "AXIS2D", "axis3d": "AXIS3D"}


def _input_asset(path, cls):
    eal = unreal.EditorAssetLibrary
    obj = unreal.load_asset(path) if eal.does_asset_exist(path) else None
    if obj is not None:
        if not isinstance(obj, cls):
            raise _V2Error("BAD_VALUE", "%s is a %s, not a %s" % (path, obj.get_class().get_name(), cls.__name__))
        return obj, False
    folder, name = path.rsplit("/", 1)
    if not eal.does_directory_exist(folder):
        eal.make_directory(folder)
    f = unreal.DataAssetFactory()
    f.set_editor_property("data_asset_class", cls)
    obj = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, folder, cls, f)
    if obj is None:
        raise _V2Error("EDITOR_ERROR", "could not create %s" % path)
    return obj, True


def _op_data_input_mapping(args):
    """Create or edit an Enhanced Input action and its keys in a mapping context: the
    action's keys in that context become exactly `keys` (none: unmapped). Missing assets
    are created. Not an undo step."""
    action_path, context_path = args.get("action") or "", args.get("context") or ""
    if not action_path.startswith("/Game/") or not context_path.startswith("/Game/"):
        raise _V2Error("BAD_VALUE", "action and context are /Game asset paths (e.g. /Game/Input/IA_Dash, /Game/Input/IMC_Default)")
    keys = args.get("keys")
    if not isinstance(keys, list):
        raise _V2Error("BAD_VALUE", "keys must be a list of key names (e.g. [\"LeftShift\", \"Gamepad_FaceButton_Right\"]); [] unmaps")
    parsed = []
    for k in keys:
        key = unreal.Key()
        key.import_text(str(k))
        if str(key.get_editor_property("key_name")) in ("", "None"):
            raise _V2Error("BAD_VALUE", "%r is not a key name" % k)
        parsed.append(key)
    action, made_action = _input_asset(action_path, unreal.InputAction)
    vt = args.get("value_type")
    if vt is not None:
        if vt not in _VALUE_TYPES:
            raise _V2Error("BAD_VALUE", "value_type must be one of %s (got %r)" % (", ".join(_VALUE_TYPES), vt))
        action.set_editor_property("value_type", getattr(unreal.InputActionValueType, _VALUE_TYPES[vt]))
    context, made_context = _input_asset(context_path, unreal.InputMappingContext)
    context.unmap_all_keys_from_action(action)
    for key in parsed:
        context.map_key(action, key)
    unreal.EditorAssetLibrary.save_asset(action_path)
    unreal.EditorAssetLibrary.save_asset(context_path)
    mapped = [str(m.get_editor_property("key").get_editor_property("key_name"))
              for m in context.get_editor_property("default_key_mappings").get_editor_property("mappings")
              if m.get_editor_property("action") == action]
    return {"action": action_path, "context": context_path, "keys": mapped,
            "value_type": str(action.get_editor_property("value_type")).rsplit(".", 1)[-1].split(":")[0],
            "created": [p for p, made in ((action_path, made_action), (context_path, made_context)) if made]}
