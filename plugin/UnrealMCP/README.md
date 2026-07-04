# UnrealMCP plugin — game-world capture helper

A tiny companion plugin the `unreal-mcp-server` dispatches into. Its one job today
is the capability the Python bridge **cannot** reach: filming the **live game
world** (possessed PIE / standalone) with a `SceneCapture2D` spawned *into that
world*, so `capture_start`/`capture_stop` can produce the real gameplay readability
filmstrip **even in a backgrounded/headless editor**.

## Why it's needed

- `unreal.EditorActorSubsystem.spawn_actor_from_class(SceneCapture2D, …)` spawns
  only into the **editor** world → `NoneType.capture_component2d` during PIE.
- `HighResShot` (the `pie_highres` source) needs the game viewport to **flush a
  frame**, which a backgrounded/headless editor never does → 0 PNGs.
- A `USceneCaptureComponent2D` living in the **game** world renders regardless of
  viewport focus. Only C++ can spawn into that world — hence this plugin.

## Install

1. Copy (or symlink) this `UnrealMCP/` folder into your project's `Plugins/`
   directory: `<YourProject>/Plugins/UnrealMCP/`.
2. Regenerate project files (right-click the `.uproject` → *Generate … files*, or
   `GenerateProjectFiles`).
3. Build. Via the MCP server: run `build_compile` (a new module → full rebuild;
   the classifier handles it). The `MCPCapture` module is `Runtime` so it loads in
   PIE.
4. The plugin is auto-enabled by the descriptor. Confirm from the server with
   `execute_python` → `hasattr(unreal, 'MCPCaptureSubsystem')`.

## Use

Through the existing capture tools — `game_scene` is just a third `source`:

```jsonc
capture_start { "source": "game_scene", "cell_width": 480, "cell_height": 270,
                "interval_s": 0.25, "camera_mode": "player" }   // player POV (live view)
// … let the sim run (optionally playtest_capture drives beats + slomo) …
capture_stop  { "session": "<from start>", "cols": 8, "draw_labels": true }
```

`camera_mode`: `player` (follow the local player camera — the live gameplay view;
default) · `fixed` (`camera_location`/`camera_rotation_pyr`/`camera_fov`) · `actor`
(`camera_actor` label). Frames are exported as real PNGs (extensioned) into
`Saved/MCP/capture/<session>/` with a `manifest.json`, and flow through the server's
montage/timeline pipeline exactly like the other sources. `read_capture`/
`capture_clear` work on them too.

Note: `game_scene` frames carry images + timing but no per-frame reflected game
state (that comes from the Python slate recorder / `pie_observe`); pair with
`pie_observe`/`pie_verify` if you need synchronized state.

## Module layout

```
UnrealMCP.uplugin
Source/MCPCapture/
  MCPCapture.Build.cs
  Public/MCPCaptureModule.h, MCPCaptureSubsystem.h
  Private/MCPCaptureModule.cpp, MCPCaptureSubsystem.cpp   (UGameInstanceSubsystem)
```

The bridge resolves the subsystem via `GameplayStatics.get_game_instance(world)
.get_subsystem(unreal.MCPCaptureSubsystem)` and calls its `StartCapture`/
`StopCapture`/`PollCapture` UFUNCTIONs. If the plugin isn't compiled in, `game_scene`
returns a `PLUGIN_MISSING` coded error and the other capture sources still work.
