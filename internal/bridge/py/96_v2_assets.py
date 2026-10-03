# ===========================================================================
# v2 assets / reflection / widgets (docs/plans/OVERHAUL_PLAN.md §2.3 rows 9-13,
# 26, 41): asset_create refuses to overwrite unless asked (the *_create hazard in
# P3a_PYOPS_SWEEP.md), class arguments go through the v2 resolver (short names,
# CONFLICT on ambiguity), reflect resolves actors like actor_query, and widget
# renders land in a server-owned folder.
# ===========================================================================

_PKG_PATH = re.compile(r"^/[A-Za-z0-9_]+(/[A-Za-z0-9_\-]+)+$")


def _v2_dest(args):
    dest = args.get("dest") or ""
    if not _PKG_PATH.match(dest):
        raise _V2Error("BAD_VALUE", "dest must be a package path like /Game/Folder/Name (got %r)" % (dest,))
    return dest


def _class_path(ref):
    """The v2 resolver's class, as a path the v1 creation helpers accept."""
    return _resolve_class_v2(ref).get_path_name()


# Each kind's preparer resolves and validates every input and returns the creation
# step; nothing is touched (in particular, op=replace deletes nothing) until all of
# them succeeded.

def _prep_blueprint(args, dest):
    parent = _class_path(args.get("class"))
    return lambda: _op_blueprint_create({"parent_class_path": parent, "dest": dest})


def _prep_data_asset(args, dest):
    cls = _class_path(args.get("class"))
    return lambda: _op_dataasset_create({"class": cls, "dest": dest})


def _prep_data_table(args, dest):
    rs = args.get("row_struct") or ""
    struct = unreal.load_object(None, rs) if rs.startswith("/Script/") else unreal.load_asset(rs) if rs else None
    if not struct:
        raise _V2Error("NOT_FOUND", "kind=data_table needs a loadable row_struct (got %r)" % (rs,))
    return lambda: _op_datatable_create({"row_struct": rs, "dest": dest})


def _prep_material_instance(args, dest):
    parent = args.get("parent")
    if not parent or not unreal.EditorAssetLibrary.does_asset_exist(parent):
        raise _V2Error("NOT_FOUND", "kind=material_instance requires an existing parent material (got %r)" % (parent,))
    params = args.get("params") or {}
    for name, tex in (params.get("texture") or {}).items():
        if not unreal.EditorAssetLibrary.does_asset_exist(tex):
            raise _V2Error("NOT_FOUND", "texture parameter %s: no asset %s" % (name, tex))

    def make():
        out = _op_create_material_instance({"parent": parent, "dest": dest, "params": params})
        if not unreal.EditorAssetLibrary.does_asset_exist(dest):
            raise _V2Error("SPAWN_FAILED", "material instance %s was not created" % dest)
        return out
    return make


def _prep_widget_blueprint(args, dest):
    a = {"dest": dest, "root_panel": args.get("root_panel") or "CanvasPanel"}
    if args.get("class"):
        a["parent_class"] = _class_path(args["class"])
    return lambda: _op_widget_create(a)


_ASSET_KINDS = {
    "blueprint": _prep_blueprint,
    "data_asset": _prep_data_asset,
    "data_table": _prep_data_table,
    "material_instance": _prep_material_instance,
    "widget_blueprint": _prep_widget_blueprint,
}


def _op_asset_create(args):
    """Create an asset of `kind` at `dest`. An existing asset is a CONFLICT unless
    replace=true (the replace op), which deletes it — only after every input has
    been validated — and never via UE's interactive overwrite prompt."""
    kind = args.get("kind")
    prep = _ASSET_KINDS.get(kind)
    if prep is None:
        raise _V2Error("BAD_VALUE", "kind must be one of %s (got %r)" % (", ".join(sorted(_ASSET_KINDS)), kind))
    dest = _v2_dest(args)
    eal = unreal.EditorAssetLibrary
    exists = eal.does_asset_exist(dest)
    if exists and not args.get("replace"):
        raise _V2Error("CONFLICT", "%s already exists (op=replace overwrites it)" % dest, asset=dest)
    make = prep(args, dest)  # raises before anything is deleted
    replaced = False
    if exists:
        if not eal.delete_asset(dest):
            raise _V2Error("EDITOR_ERROR", "could not delete the existing %s (referenced or checked out?)" % dest)
        replaced = True
    out = make()
    if isinstance(out, dict) and "error" in out:
        if replaced:
            out = dict(out, details={"deleted": dest})
        return out
    out = dict(out or {})
    out.update({"asset": dest, "kind": kind, "replaced": replaced})
    return out


def _op_asset_edit(args):
    """Blueprint CDO edits: set_defaults | add_component | assign_subclass."""
    op = args.get("op")
    asset = args.get("asset")
    if op == "set_defaults":
        return _op_blueprint_set_defaults({"blueprint": asset, "defaults": args.get("properties") or {}})
    if op == "add_component":
        a = {"blueprint": asset, "component_class": _class_path(args.get("class"))}
        if args.get("name"):
            a["name"] = args["name"]
        return _op_blueprint_add_component(a)
    if op == "assign_subclass":
        return _op_assign_subclass({"target": asset, "prop": args.get("property"),
                                    "class_path": _class_path(args.get("class"))})
    raise _V2Error("BAD_VALUE", "op must be set_defaults, add_component or assign_subclass (got %r)" % (op,))


def _op_reflect(args):
    """Reflect an actor in a world (op=object), a class contract (op=class) or an
    enum (op=enum)."""
    op = args.get("op")
    shape = {k: args[k] for k in ("include", "exclude", "properties") if args.get(k)}
    if op == "object":
        world, name = _v2_world(args, "editor")
        obj = _resolve_actor(world, name, args.get("actor"))
        out = _reflect_observe(obj, max_props=int(args.get("max_props", 64)),
                               max_str=int(args.get("max_str", 512)), **shape)
        out["world"] = name
        return out
    if op == "class":
        a = dict(shape, class_path=_class_path(args.get("class")))
        if args.get("max_props"):
            a["max_props"] = args["max_props"]
        return _op_reflect_class(a)
    if op == "enum":
        return _op_enum_values({"enum_path": args.get("enum") or ""})
    raise _V2Error("BAD_VALUE", "op must be object, class or enum (got %r)" % (op,))


def _op_widget_render_v2(args):
    """Render a UserWidget class offscreen into Saved/MCP/WidgetRenders (a
    server-owned folder; the caller no longer chooses the output path)."""
    wc = args.get("widget_class") or ""
    if not wc:
        raise _V2Error("BAD_VALUE", "widget_class is required")
    out_dir = _saved_mcp_dir("WidgetRenders")
    stem = re.sub(r"[^A-Za-z0-9_]+", "_", wc.strip("/")) or "widget"
    stem += "_%d" % int(time.time() * 1000)  # render -> edit -> render keeps the "before" image
    # CaptureWidget LoadClass()es a class path: accept a WidgetBlueprint asset path, a
    # short name or a _C path alike.
    cls = _resolve_class_v2(wc)
    if not unreal.MathLibrary.class_is_child_of(cls, unreal.UserWidget):
        raise _V2Error("BAD_VALUE", "%s is not a UserWidget class" % cls.get_path_name())
    wc = cls.get_path_name()
    out = _op_widget_render({"widget_class": wc, "out_path": out_dir + "/" + stem + ".png",
                             "width": int(args.get("width", 1280)), "height": int(args.get("height", 720))})
    if "error" in out:
        raise _V2Error("PLUGIN_MISSING", out["error"])
    if not out.get("ok"):
        raise _V2Error("EDITOR_ERROR", "render produced no image (is %s a UserWidget class?)" % wc)
    return out


def _op_widget_compose_v2(args):
    """widget_compose, except that an additive compose may not swap out a root that
    already has authored children (the old tree would be orphaned): that is a
    CONFLICT unless prune (the destructive op) is set."""
    if not args.get("prune"):
        wbp = unreal.load_asset(args.get("blueprint") or "")
        spec_root = (args.get("tree") or {}).get("name")
        if isinstance(wbp, unreal.WidgetBlueprint) and spec_root:
            cur = _widget_canon(wbp.get_editor_property("widget_tree"))
            if cur.get("children") and cur.get("name") != spec_root:
                raise _V2Error("CONFLICT", "the tree's root %r differs from the existing root %r, which has children; "
                               "use op=prune to replace the whole tree" % (spec_root, cur.get("name")),
                               existing_root=cur.get("name"))
    return _op_widget_compose(args)
