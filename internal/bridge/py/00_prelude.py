# mcp_bridge.py — companion module hot-loaded into the Unreal Editor's __main__
# namespace by the Go MCP server. The server invokes _mcp_dispatch(op, b64args)
# with base64-encoded JSON args; each op returns a JSON-serializable value that
# is emitted back via the __MCP_JSON__ marker. Keeping all editor-side logic here
# (not hand-built in Go) is the DRY + injection-safe design (GO_REWRITE_PLAN.md §8).
#
# Editor-scripting gotchas preserved verbatim from the Python server:
#   - unreal.Rotator(a, b, c) is (roll, pitch, yaw), NOT (pitch, yaw, roll).
#   - rotation_pyr args are [pitch, yaw, roll] and map to Rotator(roll, pitch, yaw).
#   - take_screenshot uses SceneCapture2D (renders even when backgrounded; editor
#     world only, NOT during PIE).
#
# Text-style ops return a "message" field carrying the exact string the Python
# server produced, so the A/B parity harness can assert text equality.

_MCP_BRIDGE_VERSION = 37

import unreal
import json
import base64
import traceback
import os
import io
import math
import time
import fnmatch
import contextlib

_MARKER = "__MCP_JSON__"


def _jsonable(v):
    """Coerce a value to something JSON-serializable. Unreal enums (UENUM) are
    returned as their enumerator NAME (a string) so string predicates can match;
    other non-primitives fall back to str(). Also usable as json.dumps(default=)."""
    if v is None or isinstance(v, (bool, int, float, str)):
        return v
    name = getattr(v, "name", None)
    if name is not None:
        return name
    return str(v)


# --- prelude helpers -------------------------------------------------------

def _find_actor(label):
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    for a in sub.get_all_level_actors():
        if a and a.get_actor_label() == label:
            return a
    return None


def _actor_ref(a):
    loc = a.get_actor_location()
    return {
        "label": a.get_actor_label(),
        "class": a.get_class().get_name(),
        "location": [round(loc.x, 1), round(loc.y, 1), round(loc.z, 1)],
    }


# --- native cockpit sink (Phase B1, EDITOR_PLUGIN_PLAN.md §5.1) -------------
# When MCPCore drives a dispatch it sets _MCP_NATIVE_SINK to the current op_id so _emit
# routes the result to the native framed channel instead of the stdout marker. This is
# strictly PER-DISPATCH (set/cleared around one op by _mcp_dispatch_native), deliberately
# DECOUPLED from the cockpit_info presence probe, so a session that fell back to uexec can
# never mis-route a result into the native ring and hang.
_MCP_NATIVE_SINK = None


def _mcp_cockpit_bridge():
    """The UMCPCockpitBridge editor subsystem, or None if MCPCore is not loaded."""
    try:
        return unreal.get_editor_subsystem(unreal.MCPCockpitBridge)
    except Exception:
        return None


def _emit(payload):
    # default=_jsonable guarantees an op's result can never poison the marker
    # (a single non-serializable field would otherwise fail the whole dispatch).
    if _MCP_NATIVE_SINK is not None:
        b = _mcp_cockpit_bridge()
        if b is not None:
            try:
                b.emit_result(_MCP_NATIVE_SINK, json.dumps(payload, default=_jsonable))
                return
            except Exception:
                pass  # fall through to the marker (belt-and-suspenders per §5.1)
    print(_MARKER + json.dumps(payload, default=_jsonable))


def _issue(code, label, message):
    """One normalized problem record so agents branch on `code` uniformly across
    ops (scene_apply/apply_level_recipe emit these alongside their legacy arrays).
    Also mirrored to the event stream so a blocked agent can see failures live."""
    rec = {"code": code, "label": label, "message": message}
    try:
        _emit_event("issue", rec)
    except Exception:
        pass
    return rec


