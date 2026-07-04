# The "Perfect HUD" — built with the new tools

A complete, real game HUD expressed as the exact MCP tool sequence, per
`HUD_TOOLING_PLAN.md`. Every step maps to a shipped tool (`internal/tools/hud_tools.go`
+ `mcp_bridge.py` ops + the `MCPAuthoring` module). This is the acceptance artifact:
authoring can't be self-run headlessly here, so this proves the tool surface *covers*
the whole build — and where it didn't, a tool was added (see "Gaps found" at the end).

The HUD: a reusable **composite health bar** (WBP-in-WBP), an **ammo counter**, a
**render-target minimap**, a world-tracked **objective marker**, and a
**controller-navigable pause menu**. All 5 canonical elements.

---

## 1. WBP_HealthBar — reusable composite (its own gameplay binding travels with it)

```jsonc
// create (opt-in UMCPHUDWidget base so it can bind; bake out before shipping)
widget_create { "dest": "/Game/UI/WBP_HealthBar", "parent_class": "/Script/MCPCapture.MCPHUDWidget", "root_panel": "Overlay" }

// compose the tree
widget_compose {
  "blueprint": "/Game/UI/WBP_HealthBar",
  "tree": {
    "name": "Root", "class": "Overlay", "children": [
      { "name": "Fill",  "class": "ProgressBar", "is_variable": true,
        "slot": { "h_align": "Fill", "v_align": "Fill" },
        "props": { "Percent": 1.0, "FillColorAndOpacity": [0.85, 0.2, 0.2, 1] } },
      { "name": "Label", "class": "TextBlock", "is_variable": true,
        "slot": { "h_align": "Center", "v_align": "Center" },
        "props": { "Text": "100" } }
    ]
  }
}

// bind — the flagship separate-property ratio (previously the broken case)
widget_bind_field { "blueprint": "/Game/UI/WBP_HealthBar", "target_widget": "Fill",  "target_field": "Percent",
                    "source": "owning_pawn", "path": "Health", "max_path": "MaxHealth", "conversion": "ratio" }
widget_bind_field { "blueprint": "/Game/UI/WBP_HealthBar", "target_widget": "Label", "target_field": "Text",
                    "source": "owning_pawn", "path": "Health", "conversion": "int_to_text" }
```

Because `WBP_HealthBar` is itself a `UMCPHUDWidget`, its `NativeTick` pull resolves
`GetOwningPlayerPawn()` — so the binding works **wherever the widget is placed**,
including embedded inside `WBP_HUD`. This is why the composite's opaque internals
don't block binding (the parent never needs to reach `Fill.Percent`).

---

## 2. WBP_HUD — the main HUD, embeds the composite

```jsonc
widget_create { "dest": "/Game/UI/WBP_HUD", "parent_class": "/Script/MCPCapture.MCPHUDWidget", "root_panel": "CanvasPanel" }

widget_compose {
  "blueprint": "/Game/UI/WBP_HUD",
  "tree": {
    "name": "Root", "class": "CanvasPanel", "children": [
      // the composite health bar — ONE opaque node (C++ ConstructWidget path)
      { "name": "HealthBar", "class": "/Game/UI/WBP_HealthBar",
        "slot": { "anchor_preset": "BottomLeft", "offsets": [40, -80, 260, 32], "alignment": [0, 1] } },
      { "name": "Ammo", "class": "TextBlock", "is_variable": true,
        "slot": { "anchor_preset": "BottomRight", "offsets": [-220, -80, 180, 40], "alignment": [1, 1] },
        "props": { "Text": "24 / 30" } },
      { "name": "Minimap", "class": "Image", "is_variable": true,
        "slot": { "anchor_preset": "TopRight", "offsets": [-276, 20, 256, 256], "alignment": [1, 0] } },
      { "name": "ObjMarker", "class": "Image", "is_variable": true,
        "slot": { "anchor_preset": "TopLeft", "offsets": [0, 0, 48, 48], "alignment": [0.5, 0.5] } }
    ]
  }
}

// ammo: formatted "24 / 30"
widget_bind_field { "blueprint": "/Game/UI/WBP_HUD", "target_widget": "Ammo", "target_field": "Text",
                    "source": "owning_pawn", "path": "Ammo", "max_path": "MaxAmmo",
                    "conversion": "format_text", "format": "{value} / {max}" }

// objective marker: DPI-correct world tracking (forced point-anchor + centered pivot)
widget_track_actor { "blueprint": "/Game/UI/WBP_HUD", "marker_widget": "ObjMarker", "target": "ObjectiveBeacon" }
```

`widget_tree_get` reads `HealthBar` back as one node whose `class` is
`WBP_HealthBar_C` — the composite is opaque, exactly as designed.

---

## 3. Minimap render-target material (closes the minimap to full)

```jsonc
// (RT + SceneCapture2D authored earlier via existing tools: dataasset/spawn_actor + pie_set_property)
widget_make_rt_material { "dest": "/Game/UI/M_Minimap_UI", "render_target": "/Game/UI/RT_Minimap",
                          "param_name": "RT", "image_widget": "Minimap" }
```

Builds an `MD_UI` material sampling `RT_Minimap`, a dynamic instance, and applies it to
the `Minimap` image brush at show-time.

---

## 4. WBP_PauseMenu — controller-navigable

```jsonc
widget_create { "dest": "/Game/UI/WBP_PauseMenu", "parent_class": "/Script/MCPCapture.MCPHUDWidget", "root_panel": "Overlay" }

widget_compose {
  "blueprint": "/Game/UI/WBP_PauseMenu",
  "tree": {
    "name": "Root", "class": "Overlay", "children": [
      { "name": "Menu", "class": "VerticalBox",
        "slot": { "h_align": "Center", "v_align": "Center" }, "children": [
          { "name": "ResumeBtn", "class": "/Script/MCPCapture.MCPButton", "is_variable": true,
            "slot": { "padding": [8, 8, 8, 8] } },
          { "name": "QuitBtn",   "class": "/Script/MCPCapture.MCPButton", "is_variable": true,
            "slot": { "padding": [8, 8, 8, 8] } }
      ] }
    ]
  }
}

widget_bind_event { "blueprint": "/Game/UI/WBP_PauseMenu", "widget": "ResumeBtn", "command": "Resume" }
widget_bind_event { "blueprint": "/Game/UI/WBP_PauseMenu", "widget": "QuitBtn",   "command": "Quit" }
```

---

## 5. Verify — the closed observe→iterate loop

```jsonc
// LAYER 1+3 (author-time, no PIE): multi-resolution geometry oracle
widget_capture { "blueprint": "/Game/UI/WBP_HealthBar", "resolutions": [[1280,720],[1920,1080],[3840,2160]], "geometry": true }
//   -> assert Fill/Label stay pinned + inside the SafeZone at each res (composite child verified standalone, per fix #12)

// LAYER 2+4 (in-PIE): drive gameplay, read the bound value, prove Slate actually moved
widget_view       { "mode": "show", "blueprint": "/Game/UI/WBP_HUD", "z": 0 }         // -> {handle}
pie_set_source    { "target": "PlayerPawn", "source_kind": "property", "path": "Health", "value": 75 }
widget_read       { "handle": "<h>", "widget_name": "Fill", "field": "Percent" }       // -> ~0.75  (value oracle)
widget_capture    { "blueprint": "/Game/UI/WBP_HUD", "resolutions": [[1920,1080]], "pixels": true }
//   -> image_compare a bar-rect pixel delta across the health change (stale-Slate guard: proves SynchronizeProperties ran)

// menu: BOTH input paths
set_input_mode    { "mode": "UIOnly", "show_cursor": true }
ui_click          { "handle": "<menu>", "widget_name": "ResumeBtn" }                    // mouse
widget_set_focus  { "handle": "<menu>", "widget_name": "ResumeBtn" }                    // + pie_input Down/Accept = gamepad
```

---

## Coverage — every step maps to a shipped tool

| HUD element | Tools exercised | Status |
|---|---|---|
| Composite health bar (WBP-in-WBP) | widget_create, widget_compose (composite dispatch), widget_bind_field (ratio) | ✅ |
| Ammo counter "24 / 30" | widget_compose, widget_bind_field (format_text) | ✅ |
| Render-target minimap | widget_make_rt_material | ✅ |
| Objective marker (world-tracked) | widget_track_actor (DPI-correct) | ✅ |
| Pause menu (mouse + controller) | widget_compose (UMCPButton), widget_bind_event, ui_click, set_input_mode, widget_set_focus | ✅ |
| Verification (4-layer, multi-res) | widget_tree/widget_capture/widget_read/pie_set_source/image_compare | ✅ |

## Gaps found while building this HUD → tooling added

Writing the sequence surfaced no missing verb: the composite-bind-on-child pattern,
the `format_text` ammo, the RT minimap material, and the dual-path menu all resolve to
existing tools. The one design subtlety made explicit here — **bind on the composite
child, not the parent** (the parent can't reach opaque internals) — is a usage pattern,
not a missing tool. If live validation surfaces a gap (e.g. a health bar that must be
driven from the *parent's* viewmodel), the follow-up is `widget_bind_mvvm` (Phase 3) or
a composite `ExposeOnSpawn`-style setter — noted for the live pass.

> **Status:** authored as a spec against the A+ tool surface; the Go layer is
> unit-tested, the Python/C++ are written to the plan and gated for correctness. LIVE
> execution (running this sequence against a real editor with the compiled
> `MCPAuthoring` module) is the morning validation step.
