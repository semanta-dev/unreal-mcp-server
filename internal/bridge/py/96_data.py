# --- R3: data and logic authoring (toolset `data`) ---------------------------------------
# DataTables (typed rows, keyed upsert/delete — never replace-all), float curves (plugin
# API 6: Python cannot read FRichCurve keys), Blueprint describe (plugin API 6) and
# add_variable, property edits on any asset or settings class, Enhanced Input actions and
# mapping contexts.


def _data_asset(args, kind=None):
    path = args.get("asset") or ""
    if not path:
        raise _V2Error("BAD_VALUE", "asset is required")
    obj = unreal.load_asset(path)
    if obj is None:
        raise _V2Error("NOT_FOUND", "no asset %s" % path)
    if kind is not None and not isinstance(obj, kind):
        hint = ("" if kind is not unreal.DataTable else
                " (an asset's properties: read with reflect op=object actor=%s, change with data_edit op=set_properties)" % path)
        raise _V2Error("BAD_VALUE", "%s is a %s, not a %s%s" % (path, obj.get_class().get_name(), kind.__name__, hint))
    return path, obj


def _save(path):
    """Save an asset whether or not the editor thinks it is dirty (some edits — mapping
    contexts, filled tables — do not mark the package) and fail when it does not save."""
    if not unreal.EditorAssetLibrary.save_asset(path, only_if_is_dirty=False):
        raise _V2Error("EDITOR_ERROR", "%s did not save (read-only, or checked out elsewhere?)" % path)


def _norm(name):
    return str(name).lower().replace("_", "")


def _data_table(args):
    path, dt = _data_asset(args, unreal.DataTable)
    composite = getattr(unreal, "CompositeDataTable", None)
    if composite is not None and isinstance(dt, composite):
        raise _V2Error("BAD_VALUE", "%s is a composite DataTable: edit the tables it is made of" % path)
    return path, dt


def _table_rows(dt):
    """The table's rows as {row name: {column: value}} (the engine's own JSON export, so
    the columns are the struct's export names and the values its JSON forms)."""
    raw = unreal.DataTableFunctionLibrary.export_data_table_to_json_string(dt)
    rows = {}
    for r in json.loads(raw or "[]"):
        name = r.pop("Name", None)
        if name is not None:
            rows[str(name)] = r
    return rows


def _columns(dt):
    """{normalized column: export name} — the names the JSON import matches, for a C++
    struct and a Blueprint (user-defined) struct alike."""
    return {_norm(c): str(c) for c in unreal.DataTableFunctionLibrary.get_data_table_column_export_names(dt)}


def _row_name(rows, name):
    """The existing row a name means: row names are FNames, matched without regard to case."""
    for n in rows:
        if n.lower() == str(name).lower():
            return n
    return None


def _op_data_table_read(args):
    path, dt = _data_table(args)
    rows = _table_rows(dt)
    want = args.get("rows")
    if want:
        found = {w: _row_name(rows, w) for w in want}
        missing = [w for w, n in found.items() if n is None]
        if missing:
            raise _V2Error("NOT_FOUND", "%s has no rows %s" % (path, ", ".join(missing)), rows=sorted(rows)[:50])
        rows = {n: rows[n] for n in found.values()}
    limit = max(1, int(args.get("limit") or 200))
    names = list(rows)[:limit]
    st = dt.get_editor_property("row_struct")
    return {"asset": path, "row_struct": st.get_path_name() if st else None, "columns": sorted(_columns(dt).values()),
            "total": len(rows), "truncated": len(rows) > limit, "rows": {n: rows[n] for n in names}}


def _check_row_fields(path, dt, rows):
    """Every field named in rows is a column of the row struct (UE's JSON import ignores
    an unknown one: a typo would silently keep the old value). Returns the rows keyed by
    the columns' export names."""
    columns = _columns(dt)
    if not columns:
        raise _V2Error("BAD_VALUE", "%s: the row struct has no columns" % path)
    out = {}
    for name, row in rows.items():
        if not isinstance(row, dict):
            raise _V2Error("BAD_VALUE", "row %s must be an object of field -> value" % name)
        fixed = {}
        for k, v in row.items():
            col = columns.get(_norm(k))
            if col is None:
                raise _V2Error("BAD_VALUE", "row %s: the row struct has no column %r" % (name, k), columns=sorted(columns.values()))
            fixed[col] = v
        out[str(name)] = fixed
    return out


def _same_json(a, b):
    """Numbers compare as numbers (the export writes 0.8 as 0.80000001192092896)."""
    if isinstance(a, (int, float)) and isinstance(b, (int, float)) and not isinstance(a, bool):
        return abs(float(a) - float(b)) <= 1e-5 * max(1.0, abs(float(b)))
    return a == b


def _op_data_table_upsert(args):
    """Insert or update the named rows; every other row is written back unchanged (the
    engine has no single-row add, so the table is re-filled from its own export with
    only these rows changed). Fields not given keep their values (new rows: the
    struct's defaults). One undo step — none when it fails."""
    path, dt = _data_table(args)
    rows = args.get("rows") or {}
    if not isinstance(rows, dict) or not rows:
        raise _V2Error("BAD_VALUE", "rows must be {row name: {field: value}}")
    rows = _check_row_fields(path, dt, rows)
    current = _table_rows(dt)
    created, updated, targets, changes = [], [], {}, {}
    for name, fields in rows.items():
        existing = _row_name(current, name)
        if existing is None:
            created.append(name)
            existing = name
            current[existing] = {}
        else:
            updated.append(existing)
        changes[existing] = {c: {"from": current[existing].get(c), "to": v} for c, v in fields.items()
                             if not _same_json(current[existing].get(c), v)}
        current[existing].update(fields)
        targets[existing] = fields
    if args.get("dry_run"):
        return {"dry_run": True, "asset": path, "created": created, "updated": updated, "changes": changes,
                "checked": "row names and fields (a value the engine cannot import shows only on the real call)"}
    original = unreal.DataTableFunctionLibrary.export_data_table_to_json_string(dt)
    payload = [dict({"Name": n}, **r) for n, r in current.items()]
    fill = unreal.DataTableFunctionLibrary.fill_data_table_from_json_string
    with _transaction("MCP: upsert rows in " + path.rsplit("/", 1)[-1]) as t:
        dt.modify()
        # The engine empties the table before filling it: any failure restores the
        # original rows, and cancels the transaction (no undo step for a failed edit).
        problem = None if fill(dt, json.dumps(payload)) else "%s rejected the rows (the editor log names the field)" % path
        if problem is None:
            after = _table_rows(dt)
            for name, fields in targets.items():  # read back: the import must have kept every value
                got = after.get(name) or {}
                for col, v in fields.items():
                    if not _same_json(got.get(col), v):
                        problem = "row %s: %s did not take the value %r (read back %r)" % (name, col, v, got.get(col))
        if problem is not None:
            restored = fill(dt, original)
            if t is not None:
                t.cancel()
            raise _V2Error("BAD_VALUE" if restored else "EDITOR_ERROR",
                           problem + ("; the table is unchanged" if restored else
                                      "; RESTORING THE TABLE FAILED — reload it from disk (it was not saved)"))
    _save(path)
    return {"asset": path, "created": created, "updated": updated, "total": len(_table_rows(dt))}


def _op_data_table_delete(args):
    """Delete the named rows — all of them or none (a missing name is NOT_FOUND)."""
    path, dt = _data_table(args)
    names = [str(n) for n in (args.get("rows") or [])]
    if not names:
        raise _V2Error("BAD_VALUE", "rows must name the rows to delete")
    current = _table_rows(dt)
    found = {n: _row_name(current, n) for n in names}
    missing = [n for n, r in found.items() if r is None]
    if missing:
        raise _V2Error("NOT_FOUND", "%s has no rows %s (nothing deleted)" % (path, ", ".join(missing)))
    if args.get("dry_run"):
        return {"dry_run": True, "asset": path, "deleted": list(found.values()), "total": len(current) - len(found)}
    with _transaction("MCP: delete rows from " + path.rsplit("/", 1)[-1]):
        dt.modify()
        for n in found.values():
            unreal.DataTableFunctionLibrary.remove_data_table_row(dt, unreal.Name(n))
    _save(path)
    return {"asset": path, "deleted": list(found.values()), "total": len(_table_rows(dt))}


def _authoring():
    auth = _mcp_authoring()
    if auth is None:
        raise _V2Error("PLUGIN_MISSING", "the UnrealMCP plugin's MCPAuthoring module is not loaded")
    return auth


def _op_data_curve_read(args):
    path, curve = _data_asset(args, unreal.CurveFloat)
    _need_plugin(6, "curve keys")
    return {"asset": path, "keys": json.loads(_authoring().get_curve_keys_json(curve) or "[]")}


def _op_data_curve_keys(args):
    """Replace a float curve's keys (all or nothing: the plugin validates them first).
    One undo step — none when refused."""
    path, curve = _data_asset(args, unreal.CurveFloat)
    _need_plugin(6, "curve keys")
    keys = args.get("keys")
    if not isinstance(keys, list) or not keys:
        raise _V2Error("BAD_VALUE", "keys must be [{time, value, interp?}, ...] or [[time, value], ...]")
    keys = [{"time": k[0], "value": k[1]} if isinstance(k, (list, tuple)) and len(k) == 2 else k for k in keys]
    with _transaction("MCP: set keys of " + path.rsplit("/", 1)[-1]) as t:
        why = _authoring().set_curve_keys_json(curve, json.dumps(keys))
        if why:
            if t is not None:
                t.cancel()  # validated before anything changed: no undo step
            raise _V2Error("BAD_VALUE", "%s: %s" % (path, why))
    _save(path)
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


def _known_props(obj, props):
    """[{property, error}] for the names obj does not have (checked before any change)."""
    unknown = []
    for k in props:
        try:
            obj.get_editor_property(k)
        except Exception as e:
            unknown.append({"property": k, "error": str(e)})
    return unknown


def _read_back(obj, props):
    values = {}
    for k in props:
        try:
            values[k] = _coerce_prop(obj.get_editor_property(k), 512)
        except Exception:
            pass
    return values


def _op_data_set_properties(args):
    """Set properties on any asset (DataAsset, PrimaryDataAsset, curve, sound class…),
    per-property errors; one undo step; saved."""
    path, obj = _data_asset(args)
    if isinstance(obj, unreal.Blueprint):
        raise _V2Error("BAD_VALUE", "%s is a Blueprint: set its class defaults with asset_edit op=set_defaults" % path)
    props = args.get("properties") or {}
    if not isinstance(props, dict) or not props:
        raise _V2Error("BAD_VALUE", "properties must be {property: value}")
    unknown = _known_props(obj, props)
    if len(unknown) == len(props):  # before any transaction: nothing would change
        raise _V2Error("BAD_VALUE", "%s has none of these properties" % path, property_errors=unknown)
    if args.get("dry_run"):
        known = {k: v for k, v in props.items() if k not in {u["property"] for u in unknown}}
        now = _read_back(obj, known)
        return {"dry_run": True, "asset": path,
                "changes": {k: {"from": now.get(k), "to": v} for k, v in known.items() if not _same_json(now.get(k), v)},
                "property_errors": unknown, "checked": "property names (a value of the wrong type shows only on the real call)"}
    with _transaction("MCP: set properties on " + path.rsplit("/", 1)[-1]):
        obj.modify()
        errors = _set_props(obj, {k: v for k, v in props.items() if k not in {u["property"] for u in unknown}}) + unknown
    _save(path)
    return {"asset": path, "values": _read_back(obj, props), "property_errors": errors}


def _op_data_set_settings(args):
    """Set a settings class's defaults (a UDeveloperSettings / config class, e.g.
    /Script/EngineSettings.GeneralProjectSettings) and write them to its Default*.ini -
    through the plugin (API 6): Python sees many settings classes not at all, and cannot
    write config. Not an undo step: the file is written."""
    _need_plugin(6, "settings")
    cls = _resolve_class_v2(args.get("class") or "")
    props = args.get("properties") or {}
    if not isinstance(props, dict) or not props:
        raise _V2Error("BAD_VALUE", "properties must be {property: value}")
    res = json.loads(_authoring().set_config_defaults_json(cls, json.dumps(props)))
    errors = res.get("errors") or []
    if not res.get("ok"):
        code = "EDITOR_ERROR" if res.get("values") else "BAD_VALUE"
        raise _V2Error(code, "%s: %s" % (cls.get_name(), res.get("error") or "no property was set"), property_errors=errors)
    return {"class": cls.get_path_name(), "values": res.get("values") or {}, "property_errors": errors}


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


def _pin_category(t):
    m = re.search(r'PinCategory="(\w+)"', t.export_text())
    return m.group(1) if m else ""


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


def _op_data_add_variable(args):
    """Add a member variable to a Blueprint (graph editing stays a non-goal), compile and
    save. instance_editable / expose_on_spawn as given. Not an undo step (it recompiles)."""
    path, bp = _data_asset(args, unreal.Blueprint)
    name = str(args.get("name") or "")
    if not re.match(r"^[A-Za-z_][A-Za-z0-9_]*$", name):
        raise _V2Error("BAD_VALUE", "name must be an identifier (got %r)" % name)
    _need_plugin(6, "add_variable")
    if _pie_running():
        raise _V2Error("PRECONDITION", "adding a variable recompiles the Blueprint, which would change the running game: stop PIE first")
    L = unreal.BlueprintEditorLibrary
    pin = _pin_type(args.get("type"))
    auth = _authoring()
    # Kismet's own validator sees inherited members too: UE would rename a clash
    # silently (a second Health became Health_0, live R3; an Actor's Tags -> Tags_0).
    taken = auth.check_member_name(bp, unreal.Name(name))
    if taken:
        raise _V2Error("CONFLICT", "%s: %s" % (path, taken))
    if args.get("dry_run"):
        return {"dry_run": True, "asset": path, "would_add": {"name": name, "type": pin.export_text()},
                "checked": "name and type (a default value is checked only on the real call)"}
    before = {v.get("name") for v in json.loads(auth.describe_blueprint_json(bp, False)).get("variables") or []}
    if not L.add_member_variable(bp, unreal.Name(name), pin):
        raise _V2Error("EDITOR_ERROR", "%s could not add %s" % (path, name))
    after = [v.get("name") for v in json.loads(auth.describe_blueprint_json(bp, False)).get("variables") or []]
    if name not in after:
        stray = [v for v in after if v not in before]  # only what this add created (never a look-alike)
        for v in stray:  # undo the half-applied add before reporting it
            auth.remove_member_variable(bp, unreal.Name(v))
        raise _V2Error("EDITOR_ERROR", "%s: the engine did not add a variable named %s (removed %s)" % (path, name, stray or "nothing"))
    if args.get("instance_editable") is not None:
        L.set_blueprint_variable_instance_editable(bp, unreal.Name(name), bool(args["instance_editable"]))
    if args.get("expose_on_spawn") is not None:
        L.set_blueprint_variable_expose_on_spawn(bp, unreal.Name(name), bool(args["expose_on_spawn"]))
    L.compile_blueprint(bp)
    errors = []
    if args.get("default") is not None:
        cdo = unreal.get_default_object(bp.generated_class())
        errors = _set_props(cdo, {name: args["default"]})
    _save(path)
    added = next((v for v in json.loads(auth.describe_blueprint_json(bp, False)).get("variables") or []
                  if v.get("name") == name), {"name": name})
    return {"asset": path, "added": added, "property_errors": errors}


_VALUE_TYPES = {"digital": "BOOLEAN", "bool": "BOOLEAN", "axis1d": "AXIS1D", "axis2d": "AXIS2D", "axis3d": "AXIS3D"}


def _registry_class(path):
    """The class (path, name) of the asset at a package path, from the asset registry
    (the asset is not loaded), or None when there is none."""
    name = path.rsplit("/", 1)[-1]
    for ad in unreal.AssetRegistryHelpers.get_asset_registry().get_assets_by_package_name(path) or []:
        if str(ad.get_editor_property("asset_name")) == name:
            cp = ad.get_editor_property("asset_class_path")
            return "%s.%s" % (cp.get_editor_property("package_name"), cp.get_editor_property("asset_name")), \
                str(cp.get_editor_property("asset_name"))
    return None


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


def _op_data_input_mapping_read(args):
    """An InputMappingContext's actions and the keys mapped to each, in mapping order."""
    path, context = _data_asset(args, unreal.InputMappingContext)
    order, keys = [], {}
    for m in context.get_editor_property("default_key_mappings").get_editor_property("mappings"):
        action = m.get_editor_property("action")
        name = action.get_path_name().split(".", 1)[0] if action else "(none)"
        if name not in keys:
            order.append(name)
            keys[name] = []
        keys[name].append(str(m.get_editor_property("key").get_editor_property("key_name")))
    return {"context": path, "mappings": [{"action": a, "keys": keys[a]} for a in order]}


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
        # The engine takes any name as a key (FKey::ImportTextItem never checks it): a typo
        # would map a key that never fires.
        if not unreal.InputLibrary.key_is_valid(key):
            raise _V2Error("BAD_VALUE", "%r is not a key name (e.g. LeftShift, SpaceBar, E, Gamepad_FaceButton_Bottom)" % k)
        parsed.append(key)
    vt = args.get("value_type")
    if vt is not None and vt not in _VALUE_TYPES:
        raise _V2Error("BAD_VALUE", "value_type must be one of %s (got %r)" % (", ".join(_VALUE_TYPES), vt))
    if args.get("dry_run"):
        creates = []
        for p, want, want_name in ((action_path, unreal.InputAction, "InputAction"),
                                   (context_path, unreal.InputMappingContext, "InputMappingContext")):
            have = _registry_class(p)
            if have is None:
                creates.append(p)
                continue
            cls = unreal.load_class(None, have[0])  # the class, not the asset
            if cls is None or not unreal.MathLibrary.class_is_child_of(cls, want):  # what the real call refuses
                raise _V2Error("BAD_VALUE", "%s is a %s, not a %s" % (p, have[1], want_name))
        return {"dry_run": True, "action": action_path, "context": context_path, "keys": [str(k) for k in keys],
                "creates": creates}
    action, made_action = _input_asset(action_path, unreal.InputAction)
    context, made_context = _input_asset(context_path, unreal.InputMappingContext)
    # MapKey / UnmapAllKeysFromAction do not mark the package: mark it, so it saves.
    action.modify()
    context.modify()
    if vt is not None:
        action.set_editor_property("value_type", getattr(unreal.InputActionValueType, _VALUE_TYPES[vt]))
    context.unmap_all_keys_from_action(action)
    for key in parsed:
        context.map_key(action, key)
    _save(action_path)
    _save(context_path)
    mapped = [str(m.get_editor_property("key").get_editor_property("key_name"))
              for m in context.get_editor_property("default_key_mappings").get_editor_property("mappings")
              if m.get_editor_property("action") == action]
    return {"action": action_path, "context": context_path, "keys": mapped,
            "value_type": str(action.get_editor_property("value_type")).rsplit(".", 1)[-1].split(":")[0],
            "created": [p for p, made in ((action_path, made_action), (context_path, made_context)) if made]}
