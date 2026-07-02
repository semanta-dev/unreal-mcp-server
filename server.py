"""MCP server exposing a live Unreal Editor session as tools.

Run by the MCP client (see .mcp.json at the project root). Requires the
Unreal Editor to be open with this project for most tools to work.
"""
import json
import os
import time
from pathlib import Path

from mcp.server.fastmcp import FastMCP, Image

import unreal_bridge as bridge

mcp = FastMCP("unreal")


def _embed(value) -> str:
    """Safely embed a python value as a literal in generated snippets."""
    return "None" if value is None else json.dumps(value)


FIND_ACTOR_SNIPPET = """
import unreal, json
_sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
def _find_actor(label):
    for _a in _sub.get_all_level_actors():
        if _a and _a.get_actor_label() == label:
            return _a
    return None
"""


@mcp.tool()
def editor_status() -> dict:
    """Check whether the Unreal Editor is reachable and report engine version, project, and the currently open level."""
    snippet = """
import unreal, json
world = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem).get_editor_world()
payload = {
    "engine_version": unreal.SystemLibrary.get_engine_version(),
    "project_dir": unreal.SystemLibrary.get_project_directory(),
    "current_level": world.get_name() if world else None,
    "is_in_pie": unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).is_in_play_in_editor(),
}
print("__MCP_JSON__" + json.dumps(payload))
"""
    return bridge.run_json(snippet)


@mcp.tool()
def execute_python(code: str, evaluate: bool = False) -> str:
    """Execute arbitrary Python in the Unreal Editor (full `unreal` module access).

    Set evaluate=True to evaluate a single expression and get its value back;
    otherwise the code runs as a script and captured log output is returned.
    """
    if evaluate:
        return str(bridge.eval_statement(code))
    return bridge.format_output(bridge.run_python(code))


@mcp.tool()
def execute_console_command(command: str) -> str:
    """Run an Unreal console command in the editor (e.g. 'stat fps', 'r.ScreenPercentage 50', 'LiveCoding.Compile')."""
    snippet = f"""
import unreal
unreal.SystemLibrary.execute_console_command(None, {_embed(command)})
print("Executed console command: " + {_embed(command)})
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def open_level(level_path: str) -> str:
    """Open a level in the editor by asset path, e.g. '/Game/Maps/L_Arena'. Prompts nothing: unsaved changes in the current level are saved first."""
    snippet = f"""
import unreal
unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
ok = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).load_level({_embed(level_path)})
print("Loaded" if ok else "FAILED to load", {_embed(level_path)})
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def list_actors(name_filter: str = "") -> list:
    """List actors in the current level (label, class, location). Optional case-insensitive filter matching label or class name."""
    snippet = f"""
import unreal, json
_filter = {_embed(name_filter)}.lower()
sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
out = []
for a in sub.get_all_level_actors():
    if not a:
        continue
    label = a.get_actor_label()
    cls = a.get_class().get_name()
    if _filter and _filter not in label.lower() and _filter not in cls.lower():
        continue
    loc = a.get_actor_location()
    out.append({{"label": label, "class": cls, "location": [round(loc.x, 1), round(loc.y, 1), round(loc.z, 1)]}})
print("__MCP_JSON__" + json.dumps(out))
"""
    return bridge.run_json(snippet)


@mcp.tool()
def get_actor(actor_label: str) -> dict:
    """Get details for one actor by its editor label: transform, class, components."""
    snippet = FIND_ACTOR_SNIPPET + f"""
a = _find_actor({_embed(actor_label)})
if not a:
    payload = {{"error": "No actor labeled " + {_embed(actor_label)}}}
else:
    loc, rot, scale = a.get_actor_location(), a.get_actor_rotation(), a.get_actor_scale3d()
    payload = {{
        "label": a.get_actor_label(),
        "class": a.get_class().get_name(),
        "path": a.get_path_name(),
        "location": [loc.x, loc.y, loc.z],
        "rotation_pyr": [rot.pitch, rot.yaw, rot.roll],
        "scale": [scale.x, scale.y, scale.z],
        "components": [{{"name": c.get_name(), "class": c.get_class().get_name()}} for c in a.get_components_by_class(unreal.ActorComponent)],
    }}
print("__MCP_JSON__" + json.dumps(payload))
"""
    return bridge.run_json(snippet)


@mcp.tool()
def spawn_actor(class_path: str, x: float = 0, y: float = 0, z: float = 100,
                pitch: float = 0, yaw: float = 0, roll: float = 0,
                label: str = "", static_mesh_path: str = "") -> dict:
    """Spawn an actor in the current level.

    class_path: native class ('/Script/Engine.PointLight', '/Script/AesirWaveDefense.EnemySpawnPoint')
    or a Blueprint asset path ('/Game/BP_Thing'). For a simple mesh prop, pass
    class_path='/Script/Engine.StaticMeshActor' plus static_mesh_path (e.g. '/Engine/BasicShapes/Cube').
    """
    snippet = f"""
import unreal, json
cls_path = {_embed(class_path)}
if cls_path.startswith("/Script/"):
    cls = unreal.load_class(None, cls_path)
else:
    bp = unreal.load_asset(cls_path)
    cls = bp.generated_class() if isinstance(bp, unreal.Blueprint) else (bp if isinstance(bp, unreal.Class) else None)
if not cls:
    print("__MCP_JSON__" + json.dumps({{"error": "Could not resolve class: " + cls_path}}))
else:
    sub = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
    actor = sub.spawn_actor_from_class(cls, unreal.Vector({x}, {y}, {z}), unreal.Rotator({roll}, {pitch}, {yaw}))
    if not actor:
        print("__MCP_JSON__" + json.dumps({{"error": "Spawn failed"}}))
    else:
        mesh_path = {_embed(static_mesh_path)}
        if mesh_path:
            mesh = unreal.load_asset(mesh_path)
            comp = actor.get_component_by_class(unreal.StaticMeshComponent)
            if mesh and comp:
                comp.set_static_mesh(mesh)
        if {_embed(label)}:
            actor.set_actor_label({_embed(label)})
        loc = actor.get_actor_location()
        print("__MCP_JSON__" + json.dumps({{"label": actor.get_actor_label(), "class": actor.get_class().get_name(), "location": [loc.x, loc.y, loc.z]}}))
"""
    return bridge.run_json(snippet)


@mcp.tool()
def delete_actor(actor_label: str) -> str:
    """Delete an actor from the current level by its editor label."""
    snippet = FIND_ACTOR_SNIPPET + f"""
a = _find_actor({_embed(actor_label)})
if not a:
    print("No actor labeled " + {_embed(actor_label)})
else:
    _sub.destroy_actor(a)
    print("Deleted " + {_embed(actor_label)})
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def set_actor_transform(actor_label: str, location: list[float] | None = None,
                        rotation_pyr: list[float] | None = None,
                        scale: list[float] | None = None) -> str:
    """Move/rotate/scale an actor by label. Each argument is an optional [x,y,z] (rotation as [pitch,yaw,roll])."""
    snippet = FIND_ACTOR_SNIPPET + f"""
a = _find_actor({_embed(actor_label)})
if not a:
    print("No actor labeled " + {_embed(actor_label)})
else:
    loc, rot, scale = {_embed(location)}, {_embed(rotation_pyr)}, {_embed(scale)}
    if loc:
        a.set_actor_location(unreal.Vector(*loc), False, False)
    if rot:
        a.set_actor_rotation(unreal.Rotator(rot[2], rot[0], rot[1]), False)
    if scale:
        a.set_actor_scale3d(unreal.Vector(*scale))
    print("Updated transform of " + {_embed(actor_label)})
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def list_assets(path: str = "/Game", recursive: bool = True, limit: int = 200) -> dict:
    """List content browser assets under a path (e.g. '/Game', '/Game/Maps')."""
    snippet = f"""
import unreal, json
assets = unreal.EditorAssetLibrary.list_assets({_embed(path)}, recursive={recursive}, include_folder=False)
payload = {{"total": len(assets), "assets": sorted(assets)[:{int(limit)}]}}
print("__MCP_JSON__" + json.dumps(payload))
"""
    return bridge.run_json(snippet)


@mcp.tool()
def import_assets(file_paths: list[str], destination_path: str = "/Game/Imported") -> dict:
    """Import external files (FBX meshes, textures, audio, ...) from disk into the
    project's content browser at destination_path (e.g. '/Game/Characters/Annihilator').

    FBX imports auto-detect static vs skeletal meshes. Returns the imported asset paths.
    """
    snippet = f"""
import unreal, json
tasks = []
for path in {_embed(file_paths)}:
    task = unreal.AssetImportTask()
    task.set_editor_property("filename", path)
    task.set_editor_property("destination_path", {_embed(destination_path)})
    task.set_editor_property("automated", True)
    task.set_editor_property("save", True)
    task.set_editor_property("replace_existing", True)
    tasks.append(task)
unreal.AssetToolsHelpers.get_asset_tools().import_asset_tasks(tasks)
imported = []
failed = []
for task in tasks:
    paths = list(task.get_editor_property("imported_object_paths") or [])
    if paths:
        imported.extend(paths)
    else:
        failed.append(str(task.get_editor_property("filename")))
print("__MCP_JSON__" + json.dumps({{"imported": imported, "failed": failed}}))
"""
    return bridge.run_json(snippet)


@mcp.tool()
def save_all() -> str:
    """Save all dirty packages (levels and assets)."""
    snippet = """
import unreal
ok = unreal.EditorLoadingAndSavingUtils.save_dirty_packages(True, True)
print("Saved all dirty packages" if ok else "Save reported failures")
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def take_screenshot(width: int = 1280, height: int = 720,
                    camera_location: list[float] | None = None,
                    camera_rotation_pyr: list[float] | None = None) -> Image:
    """Render the scene and return it as an image. Useful for visually checking level changes.

    Uses a synchronous scene capture, so it works even when the editor window
    is in the background. Camera defaults to the editor viewport camera; pass
    camera_location [x,y,z] and camera_rotation_pyr [pitch,yaw,roll] to frame a
    specific shot. If no camera is available at all, auto-frames the level bounds.
    """
    payload = bridge.run_json(f"""
import unreal, json
ues = unreal.get_editor_subsystem(unreal.UnrealEditorSubsystem)
world = ues.get_editor_world()
explicit_loc = {_embed(camera_location)}
explicit_rot = {_embed(camera_rotation_pyr)}
if explicit_loc:
    loc = unreal.Vector(*explicit_loc)
    rot = unreal.Rotator(explicit_rot[2], explicit_rot[0], explicit_rot[1]) if explicit_rot else unreal.MathLibrary.find_look_at_rotation(loc, unreal.Vector(0, 0, 0))
else:
    cam = ues.get_level_viewport_camera_info()
    loc, rot = cam if cam else (unreal.Vector(0, 0, 0), unreal.Rotator(0, 0, 0))
    if loc.length() < 1.0:
        # Viewport camera unset (e.g. editor started in background): frame the level bounds.
        radius = 1000.0
        for a in unreal.get_editor_subsystem(unreal.EditorActorSubsystem).get_all_level_actors():
            if isinstance(a, unreal.StaticMeshActor):
                o, e = a.get_actor_bounds(False)
                radius = max(radius, o.length() * 0.7)
        loc = unreal.Vector(-radius, -radius, radius * 0.9)
        rot = unreal.MathLibrary.find_look_at_rotation(loc, unreal.Vector(0, 0, 0))
rt = unreal.RenderingLibrary.create_render_target2d(world, {int(width)}, {int(height)}, unreal.TextureRenderTargetFormat.RTF_RGBA8, unreal.LinearColor(0, 0, 0, 1), False)
actors = unreal.get_editor_subsystem(unreal.EditorActorSubsystem)
cap = actors.spawn_actor_from_class(unreal.SceneCapture2D, loc, rot)
cc = cap.capture_component2d
cc.set_editor_property("texture_target", rt)
cc.set_editor_property("capture_source", unreal.SceneCaptureSource.SCS_FINAL_COLOR_LDR)
cc.set_editor_property("always_persist_rendering_state", True)
cc.capture_scene()
out_dir = unreal.SystemLibrary.get_project_directory() + "Saved/Screenshots/"
unreal.RenderingLibrary.export_render_target(world, rt, out_dir, "mcp_screenshot.png")
actors.destroy_actor(cap)
print("__MCP_JSON__" + json.dumps({{"file": out_dir + "mcp_screenshot.png"}}))
""")
    if "error" in payload:
        raise RuntimeError(payload["error"])

    target = Path(payload["file"])
    deadline = time.time() + 10.0
    while time.time() < deadline:
        if target.exists() and target.stat().st_size > 0:
            data = target.read_bytes()
            os.remove(target)
            return Image(data=data, format="png")
        time.sleep(0.2)
    raise RuntimeError(f"Screenshot export did not produce {target}")


@mcp.tool()
def start_play(simulate: bool = False) -> str:
    """Start a play-in-editor session in the current level.

    simulate=False (default) is real PIE with a player pawn; simulate=True
    runs the world without possessing a player.
    """
    snippet = f"""
import unreal
les = unreal.get_editor_subsystem(unreal.LevelEditorSubsystem)
if {"True" if simulate else "False"}:
    les.editor_play_simulate()
else:
    les.editor_request_begin_play()
print("Play session starting")
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def stop_play() -> str:
    """Stop the current play-in-editor session."""
    snippet = """
import unreal
unreal.get_editor_subsystem(unreal.LevelEditorSubsystem).editor_request_end_play()
print("Play session ending")
"""
    return bridge.format_output(bridge.run_python(snippet))


@mcp.tool()
def live_coding_compile() -> str:
    """Trigger a Live Coding compile so C++ changes hot-reload into the running editor. Check the editor's Live Coding window for results."""
    snippet = """
import unreal
unreal.SystemLibrary.execute_console_command(None, "LiveCoding.Compile")
print("Live Coding compile triggered (see editor log/Live Coding console for status)")
"""
    return bridge.format_output(bridge.run_python(snippet))


if __name__ == "__main__":
    mcp.run()
