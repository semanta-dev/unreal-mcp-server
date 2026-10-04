

# ---------------------------------------------------------------------------
# HUD / UMG authoring — Phase 0a (flat primitive authoring, Python-first).
# Composite (WBP-in-WBP), structured FCompilerResultsLog compile, BindWidget
# enumeration, and FWidgetRenderer capture are Phase 0b/1 (the MCPAuthoring C++
# Editor module). See HUD_TOOLING_PLAN.md. The reflected recipe here (new_object
# for primitives + universal add_child + bare-UPROPERTY setters) is the one the
# plan's Spike 0 validates first; any op a UE point-release stops reflecting
# moves to the C++ subsystem without changing this contract.
# ---------------------------------------------------------------------------

# Friendly widget-class names -> the unreal.* type. Primitive UWidget subclasses
# only; a /Game WBP or UserWidget subclass is a COMPOSITE (Phase 0b, C++).
_WIDGET_CLASSES = {
    "CanvasPanel": "CanvasPanel", "Overlay": "Overlay", "VerticalBox": "VerticalBox",
    "HorizontalBox": "HorizontalBox", "ScrollBox": "ScrollBox", "GridPanel": "GridPanel",
    "UniformGridPanel": "UniformGridPanel", "SizeBox": "SizeBox", "Border": "Border",
    "ScaleBox": "ScaleBox", "WidgetSwitcher": "WidgetSwitcher", "SafeZone": "SafeZone",
    "TextBlock": "TextBlock", "RichTextBlock": "RichTextBlock", "Image": "Image",
    "Button": "Button", "ProgressBar": "ProgressBar", "Slider": "Slider",
    "CheckBox": "CheckBox", "EditableText": "EditableText", "EditableTextBox": "EditableTextBox",
    "Spacer": "Spacer", "NamedSlot": "NamedSlot",
}
# Anchor presets -> (min[x,y], max[x,y], alignment[x,y]) — the single highest-leverage
# layout-correctness lever (coherent anchors+alignment, per the plan §3.b).
_ANCHOR_PRESETS = {
    "TopLeft": ((0, 0), (0, 0), (0, 0)), "TopCenter": ((0.5, 0), (0.5, 0), (0.5, 0)),
    "TopRight": ((1, 0), (1, 0), (1, 0)), "CenterLeft": ((0, 0.5), (0, 0.5), (0, 0.5)),
    "Center": ((0.5, 0.5), (0.5, 0.5), (0.5, 0.5)), "CenterRight": ((1, 0.5), (1, 0.5), (1, 0.5)),
    "BottomLeft": ((0, 1), (0, 1), (0, 1)), "BottomCenter": ((0.5, 1), (0.5, 1), (0.5, 1)),
    "BottomRight": ((1, 1), (1, 1), (1, 1)), "Fill": ((0, 0), (1, 1), (0, 0)),
}


def _widget_prim_class(name):
    """Resolve a widget class name to (unreal_class, is_composite). Friendly names are
    the primitives; anything that loads as a WidgetBlueprint (a /Game WBP) or resolves
    to a WidgetBlueprintGeneratedClass is a COMPOSITE (needs the C++ ConstructWidget
    path). is_child_of is NOT a reflected UFUNCTION in 5.7, so detection is by asset
    type + generated-class name, never by is_child_of."""
    friendly = _WIDGET_CLASSES.get(name)
    if friendly is not None:
        return getattr(unreal, friendly, None), False
    # A /Game path: composite iff the asset is a WidgetBlueprint.
    asset = None
    try:
        asset = unreal.load_asset(name)
    except Exception:
        asset = None
    if isinstance(asset, unreal.WidgetBlueprint):
        return asset.generated_class(), True
    cls = _resolve_class(name)
    if cls is None:
        return None, False
    # A WidgetBlueprintGeneratedClass (name ends _C) or a class whose name flags a
    # UserWidget base is a composite; a bare /Script UWidget subclass is primitive.
    cname = ""
    try:
        cname = str(cls.get_name())
    except Exception:
        cname = ""
    is_composite = cname.endswith("_C") or "UserWidget" in cname
    return cls, is_composite


_SLOT_KEYS = frozenset(("anchor_preset", "anchors", "alignment", "offsets", "z", "size_to_content", "size", "padding",
                        "h_align", "v_align", "row", "col", "row_span", "col_span"))


def _widget_apply_slot(child, slot_body):
    """Apply a slot-class-aware layout body to a child's (already-added) UPanelSlot.
    Must run AFTER add_child (which mints a fresh slot). Returns [issue,...]."""
    issues = []
    slot = child.slot
    if slot is None or not slot_body:
        return issues
    for k in slot_body:
        if k not in _SLOT_KEYS:  # never ignore a layout key silently (the widget would sit at 0,0)
            issues.append(_issue("SLOT_KEY_UNKNOWN", k, "slot keys are " + ", ".join(sorted(_SLOT_KEYS))))
    tn = type(slot).__name__
    try:
        if isinstance(slot, unreal.CanvasPanelSlot):
            preset = slot_body.get("anchor_preset")
            if preset and preset in _ANCHOR_PRESETS:
                mn, mx, al = _ANCHOR_PRESETS[preset]
                slot.set_anchors(unreal.Anchors(unreal.Vector2D(*mn), unreal.Vector2D(*mx)))
                slot.set_alignment(unreal.Vector2D(*al))
            elif slot_body.get("anchors"):
                a = slot_body["anchors"]
                slot.set_anchors(unreal.Anchors(unreal.Vector2D(*a[0]), unreal.Vector2D(*a[1])))
            if slot_body.get("alignment") and not preset:
                slot.set_alignment(unreal.Vector2D(*slot_body["alignment"]))
            off = slot_body.get("offsets")
            if off:
                slot.set_offsets(unreal.Margin(off[0], off[1], off[2], off[3]))
            if "z" in slot_body:
                slot.set_z_order(int(slot_body["z"]))
            if slot_body.get("size_to_content"):
                slot.set_auto_size(True)
        elif isinstance(slot, (unreal.HorizontalBoxSlot, unreal.VerticalBoxSlot)):
            sz = slot_body.get("size")
            if sz:
                rule = unreal.SlateSizeRule.FILL if sz.get("fill") or sz.get("value") else unreal.SlateSizeRule.AUTOMATIC
                slot.set_size(unreal.SlateChildSize(value=float(sz.get("value", 1.0)), size_rule=rule))
            _widget_slot_common(slot, slot_body)
        elif isinstance(slot, (unreal.OverlaySlot, unreal.BorderSlot)):
            _widget_slot_common(slot, slot_body)
        elif isinstance(slot, (unreal.GridSlot, unreal.UniformGridSlot)):
            if "row" in slot_body:
                slot.set_row(int(slot_body["row"]))
            if "col" in slot_body:
                slot.set_column(int(slot_body["col"]))
            # Spans exist only on GridSlot; UniformGridSlot cells are single.
            if isinstance(slot, unreal.GridSlot):
                if "row_span" in slot_body:
                    slot.set_row_span(int(slot_body["row_span"]))
                if "col_span" in slot_body:
                    slot.set_column_span(int(slot_body["col_span"]))
        else:
            issues.append(_issue("SLOT_TYPE_UNHANDLED", tn, "no slot recipe for " + tn))
    except Exception as e:
        issues.append(_issue("SLOT_SET_FAILED", tn, str(e)))
    return issues


def _widget_slot_common(slot, body):
    if body.get("padding"):
        p = body["padding"]
        slot.set_padding(unreal.Margin(p[0], p[1], p[2], p[3]) if isinstance(p, list) else unreal.Margin(p, p, p, p))
    if body.get("h_align"):
        slot.set_horizontal_alignment(getattr(unreal.HorizontalAlignment, "H_ALIGN_" + body["h_align"].upper(), unreal.HorizontalAlignment.H_ALIGN_FILL))
    if body.get("v_align"):
        slot.set_vertical_alignment(getattr(unreal.VerticalAlignment, "V_ALIGN_" + body["v_align"].upper(), unreal.VerticalAlignment.V_ALIGN_FILL))


def _widget_apply_props(widget, props):
    """Reflection-set leaf props (Text as FText, colors, sizes, visibility). Partial
    failure: accumulate per-prop issues rather than aborting."""
    issues = []
    for k, v in (props or {}).items():
        try:
            if k == "Text" and isinstance(v, str):
                widget.set_editor_property("text", unreal.Text(v))  # 5.7 has no Text.from_string
            elif k == "Visibility" and isinstance(v, str):
                widget.set_editor_property("visibility", getattr(unreal.SlateVisibility, v.upper(), unreal.SlateVisibility.VISIBLE))
            else:
                widget.set_editor_property(_snake(k), _maybe_asset(v))
        except Exception as e:
            issues.append(_issue("PROPERTY_SET_FAILED", k, str(e)))
    return issues


def _snake(name):
    """CamelCase -> snake_case for set_editor_property (UMG props are exposed snake)."""
    out = []
    for i, ch in enumerate(name):
        if ch.isupper() and i > 0 and not name[i - 1].isupper():
            out.append("_")
        out.append(ch.lower())
    return "".join(out)


# --- widget tree access -----------------------------------------------------------
# UE 5.7 hides UWidgetBlueprint.WidgetTree and UWidgetTree.RootWidget from Python
# (get_editor_property raises "Failed to find property"): the plugin (API 4) hands the
# tree and its root over. Earlier engines expose them directly; both are tried, and a
# 5.7 editor without the plugin fails with PLUGIN_MISSING rather than half-building.

def _wtree(wbp):
    try:
        return wbp.get_editor_property("widget_tree")
    except Exception:
        pass
    auth = _mcp_authoring()
    if auth is None or not hasattr(auth, "get_widget_tree"):
        raise _V2Error("PLUGIN_MISSING", "authoring a WidgetBlueprint in UE 5.7 needs the UnrealMCP plugin API 4 (the "
                       "widget tree is hidden from Python) — copy plugin/UnrealMCP into <project>/Plugins and rebuild "
                       "(build strategy=ubt)", needed=4, have=_plugin_api())
    return auth.get_widget_tree(wbp)


def _wroot(wt):
    try:
        return wt.get_editor_property("root_widget")
    except Exception:
        return _mcp_authoring().get_root_widget(wt.get_outer())


def _wregister(wt, widget):
    """Give a Python-created widget its variable GUID (5.7's compiler ensures without
    one); older engines without the plugin call need nothing."""
    auth = _mcp_authoring()
    if auth is not None and hasattr(auth, "register_widget"):
        auth.register_widget(wt.get_outer(), widget)


def _wset_root(wt, widget):
    try:
        wt.set_editor_property("root_widget", widget)
        return
    except Exception:
        pass
    if not _mcp_authoring().set_root_widget(wt.get_outer(), widget):
        raise _V2Error("EDITOR_ERROR", "could not make %s the root widget" % widget.get_name())


def _op_widget_create(args):
    """Create a WidgetBlueprint shell with a chosen parent class + root panel."""
    pkg_path, name = args["dest"].rsplit("/", 1)
    factory = unreal.WidgetBlueprintFactory()
    parent_path = args.get("parent_class") or "/Script/UMG.UserWidget"
    parent_cls = _resolve_class(parent_path)
    if parent_cls is not None:
        try:
            factory.set_editor_property("parent_class", parent_cls)
        except Exception:
            pass
    wbp = unreal.AssetToolsHelpers.get_asset_tools().create_asset(name, pkg_path, unreal.WidgetBlueprint, factory)
    if not wbp:
        return {"error": "widget create failed", "code": "SPAWN_FAILED"}
    root_panel = args.get("root_panel") or "CanvasPanel"
    wt = _wtree(wbp)
    root_cls, _ = _widget_prim_class(root_panel)
    root_name = "RootPanel"
    if root_cls is not None and _wroot(wt) is None:
        root = unreal.new_object(root_cls, outer=wt, name=root_name)
        _wregister(wt, root)
        _wset_root(wt, root)
    unreal.BlueprintEditorLibrary.compile_blueprint(wbp)
    unreal.EditorAssetLibrary.save_asset(args["dest"])
    return {"created": args["dest"], "root": root_name, "parent_class": parent_path,
            "required_bindwidgets": []}  # BindWidget enumeration is Phase 0b (C++)


def _op_widget_compose(args):
    """Phase 0a flat reconcile: build the spec tree of PRIMITIVE nodes, apply
    slots/props (after all adds), interim-compile, and echo the read-back tree +
    digest. Composite (UserWidget) children => COMPOSITE_NEEDS_PLUGIN (Phase 0b)."""
    bp_path = args["blueprint"]
    wbp = unreal.load_asset(bp_path)
    if not isinstance(wbp, unreal.WidgetBlueprint):
        return {"error": "not a WidgetBlueprint: " + str(bp_path), "code": "ASSET_NOT_FOUND"}
    wt = _wtree(wbp)
    spec = args.get("tree") or {}
    issues = []
    restore_token = None
    destructive = bool(args.get("prune") or args.get("remove"))
    if destructive:
        restore_token = _widget_snapshot(bp_path, wt)

    # Index existing nodes by name (UWidgetTree.find_widget is NOT reflected in 5.7,
    # so walk root_widget explicitly). This drives adopt/patch + idempotence.
    index = {}
    cur_root = _wroot(wt)
    if cur_root is not None:
        _widget_index(cur_root, index)

    # PASS 1 — build/adopt every node by name (structure first: adds, no slot yet).
    built = {}
    root = _widget_reconcile_node(wt, None, spec, index, built, issues)
    # Repoint the root whenever the reconciled spec root differs from the current one
    # (widget_create leaves a RootPanel; a spec with its own root must be adopted).
    if root is not None and _wroot(wt) != root:
        _wset_root(wt, root)

    # PASS 2 — apply slot + props to every node (after all adds mint their slots).
    _widget_apply_all(spec, built, issues)

    removed = []
    # A spec root that replaced the old root (widget_create's RootPanel) detaches the old
    # tree: every old widget not in the new tree is gone and must lose its variable GUID,
    # or 5.7's compiler ensures ("was deleted but still has a GUID") on every compile.
    if cur_root is not None and root is not None and cur_root != root:
        live = {}
        _widget_index(root, live)
        auth = _mcp_authoring()
        for nm in index:
            if nm not in live:
                if auth is not None and hasattr(auth, "unregister_widget"):
                    auth.unregister_widget(wbp, unreal.Name(nm))
                removed.append(nm)

    # remove / prune (destructive; snapshotted above).
    to_remove = list(args.get("remove") or [])
    if args.get("prune"):
        spec_names = set()
        _widget_spec_names(spec, spec_names)
        for nm in index:
            if nm not in spec_names and nm not in built:
                to_remove.append(nm)
    for nm in to_remove:
        w = built.get(nm) or index.get(nm)
        if w is not None and w != root:
            try:
                w.remove_from_parent()
                auth = _mcp_authoring()
                if auth is not None and hasattr(auth, "unregister_widget"):
                    auth.unregister_widget(wbp, unreal.Name(nm))
                removed.append(nm)
            except Exception as e:
                issues.append(_issue("REMOVE_FAILED", nm, str(e)))

    if not args.get("defer"):
        unreal.BlueprintEditorLibrary.compile_blueprint(wbp)
        unreal.EditorAssetLibrary.save_asset(bp_path)
    tree = _widget_canon(wt)
    out = {"blueprint": bp_path, "tree": tree, "digest": _widget_digest(tree),
           "removed": removed, "mode": args.get("mode") or "full",
           "compile_log": {"compiled": not args.get("defer"), "structured": False},  # structured log is 0b
           "issues": issues}
    if restore_token and (removed or destructive):
        out["restore_token"] = restore_token
    return out


def _widget_index(w, out):
    """Recursively map node-name -> UWidget over the live tree (get_children_count/
    get_child_at + get_content are reflected; find_widget is not)."""
    out[str(w.get_name())] = w
    if isinstance(w, unreal.PanelWidget):
        for i in range(w.get_children_count()):
            _widget_index(w.get_child_at(i), out)
    elif isinstance(w, unreal.ContentWidget):
        c = w.get_content()
        if c is not None:
            _widget_index(c, out)


def _widget_spec_names(node, out):
    if node.get("name"):
        out.add(node["name"])
    for c in node.get("children") or []:
        _widget_spec_names(c, out)


def _widget_reconcile_node(wt, parent, node, index, built, issues):
    """Construct or adopt a node by name (from the pre-built index) and add it under
    parent. Returns the UWidget (recurses children). A composite (UserWidget/WBP) child
    needs the MCPAuthoring C++ AddChildWidget path — call it if present, else issue."""
    name = node.get("name")
    cls_name = node.get("class")
    if not name or not cls_name:
        issues.append(_issue("NODE_INVALID", str(name), "every node needs name+class"))
        return None
    cls, is_composite = _widget_prim_class(cls_name)
    if cls is None:
        issues.append(_issue("CLASS_UNRESOLVED", cls_name, "unknown widget class"))
        return None
    existing = index.get(name)
    if existing is not None:
        widget = existing
    elif is_composite:
        # Composite construction is the C++ ConstructWidget path (Phase 0b).
        auth = _mcp_authoring()
        parent_name = str(parent.get_name()) if parent is not None else ""
        added = auth is not None and parent is not None and auth.add_child_widget(
            wt.get_outer(), unreal.Name(parent_name), cls, unreal.Name(name), bool(node.get("is_variable")))
        if not added:
            issues.append(_issue("COMPOSITE_NEEDS_PLUGIN", name, "composite child needs the MCPAuthoring C++ module (Phase 0b) loaded + compiled"))
            return None
        widget = _widget_index_find(wt, name)
        built[name] = widget
        return widget  # composite subtree is opaque; not expanded
    else:
        widget = unreal.new_object(cls, outer=wt, name=unreal.Name(name))
        _wregister(wt, widget)
    # Repoint the root the moment the top-level node exists — BEFORE recursing — so a
    # composite child (C++ AddChildWidget resolves its parent via root-anchored
    # FindWidget) can reach a parent that lives in the new subtree.
    if parent is None and _wroot(wt) != widget:
        _wset_root(wt, widget)
    if node.get("is_variable"):
        try:
            widget.set_editor_property("is_variable", True)
        except Exception as e:
            auth = _mcp_authoring()  # 5.7 hides bIsVariable from Python (plugin API 4)
            if auth is None or not hasattr(auth, "set_widget_is_variable") or not auth.set_widget_is_variable(widget, True):
                issues.append(_issue("IS_VARIABLE_NOT_MATERIALIZED", name, str(e)))
    built[name] = widget
    if parent is not None and existing is None:
        try:
            parent.add_child(widget)
        except Exception as e:
            issues.append(_issue("ADD_CHILD_FAILED", name, str(e)))
    for child in node.get("children") or []:
        _widget_reconcile_node(wt, widget, child, index, built, issues)
    return widget


def _mcp_authoring():
    """The MCPAuthoring editor subsystem, or None if the module isn't compiled/loaded."""
    try:
        return unreal.get_editor_subsystem(unreal.MCPAuthoringSubsystem)
    except Exception:
        return None


def _widget_index_find(wt, name):
    idx = {}
    root = _wroot(wt)
    if root is not None:
        _widget_index(root, idx)
    return idx.get(name)


def _widget_apply_all(node, built, issues):
    w = built.get(node.get("name"))
    if w is not None:
        issues.extend(_widget_apply_props(w, node.get("props")))
        if node.get("slot"):
            issues.extend(_widget_apply_slot(w, node["slot"]))
    for child in node.get("children") or []:
        _widget_apply_all(child, built, issues)


def _widget_canon(wt):
    """Canonical in-memory tree JSON (child order preserved) — the layer-1 oracle."""
    root = _wroot(wt)
    return _widget_canon_node(root) if root is not None else {}


def _widget_canon_node(w):
    d = {"name": str(w.get_name()), "class": type(w).__name__}
    children = []
    if isinstance(w, unreal.PanelWidget):
        for i in range(w.get_children_count()):
            children.append(_widget_canon_node(w.get_child_at(i)))
    elif isinstance(w, unreal.ContentWidget):
        c = w.get_content()
        if c is not None:
            children.append(_widget_canon_node(c))
    if children:
        d["children"] = children
    return d


def _widget_digest(canon):
    import hashlib
    return hashlib.sha256(json.dumps(canon, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()[:16]


def _widget_snapshot(bp_path, wt):
    """Dump the tree to Saved/MCP/widget_snapshots and return a restore token."""
    import os
    proj = unreal.Paths.project_saved_dir()
    d = os.path.join(proj, "MCP", "widget_snapshots", bp_path.replace("/", "_"))
    os.makedirs(d, exist_ok=True)
    token = str(int(time.time() * 1000))
    with open(os.path.join(d, token + ".json"), "w") as f:
        json.dump(_widget_canon(wt), f)
    return token


def _op_widget_compile(args):
    """Phase 0a interim compile (unstructured pass/fail). The structured
    FCompilerResultsLog log is Phase 0b (C++ widget_compile)."""
    bp_path = args["blueprint"]
    wbp = unreal.load_asset(bp_path)
    if not isinstance(wbp, unreal.WidgetBlueprint):
        return {"error": "not a WidgetBlueprint", "code": "ASSET_NOT_FOUND"}
    unreal.BlueprintEditorLibrary.compile_blueprint(wbp)
    unreal.EditorAssetLibrary.save_asset(bp_path)
    tree = _widget_canon(_wtree(wbp))
    return {"compiled": True, "digest": _widget_digest(tree),
            "compile_log": {"structured": False, "note": "structured log is Phase 0b"}}


def _op_widget_tree(args):
    bp_path = args["blueprint"]
    wbp = unreal.load_asset(bp_path)
    if not isinstance(wbp, unreal.WidgetBlueprint):
        return {"error": "not a WidgetBlueprint", "code": "ASSET_NOT_FOUND"}
    mode = args.get("mode") or "get"
    if mode == "restore":
        return {"error": "restore is not yet implemented in Phase 0a", "code": "NOT_IMPLEMENTED",
                "restore_token": args.get("restore_token")}
    tree = _widget_canon(_wtree(wbp))
    return {"blueprint": bp_path, "tree": tree, "digest": _widget_digest(tree)}


def _op_widget_describe(args):
    """Phase 0a: the authorable palette (friendly names) or a class's reflected
    props via reflect_class. BindWidget/handler enumeration is Phase 0b (C++)."""
    wc = args.get("widget_class")
    if not wc:
        return {"palette": sorted(_WIDGET_CLASSES.keys()),
                "anchor_presets": sorted(_ANCHOR_PRESETS.keys())}
    return _op_reflect_class({"class_path": wc})


def _op_set_world_gamemode(args):
    cls = _resolve_class_v2(args["class_path"])
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    ws = next((a for a in sub.get_all_level_actors() if isinstance(a, unreal.WorldSettings)), None)
    if not ws:
        return {"error": "no WorldSettings actor in the level", "code": "NOT_FOUND"}
    try:
        ws.set_editor_property("default_game_mode", cls)
    except Exception as e:
        return {"error": "set default_game_mode: " + str(e), "code": "PROPERTY_READONLY"}
    unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
    return {"world_game_mode": args["class_path"]}


def _op_pie_set_property(args):
    """Set a property on a LIVE actor in the game world (test preconditions)."""
    world = _game_world()
    if not world:
        return {"error": "not in PIE", "code": "NOT_IN_PIE"}
    label = args["target"]
    for a in unreal.GameplayStatics.get_all_actors_of_class(world, unreal.Actor):
        if a.get_actor_label() == label:
            errors = []
            for k, v in (args.get("properties") or {}).items():
                try:
                    a.set_editor_property(k, _maybe_asset(v))
                except Exception as e:
                    errors.append(_issue("PROPERTY_SET_FAILED", k, str(e)))
            return {"target": label, "errors": errors}
    return {"error": "target not found: " + str(label), "code": "TARGET_NOT_FOUND"}


