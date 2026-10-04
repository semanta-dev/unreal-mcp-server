
_OPS = {
    # v2 core (95_v2_core.py)
    "actor_query": _op_actor_query,
    "actor_spawn": _op_actor_spawn,
    "actor_delete": _op_actor_delete,
    "editor_undo": _op_editor_undo,
    "note_edit": _op_note_edit,
    "observe_paths": _op_observe_paths,
    "game_read": _op_game_read,
    "game_command": _op_game_command,
    "actor_transform": _op_actor_transform,
    "actor_set_properties": _op_actor_set_properties,
    "actor_call": _op_actor_call,
    "asset_create": _op_asset_create,
    "pie_exec": _op_pie_exec,  # playtest/scenario beats until P5d moves them to actor_call
    "pie_preflight": _op_pie_preflight,
    "pie_start": _op_pie_start,
    "packages_state": _op_packages_state,
    "quit_editor": _op_quit_editor,
    "pie_stop": _op_pie_stop,
    "snapshot_actors": _op_snapshot_actors,
    "snapshot_restore": _op_snapshot_restore,
    "scene_actors": _op_scene_actors,
    "scene_prune": _op_scene_prune,
    "asset_edit": _op_asset_edit,
    "reflect": _op_reflect,
    "editor_status": _op_editor_status,
    "open_level": _op_open_level,
    "level_revert": _op_level_revert,
    "save_all": _op_save_all,
    "list_assets": _op_list_assets,
    "import_assets": _op_import_assets,
    "live_coding_compile": _op_live_coding_compile,
    "take_screenshot": _op_take_screenshot_v2,
    "pie_observe": _op_pie_observe_v2,
    "pie_screenshot": _op_pie_screenshot_v2,
    "pie_ui_shot": _op_pie_ui_shot,
    "events_start": _op_events_start,
    "events_stop": _op_events_stop,
    "seed_random": _op_seed_random,
    "apply_level_recipe": _op_apply_level_recipe,
    "asset_info": _op_asset_info,
    "asset_thumbnail": _op_asset_thumbnail,
    "audio_capture_start": _op_audio_capture_start_v2,
    "audio_capture_stop": _op_audio_capture_stop_v2,
    "play_test_sound": _op_play_test_sound_v2,
    "company_status": _pie_only(_op_company_status),
    "widget_render": _op_widget_render_v2,
    "company_road": _pie_only(_op_company_road),
    "company_demolish": _pie_only(_op_company_demolish),
    "company_build": _pie_only(_op_company_build),
    "company_select": _pie_only(_op_company_select),
    "asset_reimport": _op_asset_reimport,
    # v7 additions
    "capture_start": _op_capture_start_v2,
    "capture_poll": _op_capture_poll,
    "capture_stop": _op_capture_stop,
    "capture_list": _op_capture_list,
    "capture_poses": _op_capture_poses_v2,
    "scene_apply": _op_scene_apply,
    "scene_clear": _op_scene_clear,
    "scene_bounds": _op_scene_bounds,
    "design_probe": _op_design_probe,
    "viewport_set": _op_viewport_set,
    "viewport_get": _op_viewport_get,
    "focus_actors": _op_focus_actors,
    "select_actors": _op_select_actors,
    "get_selection": _op_get_selection,
    "console": _op_console,
    # P2 discovery
    "asset_query": _op_asset_query,
    "asset_deps": _op_asset_deps,
    "asset_tags": _op_asset_tags,
    "map_gameplay": _op_map_gameplay,
    # P3 structured authoring
    "datatable_import": _op_datatable_import,
    "widget_compose": _op_widget_compose_v2,
    "widget_compile": _op_widget_compile,
    "widget_tree": _op_widget_tree,
    "widget_describe": _op_widget_describe,
    "set_world_gamemode": _op_set_world_gamemode,
    "pie_set_property": _op_pie_set_property,
    # P4 spatial verification
    "world_query": _op_world_query,
    # PW instanced content
    "instances_count": _op_instances_count,
    "instances_list": _op_instances_list,
    # P5 robustness
    "editor_ping": _op_editor_ping,
    "cockpit_info": _op_cockpit_info,
    # P7 plugin-backed input synthesis
    "pie_input": _op_pie_input,
    "pie_cursor": _op_pie_cursor,
    "pie_ui_click": _op_pie_ui_click,
    "pie_time": _op_pie_time,
    "pie_axis_stats": _op_pie_axis_stats,
    "widget_bind": _op_widget_bind,
    "widget_mount": _op_widget_mount,
    "widget_unmount": _op_widget_unmount,
    "widget_live_tree": _op_widget_live_tree,
    "data_table_read": _op_data_table_read,
    "data_table_upsert": _op_data_table_upsert,
    "data_table_delete": _op_data_table_delete,
    "data_curve_read": _op_data_curve_read,
    "data_curve_keys": _op_data_curve_keys,
    "data_blueprint": _op_data_blueprint,
    "data_set_properties": _op_data_set_properties,
    "data_set_settings": _op_data_set_settings,
    "data_add_variable": _op_data_add_variable,
    "data_input_mapping": _op_data_input_mapping,
}


# Stable error codes an autonomous agent can branch on, instead of regexing a
# raw Python traceback. Only TIMEOUT is retryable at the op layer (the transport
# handles connection loss); everything else is a caller/state fault to fix.
_RETRYABLE_CODES = {"TIMEOUT", "EDITOR_BUSY"}


def _classify_error_message(msg):
    """Classify a failure MESSAGE string (from an in-band op {"error": ...} return
    or an exception) into a stable code. Shared by the exception path and the
    in-band-error promotion so NOT_IN_PIE/CLASS_UNRESOLVED/etc. are actually
    produced by the conditions that name them."""
    low = str(msg).lower()
    if "not in pie" in low or "no game world" in low or "no world" in low:
        return "NOT_IN_PIE"
    if "could not resolve class" in low or ("class" in low and "resolve" in low):
        return "CLASS_UNRESOLVED"
    if "spawn failed" in low or ("spawn" in low and "fail" in low):
        return "SPAWN_FAILED"
    if "asset not found" in low or ("not found" in low and "asset" in low):
        return "ASSET_NOT_FOUND"
    if "target not found" in low or ("not found" in low and "target" in low):
        return "TARGET_NOT_FOUND"
    if ("save" in low and ("fail" in low or "block" in low)):
        return "SAVE_BLOCKED"
    if "read-only" in low or "readonly" in low:
        return "PROPERTY_READONLY"
    if "not found" in low:
        return "NOT_FOUND"
    if "no such session" in low:
        return "NO_SESSION"
    return "EDITOR_ERROR"


def _classify_error(e):
    name = type(e).__name__
    if isinstance(e, KeyError):
        return "MISSING_ARG"
    if isinstance(e, FileNotFoundError):
        return "FILE_NOT_FOUND"
    if name == "AttributeError" or "no attribute" in str(e).lower():
        return "BAD_ATTRIBUTE"
    if name == "TypeError":
        return "BAD_ARGS"
    if name == "ValueError":
        return "BAD_VALUE"
    m = _classify_error_message(e)
    return m if m != "EDITOR_ERROR" else "EDITOR_ERROR"


def _mcp2_dispatch(op, b64args):
    try:
        # A JSON null (Go's nil map) is "no arguments", like an empty object.
        args = (json.loads(base64.b64decode(b64args)) if b64args else None) or {}
        fn = _OPS.get(op)
        if fn is None:
            _emit({"ok": False, "error": "unknown op: " + str(op), "code": "UNKNOWN_OP",
                   "retryable": False, "traceback": ""})
            return
        result = fn(args)
        # Any in-band failure ({"error": "..."} at the top level — the common way ops
        # signal NOT_IN_PIE / CLASS_UNRESOLVED / ASSET_NOT_FOUND, etc.) is a failure,
        # using the op's own "code" when it gives one. (v1 passed an error that carried
        # a code as ok:true.) A partial-result "errors"/"warnings" list is NOT this.
        if isinstance(result, dict) and result.get("error"):
            code = result.get("code") or _classify_error_message(result["error"])
            _emit({"ok": False, "error": str(result["error"]), "code": code,
                   "retryable": code in _RETRYABLE_CODES, "details": result.get("details")})
            return
        _note_op(op, args)
        _emit({"ok": True, "result": result})
    except _V2Error as e:  # a coded failure: no traceback (not a Python bug)
        _emit({"ok": False, "error": str(e), "code": e.code,
               "retryable": e.code in _RETRYABLE_CODES, "details": e.details})
    except Exception as e:
        code = _classify_error(e)
        _emit({"ok": False, "error": str(e), "code": code,
               "retryable": code in _RETRYABLE_CODES, "traceback": traceback.format_exc()})


def _mcp2_dispatch_native(op, b64args, op_id):
    """Native dispatch entry (Phase B1). MCPCore's game-thread Dispatcher calls this via
    ExecPythonCommandEx. It sets the per-dispatch native sink so the op's single _emit
    routes its result to the framed channel keyed by op_id, then always clears it — so a
    later uexec dispatch on the same interpreter is never mis-routed. MCPCore reconciles:
    if this never reaches _emit (an import/binding failure before the op body), no
    emit_result(op_id) fires and the native side synthesizes EDITOR_EXEC_FAILED (§5.1)."""
    global _MCP_NATIVE_SINK
    _MCP_NATIVE_SINK = op_id
    try:
        _mcp2_dispatch(op, b64args)
    finally:
        _MCP_NATIVE_SINK = None
