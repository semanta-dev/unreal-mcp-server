# Unreal MCP — HUD/UMG Tooling: Evaluation & Rebuild Plan

Reviewer stance: shipped-multiple-titles UMG/Slate engineer. Verdict is blunt on purpose. Grounded against the actual surface: `internal/tools/authoring2_tools.go`, `capture_tools.go`, `control_tools.go`, `discovery_tools.go`, `internal/snippets/py/mcp_bridge.py`, `internal/affordances/affordances.go`, and `plugin/UnrealMCP/Source/`.

**Grounding correction (verified in-repo, load-bearing for §3.0).** The plugin ships **one** module — `MCPCapture` (`UnrealMCP.uplugin`: `Type:Runtime`, `LoadingPhase:Default`; deps `Core/CoreUObject/Engine/InputCore/RenderCore/Json/Slate/SlateCore/ApplicationCore/ImageWrapper`). That single runtime module hosts **both** GameInstanceSubsystems: `UMCPCaptureSubsystem` (screenshot, `FSlateApplication::TakeScreenshot`, `MCPCaptureSubsystem.cpp:198`) and `UMCPControlSubsystem` (simulated input, `PC->InputKey(FInputKeyEventArgs::CreateSimulated(...))`, `MCPControlSubsystem.cpp:36`). There is no separate control module and no editor module. Every symbol the authoring family needs — `UWidgetBlueprint` tree mutation, blueprint compile, `FWidgetBlueprintEditor`, `UMVVMBlueprintView` — is **editor-only** and cannot link from a `Runtime` module. This drives the module split in §3.0.

**Target is UE 5.7; treat editor symbols as version-fragile.** UMG/Slate/MVVM churn across point releases: viewport add/remove moved to `UGameViewportSubsystem` in 5.1; MVVM authoring was reworked 5.3→5.5; several compile entry points changed signature. Every UE symbol below is the **5.7** spelling. The Phase-0 spike (Spike 0) confirms the reflection-reachable subset against the live 5.7 editor before any op is promoted; C++ ops compile against 5.7 headers. Do not assume a named editor symbol is stable across a major version without re-confirming.

**Reviewer-fix resolution map.** Each fix is resolved once, at its canonical site (tagged inline there); this map replaces the scattered echoes.

| Fix | Severity | Canonical resolution |
|---|---|---|
| #1 Composite / nested-UserWidget authoring | gamedev blocker | §3.0.1 base + §3.a *Composite dispatch* (C++ `ConstructWidget`); scope table; §5 regression |
| #2 Multi-resolution geometry oracle | gamedev + umg blocker | §2.7 layer 3 + §3.f (offscreen `FWidgetRenderer`); live `widget_inspect` demoted to current viewport |
| #3 Menu click routing | umg blocker | §3.0.1 `UMCPButton` + §3.c `widget_bind_event` |
| #4 Objective-marker DPI | umg major | §3.0.1 `FMCPWorldTrackBinding` (`ProjectWorldLocationToWidgetPosition`) |
| #5 GAS/component source-arrange | gamedev major | §3.e `pie_set_source` |
| #6 Unhandled-widget enumeration | cto major | §3.e `EnumerateViewportWidgets` |
| #7 Tool-surface budget | cto major | §3.0.2 (~20 net-new distinct tools, 100→~120 server total; 15-verb primary core after folding; family gated on module detection; edits demoted to compose sub-ops) |
| #8 Reflection spike first + sizing | cto major | Spike 0 + per-phase sizing lines |
| #9 Minors (ammo / cross-version / undo / digest) | minor | §3.0.1, scope table, §3.a, §3.h, header |

---

## 1. VERDICT

**Grade: F.** Not "D, needs polish." F, because the core verb an agent needs — *put a widget in a HUD and bind it to a number* — does not exist, and the two things that do exist aim at the wrong target. The autonomous loop (author → see → read → interact → iterate) is broken at every stage.

**No UMG authoring engine exists, and the code admits it.** `widget_create` (`authoring2_tools.go:128` → `_op_widget_create`, `mcp_bridge.py:1907`) creates a `WidgetBlueprint` shell and returns, verbatim: `"note": "widget tree/binding authoring needs the UnrealMCP C++ plugin (P7)"`. That plugin was never built. Every UMG verb — construct a `TextBlock`, add it to a `CanvasPanel`, set an anchor, set `Percent`, bind to gameplay data — is absent. The only path is raw `execute_python` with no schema, no validation, no read-back.

**The two shipped capabilities are mis-aimed.** (1) HUD observation is pixels-only and triple-gated: `capture_start include_ui:true` (`MCPCaptureSubsystem.cpp:198` → `FSlateApplication::TakeScreenshot`) needs the plugin, a **live PIE**, **and a rendering foreground window**, and returns an opaque cell with **zero structure** — no `TextBlock` text, no `ProgressBar.Percent`, no rectangle. (2) `pie_input` (`MCPControlSubsystem.cpp:36` → `PC->InputKey(FInputKeyEventArgs::CreateSimulated(...))`) drives the **PlayerController gameplay input stack**, not Slate focus/pointer, so it cannot click a named `Button`, and there is no `SetInputMode`/cursor anywhere.

**The five canonical elements today:** health bar — dead on arrival (can't add the ProgressBar, can't bind); ammo — nothing; minimap — shell only; objective marker — no projection/per-tick path; menu — can't build it and can't click it. **Net: the agent is mute (can't author), blind (pixels only, no structure/values/geometry), and paralyzed (can't reach Slate).**

The redeeming scaffolding is real and reusable — `buildMontageResult`, `image_compare`, the reflection-set path behind `blueprint_set_defaults`/`pie_set_property`, the `_issue()` taxonomy (`mcp_bridge.py:69`), `pie_verify`, `scene_digest`, `scene_snapshot`/`scene_restore`, `reflect_class`/`map_gameplay` (`discovery_tools.go:105,130`), and `affordances` — which is why the fix is **add tools**, not start over. As HUD tooling, today's surface earns an **F**.

---

## 2. WHAT GOOD LOOKS LIKE

The end-to-end workflow, in UE 5.7 terms, with observe→iterate as a first-class citizen.

### 2.1 Author the asset
Create a `WidgetBlueprint`. **Default parent is plain `/Script/UMG.UserWidget`**; deriving from the plugin-owned `UMCPHUDWidget` is an **explicit opt-in** (§3.0.1, content-integrity caveat in §3.a). Root panel is `CanvasPanel` for HUDs, `Overlay` for stacked full-screen menus.

> **BindWidget contract (routine, not an edge case).** If `parent_class` declares `meta=(BindWidget)` properties, the WBP **will not compile** unless the authored tree contains widgets whose *name and type* match each bound property (`BindWidgetOptional` may be absent). Opting into a custom base is exactly how you get typed setters, so this fails routinely. Authoring enumerates the parent's `BindWidget`/`BindWidgetOptional` set, requires matching nodes, and surfaces the compile log. **This cannot be done from Python** — `unreal` exposes no per-`UPROPERTY` metadata reader — so it is a C++ op in `MCPAuthoring`: `TFieldIterator<FObjectPropertyBase>` over the parent `UClass`, filtered by `HasMetaData(TEXT("BindWidget"))` / `HasMetaData(TEXT("BindWidgetOptional"))`. Pinned in §3.a.

### 2.2 Compose the widget tree
Build the hierarchy from panels and leaves, each **named** (mandatory, §3.a) and, where it must be reachable from the generated class, marked **"Is Variable"**:
- Runtime `widget_read`/`widget_inspect` resolve a widget by name via `UUserWidget::GetWidgetFromName` / `UWidgetTree::FindWidget`, which work for **any named widget regardless of `bIsVariable`** — so `is_variable` is **not** required for value read-back.
- `is_variable` **is** required for named-property access on the generated class, `BindWidget` resolution, and MVVM widget-source resolution. Author it on nodes that feed those; a missing flag elsewhere is a warning, not a hard fail (§3.a).

**Composite (nested-UserWidget) authoring is first-class (fix #1).** The dominant shipped HUD pattern is a WBP embedded in a WBP (e.g. `WBP_HUD` embeds `WBP_HealthBar`). A composite child is a `UUserWidget`-derived node whose class is another WBP's generated class; it appears as one opaque node in the parent (its subtree is not editable from the parent). This node type **cannot** be built with Python `new_object` and takes the C++ `UWidgetTree::ConstructWidget` path — §3.a pins detection and dispatch.

Palette — **panels:** `CanvasPanel`, `Overlay`, `VerticalBox`/`HorizontalBox`, `ScrollBox`, `UniformGridPanel`/`GridPanel`, `SizeBox`, `Border`, `ScaleBox`, `WidgetSwitcher`, `SafeZone`. **Leaves:** `TextBlock`/`RichTextBlock`, `Image`, `Button`/`UMCPButton`, `ProgressBar`, `Slider`, `CheckBox`, `EditableText(Box)`, `Spacer`, `NamedSlot`, plus any `UUserWidget`-derived composite child.

### 2.3 Layout via slots + anchors + alignment
Each panel gives its child a **different slot class**:
- `CanvasPanelSlot`: **Anchors** (min/max), **Offsets** (Left/Top/Right/Bottom — meaning *changes* with point-vs-stretch anchor, see the annotated JSON in §3), **Alignment** (0..1 pivot), **ZOrder**, `SizeToContent`.
- `HorizontalBoxSlot`/`VerticalBoxSlot`: **Size** (Auto vs Fill+weight), **Padding**, H/V align. `OverlaySlot`/`BorderSlot`: Padding + H/V align. `GridSlot`: row/col/span.

Good tooling exposes **anchor presets** ("TopLeft", "Center", "Fill", "BottomCenter") that set anchors+alignment+offsets *coherently* — the anchors×alignment×offsets interplay is the #1 "why is it in the wrong place" bug. **Anchors exist to keep a HUD pinned across aspect ratios and DPI, so correctness is only proven by inspecting at more than one resolution — via the offscreen renderer (§2.7 layer 3, §3.f), never the live viewport.**

### 2.4 Bind widgets to gameplay data
Two tiers ship; a third (MVVM) is gated behind a spike with a kill-criterion (§3.c). All live on the **opt-in** `UMCPHUDWidget` base (§3.0.1); deriving a WBP from it is the price of the supported binding story, with a bake-out before shipping (§3.a).

- **Interim reflective drive (`UMCPHUDWidget`) — the supported default.** Two halves so a health bar *tracks gameplay*, not just displays a poked value:
  - **Push (imperative):** typed setters `SetFieldFloat/SetFieldText/SetFieldInt/SetFieldBool(FName Field, <T> Value)` resolve the named sub-widget on the live instance and apply the value. Driven from tests/arrange-time via `widget_set_fields` (§3.e).
  - **Pull (configured, per-tick):** `FMCPFieldSourceBinding` config persisted on the **WBP's generated-class CDO** (§3.c), evaluated in `NativeTick` against a **pinned path grammar** (§3.0.1). A health bar is then a *configured property* reading the pawn's `Health`/`MaxHealth` (or a GAS attribute) every tick — the same mechanism as the objective marker. The separate-property ratio case, a single-value getter, and a GAS source are all first-class (§3.0.1). Written by `widget_bind_field`.
- **MVVM (`ModelViewViewModel`) — the aspirational target**, gated on the spike (§3.c). A `UMVVMViewModelBase` viewmodel with `FieldNotify` properties + declarative view bindings in `UMVVMBlueprintView`. Touches **two** risky editor surfaces — view-binding authoring *and* `FieldNotify` variable authoring — spiked separately.
- **Event-driven** — `OnClicked` routed via a **persisted `UPROPERTY` on a `UMCPButton` node** (§3.0.1), not an authored event-graph node.

### 2.5 Style
Fonts (`FSlateFontInfo`: family asset + size + typeface + letter spacing), colors (`ColorAndOpacity`, `ContentColorAndOpacity`), `FSlateBrush` on `Image`/`Border` (texture or material, tint, draw-as, tiling), Slate style assets (`FButtonStyle`, `FTextBlockStyle`), render opacity/transform, `UWidgetAnimation`.

### 2.6 Wire it to the player
- **HUD:** at PIE start, `UWidgetBlueprintLibrary::Create(World, Class, PC)` + `AddToViewport(ZOrder)`. Real projects do this three incompatible ways (a project HUD-widget property, a `CreateWidget` in some `BeginPlay`, or nowhere yet), so the plan cannot rely on any convention — see the registry in §3.e. (`AHUD::HUDClass` is the *legacy Canvas* HUD; a UMG HUD is a `UUserWidget` added to the viewport.)
- **Menu:** same, plus `SetInputMode(UIOnly/GameAndUI)` and `bShowMouseCursor = true`.

### 2.7 The OBSERVE → ITERATE loop (what makes autonomy real)
Four layers, cheapest-that-can-fail first:
1. **Structural read-back (deterministic):** dump the WidgetBlueprint's tree as canonical JSON → hashable `widget_digest` (§3.h). Catches "did the tree change" with zero rendering.
2. **Value read-back (semantic):** on a live on-screen widget, read `TextBlock.GetText()`, `ProgressBar.Percent`, `CheckBox.IsChecked()`, `Button.GetIsEnabled()`, `Visibility`. Asserts "health=75% ⇒ bar.Percent≈0.75 ∧ label=='75'." Resolves by name (`GetWidgetFromName`), so **no `is_variable`**.
3. **Geometry read-back (layout, multi-resolution) — the offscreen oracle (fix #2).** *Live* `widget_inspect` reports geometry only for the **current painted viewport**: `SWidget::GetCachedGeometry()` reflects the actual painted viewport size, so no live widget can report a *different* resolution's layout — a live `resolution` arg would be a lie. Multi-resolution layout truth comes from the **offscreen `FWidgetRenderer` prepass in `widget_capture`** (§3.f): draw the widget into a render target of the requested `DrawSize` with the per-size DPI scale applied, then read each named widget's cached geometry from *that* sized draw — arbitrary resolution, no PIE, no live tick. Absolute px via `FGeometry::GetAbsolutePosition()`/`GetAbsoluteSize()`/`GetLocalSize()`; DPI-scaled viewport rect via `USlateBlueprintLibrary::AbsoluteToViewport` + `UWidgetLayoutLibrary::GetViewportScale`. Asserts "health bar is top-left, inside the SafeZone, not overlapping ammo, at 1280×720 **and** 3840×2160" — the one property anchors exist to provide. **This same offscreen prepass is the author-time (no-PIE) layout-feedback path — one mechanism serves both fix #2 and design-time feedback (§3.f).**
4. **Perceptual read-back (visual):** render the HUD to an image and gate against a golden with tolerance.

Layers 1–3 are missing today; layer 4 exists only as pixels-with-no-anchor. **Layers 1–2 are the PRIMARY oracle; pixels (layer 4) are the last mile.**

---

## 3. THE PLAN

Design principles, because they decide the shape of every tool:

- **Asset-authoring vs runtime-only are separate tool families AND separate modules** (§3.0). Never conflate "add a widget to the asset" (persisted, editor-only) with "add a widget to the live viewport" (ephemeral, runtime).
- **One canonical widget-node JSON**, used identically by asset read-back and runtime introspection. The `CanvasPanelSlot` offset duality is annotated inline:
  ```jsonc
  { "name": "HealthBar", "class": "ProgressBar", "slot_type": "CanvasPanelSlot",
    "slot": {
      "anchors": [0,0,0,0],          // min==max => a POINT anchor (top-left)
      "offsets": [24,24,220,20],     // POINT  => [posX, posY, sizeX, sizeY] = pos(24,24) size 220x20
                                     // STRETCH (min!=max) => [left,top,right,bottom] EDGE MARGINS
      "alignment": [0,0], "z": 0 },
    "props": {"Percent": 0.75, "Visibility": "Visible"},
    "text": {"Content": {"kind":"literal","value":"75"}},   // literal-vs-binding (C++ Bindings/MVVM-view walk, §3.h)
    "geometry": {"abs":[24,24,220,20], "dpi":1.0, "res":[1920,1080], "valid":true, "source":"offscreen"}, // RUNTIME/CAPTURE ONLY; excluded from the digest
    "children": [ ... ] }
  ```
  `widget_compose` **echoes which interpretation it resolved** per canvas node (`slot.offset_meaning: "point:[posX,posY,sizeX,sizeY]"` vs `"stretch:[l,t,r,b]"`) so the agent verifies the layout model instead of assuming it.
- **Every node MUST be named.** UMG auto-names unnamed widgets (`CanvasPanel_0`, …), which churns the digest and breaks `is_variable`/generated-class resolution. Every node in a `widget_compose` spec requires an explicit `name`; unnamed → `NAME_REQUIRED`.
- **Determinism:** every authoring op returns the affected node name + `widget_digest` (§3.h).
- **Idempotence + convergence:** nodes are **name-addressed**. `widget_compose` is a declarative reconcile that **converges from a divergent (permuted/reparented) starting tree**, not just from a re-run of the same spec (§3.a).
- **Atomic-first, explicit transaction model:** `widget_compose` (one round-trip: reconcile + single compile + save) is the **PRIMARY and essentially only** authoring path — fine-grained edits are *sub-operations of the compose spec*, not separate tools (fix #7, §3.0.2). Transaction/undo semantics pinned in §3.a.
- **Destructive ops opt-in + reversible:** compose is **additive/update-only by default**; node removal requires `prune:true` or an explicit `remove:[names]`. Destructive ops snapshot the pre-mutation tree and echo a `restore_token` (§3.a) — mirroring `scene_snapshot`/`scene_restore`.
- **Structured errors + partial failure:** adopt `_issue(code,label,message)` (`mcp_bridge.py:69`) and the `errors:[...]` accumulator. Taxonomy in §3.g.
- **Reuse the verification substrate:** `buildMontageResult`, `image_compare`, `pie_verify`, `scene_digest`, `reflect_class`/`map_gameplay`, `affordances`.
- **Register each new tool in `affordances.go`** with true gates (§3.i).

### 3.0 Module architecture (the load-bearing C++ decision)

**Decision — two modules, split by link-time reality.** `MCPCapture` is `Runtime` and cannot link `UMGEditor`/`Kismet`/`BlueprintGraph`/`UnrealEd`/`ModelViewViewModelBlueprint`, which tree mutation, structured compile, and MVVM authoring require. So:

1. **`MCPAuthoring` — NEW sibling module. `Type: Editor`, `LoadingPhase: Default`.** Deps: `UMG`, `UMGEditor`, `Kismet`, `KismetCompiler`, `BlueprintGraph`, `UnrealEd`, `Blutility`, `Slate`, `SlateCore`, `RenderCore`, and (behind the MVVM spike) `ModelViewViewModel` + `ModelViewViewModelBlueprint`. Exposes **`UMCPAuthoringSubsystem : UEditorSubsystem`**, reached from the bridge as `unreal.get_editor_subsystem(unreal.MCPAuthoringSubsystem).<op>(...)`.
   > **Why `Editor`, not `UncookedOnly`.** The subsystem is a `UEditorSubsystem`, instantiated only when `GEditor` exists. `UncookedOnly` loads in uncooked non-editor `-game` runs where there is no `GEditor`, so it would never spawn there. `headless_run` uses `UnrealEditor-Cmd` (editor engine, `GEditor` present), which loads `Editor` modules, so `get_editor_subsystem(MCPAuthoringSubsystem)` is reachable there too. `Editor` strips cleanly from cooked/shipping targets — these are author-time-only ops that must never enter a game build.
   > **Reached via `get_editor_subsystem`, NOT like control.** `UMCPControlSubsystem::Get(World)` is a `UGameInstanceSubsystem` reached through the running game. `UMCPAuthoringSubsystem` is a `UEditorSubsystem` reached through `unreal.get_editor_subsystem(...)` in the editor process. Different mechanism, different lifetime.

2. **`MCPCapture` — keep `Runtime`, extend in place.** Runtime ops operate on a live `UUserWidget`/Slate in the game world: the `ui_click` Slate-pointer extension on `UMCPControlSubsystem`; a new **`UMCPHUDRegistrySubsystem : UGameInstanceSubsystem`** (auto-spawn registry + on-screen widget enumeration, §3.e — PIE/editor-gated, cooked-safe); the reusable **`UMCPHUDWidget : UUserWidget`** base and **`UMCPButton : UButton`** (§3.0.1). If the GAS `ability_system` source (§3.0.1) or `pie_set_source` GAS arrange (fix #5) is enabled, `MCPCapture.Build.cs` adds `GameplayAbilities` as an **optional, guarded** dep so a project without it still links.

> **Reconciliation.** The runtime module physically cannot link editor code; the linker settles it: sibling `MCPAuthoring` (`Editor`) for asset authoring, extend `MCPCapture` (`Runtime`) for live ops.

#### 3.0.1 `UMCPHUDWidget` + `UMCPButton` — the reusable bases (Phase-0 plugin deliverable, OPT-IN)
Small C++ classes in `MCPCapture` that anchor binding, event handling, and per-tick behavior an agent cannot author as a graph. **Opt-in**, not the `widget_create` default (content-integrity caveat, §3.a).

**`UMCPHUDWidget : UUserWidget` exposes:**

- **Push setters** `SetFieldFloat/SetFieldText/SetFieldInt/SetFieldBool(FName Widget, FName Field, <T> Value)` — four typed `UFUNCTION`s (a single variadic/templated `UFUNCTION` is impossible in UE reflection). **Both targets are explicit params (fix #13):** `Widget` is the named sub-widget resolved via `WidgetTree`; `Field` is the exact reflected property applied on it (e.g. `Widget="HealthBar", Field="Percent"` / `Widget="Ammo", Field="Text"`). Naming `Field` explicitly removes the widget-type→canonical-property guess — no hidden `ProgressBar⇒Percent`/`TextBlock⇒Text` mapping table to drift. Interim "push" (§2.4), callable through `call_method`.

  **Live-Slate apply contract — applying a field value is NOT a bare `FProperty` write (pinned; governs BOTH these push setters AND the `NativeTick` pull application below).** A raw reflected set on a *live* sub-widget updates the `UWidget` `UPROPERTY` but does **not** reach its cached `SWidget`: UMG only pushes `UPROPERTY`s into Slate inside `SynchronizeProperties()` (run once at `TakeWidget`/`RebuildWidget`) or through each widget's typed `BlueprintCallable` setter (`SetPercent`/`SetText`/`SetColorAndOpacity`/…). A bare set therefore moves `Percent`/`Text` on the object while the on-screen bar/label stays **frozen** — and it is insidious because `widget_read`/`pie_verify` read the same `UPROPERTY` (or `GetPercent`/`GetText`), so the value oracle **false-passes** on stale Slate. (The objective marker escapes this only because `UCanvasPanelSlot::SetPosition` pushes to live Slate itself — that asymmetry is precisely why reflected *value* writes need an explicit refresh and slot writes do not.) **Apply = reflected set + forced Slate refresh**, one of:
  - **(a) Generic (default):** after the `FProperty` set, call `TargetWidget->SynchronizeProperties()` once — a single call re-pushes `Percent`/`Text`/color/visibility together, preserving the "no per-type setter table" philosophy.
  - **(b) Typed setter:** resolve and `ProcessEvent`-invoke the widget's typed `BlueprintCallable` `UFUNCTION` (`SetPercent`/`SetText`/`SetColorAndOpacity`), **falling back to (a)** (`FProperty`-set + `SynchronizeProperties()`) when no typed setter exists for that `Field`.

  Every later "apply the exact reflected property" in this doc means **this contract**, never a bare `FProperty` write.

- **Per-tick pull config** — persisted `UPROPERTY` arrays on the **WBP's generated-class CDO** (§3.c), evaluated in `NativeTick`:
  - `TArray<FMCPFieldSourceBinding> FieldSourceBindings` — the derived/ratio-capable value binding (struct below). Written by `widget_bind_field`.
  - `TArray<FMCPWorldTrackBinding> WorldTrackBindings` — `{marker_widget, target, world_offset}`; each tick projects the world point and moves the marker's `CanvasPanelSlot`. Written by `widget_track_actor`.

  **`FMCPFieldSourceBinding` — pinned struct:**
  ```
  FName            TargetWidget;      // named sub-widget on this UUserWidget
  FName            TargetField;       // reflected prop on the sub-widget: "Percent", "Text", "Visibility", …
  EMCPBindSource   Source;            // OwningPawn | OwningPC | PlayerState | WorldActor | AbilitySystem
  FString          SourceLabel;       // WorldActor only: the actor label; else empty
  FString          Path;              // numerator/value path (grammar below); OR a zero-arg getter returning the final value
  FString          MaxPath;           // OPTIONAL denominator path (same grammar). If set (or Conversion==Ratio), value = read(Path)/read(MaxPath)
  FName            Attribute;         // AbilitySystem only: numeric gameplay attribute (via ASC->GetNumericAttribute)
  FName            MaxAttribute;      // AbilitySystem only: OPTIONAL denominator attribute
  EMCPFieldConversion Conversion;     // None | Ratio | IntToText | FloatToText | FloatToPercent | BoolToVisibility | FormatText
  FString          Format;            // FormatText only: an FText format string with {value}/{max} args (fix #9)
  ```
  The **flagship health bar** is expressible three ways, all shipped:
  1. **Separate reflected properties (the common case):** `Source=OwningPawn, Path="Health", MaxPath="MaxHealth", TargetField="Percent", Conversion=Ratio` → `Percent = Health/MaxHealth`. (The old single-path schema could not express this — that was the blocker.)
  2. **A single 0..1 getter:** `Source=OwningPawn, Path="GetHealthPercent", Conversion=None` — `Path` resolves to a zero-arg `BlueprintPure` getter.
  3. **GAS (Lyra-style):** `Source=AbilitySystem, Attribute="Health", MaxAttribute="MaxHealth", Conversion=Ratio` → both read via `UAbilitySystemComponent::GetNumericAttribute(Attribute)` (an `FGameplayAttributeData` is not a UPROPERTY reachable by a dotted path, so it needs this route). Requires the `GameplayAbilities` plugin; else `PLUGIN_DISABLED` with remediation.

  **Ammo (fix #9):** a real counter is `"24 / 30"`, not a bare int. `Source=OwningPawn, Path="Ammo", MaxPath="MaxAmmo", TargetField="Text", Conversion=FormatText, Format="{value} / {max}"` → `NativeTick` builds `FFormatNamedArguments` with keys `value`/`max` and applies `FText::Format(FTextFormat::FromString(Format), Args)`. A bare number uses `Conversion=IntToText` (`FText::AsNumber`). The marker (single actor) needs no conversion.

  **Pull evaluation contract (pinned):**
  - **Source-object resolution (per tick):** `OwningPawn = GetOwningPlayerPawn()`; `OwningPC = GetOwningPlayer()`; `PlayerState = GetOwningPlayer()->PlayerState`; `WorldActor` = actor found by `SourceLabel`; `AbilitySystem = UAbilitySystemBlueprintLibrary::GetAbilitySystemComponent(OwningPawn)`, falling back to the PlayerState's ASC. **The owning pawn is frequently null before possession** — when the source is null, emit a **one-shot `SOURCE_NULL` issue** and **hold last-good**, never write `0`.
  - **Path grammar (documented subset):** a dotted chain, **max depth 4**, each segment a `UPROPERTY` (`FindFProperty` + `ContainerPtrToValuePtr`) **or** a zero-arg `BlueprintPure`/`BlueprintCallable` `UFUNCTION` (via `ProcessEvent`). **Per-segment order: FProperty first, then zero-arg getter.** Struct-leaf/array access beyond this is rejected at author time. A `nullptr` at any hop → one-shot `SOURCE_NULL` + hold last-good.
  - **Handle caching:** at `NativeConstruct`, resolve each binding's `FProperty*`/`UFunction*` chain against the source class and cache the handles + a `TWeakObjectPtr` to the source. `NativeTick` reuses cached handles; **re-resolves only when the source-object identity changes** (unpossessed→possessed swap), which implements **possession recovery**. Per-frame name lookup is rejected.
  - **Ratio safety:** denominator `0` → write `0` + one-shot `RATIO_DENOM_ZERO`, never `inf`/`NaN`.
  - **Slate application (pinned — NOT a bare `FProperty` write):** writing the converted value to `TargetField` on the live sub-widget follows the **live-Slate apply contract** under the push setters above — reflected set **+** `TargetWidget->SynchronizeProperties()` (or the typed `SetPercent`/`SetText`/… setter via `ProcessEvent`) — **every tick**. A bare reflected set updates the `UWidget` `UPROPERTY` but leaves the cached `SWidget` frozen, so the bar/label never moves on-screen while `widget_read`/`pie_verify` (reading the same `UPROPERTY`/getter) false-pass. This is the pull-side of the same contract that governs the push setters.

  **`FMCPWorldTrackBinding` — objective marker, DPI-correct (fix #4).** Each tick, for `WorldLoc = target->GetActorLocation() + world_offset`:
  ```cpp
  FVector2D Pos;
  if (UWidgetLayoutLibrary::ProjectWorldLocationToWidgetPosition(GetOwningPlayer(), WorldLoc, Pos, /*bPlayerViewportRelative=*/false)) {
      if (UCanvasPanelSlot* S = UWidgetLayoutLibrary::SlotAsCanvasSlot(Marker)) S->SetPosition(Pos);  // Pos is the projected point; centering is fixed at author time
      Marker->SetVisibility(ESlateVisibility::HitTestInvisible);
  } else { Marker->SetVisibility(ESlateVisibility::Collapsed); }  // behind camera
  ```
  **Use `UWidgetLayoutLibrary::ProjectWorldLocationToWidgetPosition` — it returns DPI-scaled widget-space (divides by the viewport DPI scale internally) — feeding `UCanvasPanelSlot::SetPosition`. Do NOT use `APlayerController::ProjectWorldLocationToScreen`, which returns raw viewport *pixels* and drifts under DPI≠1.** The marker's slot must be a `CanvasPanelSlot`.

  **Marker-slot centering — FORCED at author time (fix #3).** `SetPosition(Pos)` places the slot's *anchor-relative pivot* at `Pos`; unless the slot pivots on its own center, the marker lands off by up to its own size and mis-tracks under resize. So `widget_track_actor` **forces the marker's `CanvasPanelSlot` to a point anchor and a centered pivot** before writing the binding: `S->SetAnchors(FAnchors(0.f,0.f,0.f,0.f))` (min==max==(0,0)) and `S->SetAlignment(FVector2D(0.5f,0.5f))`, so `Pos` is interpreted as top-left-anchored widget-space and the alignment re-centers the marker on the projected point. A marker whose authored slot has **non-(0,0) anchors** is either **rejected with `MARKER_ANCHOR_INVALID`** or, when `auto_correct:true` (default), **auto-corrected to the forced values with a `MARKER_ANCHOR_CORRECTED` warning** — never silently mis-centered.

- **Menu click routing — `UMCPButton`, param-bearing dispatch (fix #3).** `UButton::OnClicked` is a **0-arg dynamic multicast** (`FOnButtonClickedEvent`), so a single shared 0-param handler cannot tell which button fired — the naive single-handler design is broken for exactly this reason. **Fix:** `UMCPButton : UButton` carries a persisted `UPROPERTY(EditAnywhere) FName Command`. It self-binds in `RebuildWidget()` (the runtime construction hook — `UButton` has no `NativeConstruct`), so the delegate wire is formed at runtime from persisted data — no serialized delegate, no graph node:
  ```cpp
  TSharedRef<SWidget> UMCPButton::RebuildWidget() {
      TSharedRef<SWidget> W = Super::RebuildWidget();
      if (!OnClicked.IsAlreadyBound(this, &UMCPButton::HandleClick))
          OnClicked.AddDynamic(this, &UMCPButton::HandleClick);
      return W;
  }
  UFUNCTION() void UMCPButton::HandleClick() {                 // per-instance: this->Command is known
      if (UMCPHUDWidget* H = GetTypedOuter<UMCPHUDWidget>()) H->RunNamedCommand(Command);
  }
  ```
  At runtime a widget's outer chain is Button → `WidgetTree` → owning `UUserWidget`, so `GetTypedOuter<UMCPHUDWidget>()` resolves the owner. `widget_bind_event` simply writes `Command` on the named `UMCPButton` **node** (a `UPROPERTY` on the tree subobject — serialized with the WBP), then compiles. Because `Command` rides the button, dispatch is unambiguous. **Fallback if a project cannot use a custom button class:** a fixed pool of indexed `UFUNCTION`s (`HandleClick0..N`) on `UMCPHUDWidget`, each `AddDynamic`-bound in `NativeConstruct` to one named `UButton` and forwarding a per-index command — same param-bearing effect, bounded arity.

- **A fixed, enumerable handler set — pinned default bodies.** `UFUNCTION`s tagged `meta=(MCPHandler)` so `widget_describe` enumerates them (§3.a) and the agent binds a **discovered** list:
  - `OnResume()` → `RemoveFromParent()` + `set_input_mode(GameOnly)` + `PC->bShowMouseCursor=false`.
  - `OnQuit()` → `UKismetSystemLibrary::QuitGame(this, GetOwningPlayer(), EQuitPreference::Quit, /*bIgnorePlatformRestrictions=*/false)`.
  - `OnOpenPanel(FName PanelName)` → resolve a `WidgetSwitcher` (named by `UPROPERTY FName MenuSwitcherName`, default `"MenuSwitcher"`) via `WidgetTree`, map `PanelName` → child index by name, `SetActiveWidgetIndex`. Missing switcher/panel → `UNKNOWN_COMMAND`.
  - `RunNamedCommand(FName)` — the generic dispatch routing to the above and any project-added `MCPHandler`. **An unmapped command fails loudly** (error + log; negative test in §5). **"Menu ✅ full" means each handler produces a real, observable state delta** — verified in §5 (click → `widget_read`/`widget_inspect` confirms removed-from-viewport / input-mode / active-panel-index changed), not just the negative path.

**Honest boundary:** bespoke handler logic beyond the fixed set requires adding a `UFUNCTION` to `UMCPHUDWidget` in C++ and recompiling — a compile-in-the-loop step, the deliberate edge of "no node-graph authoring."

#### 3.0.2 Tool-surface budget — counted honestly (fix #7; fix #5)
~30 fine-grained tools in one family is unusable, so fine-grained edits are **sub-operations of `widget_compose`'s node spec**, not separate tools. **The honest count (fix #5), no slash-hiding of distinct names:**

- **≈20 net-new distinct MCP tool names.** `widget_create` is an **upgrade** of the existing shell (already in the server's 100 tools), not net-new.
- **Server total: 100 → ~120** when the full authoring family is registered (see the gate below — most sessions register fewer).
- **Core primary surface: 15 verbs** after **folding paired verbs into single mode-carrying tools** — `widget_view{show|hide}` and `widget_tree{get|restore}`. (Elsewhere in this doc these are named by their **mode** for readability — `widget_show`/`widget_hide` ≡ `widget_view show`/`hide`; `widget_tree_get`/`widget_tree_restore` ≡ `widget_tree get`/`restore`.)

**Primary (15 distinct names):**

| # | Tool | Role |
|---|---|---|
| 1 | `widget_compose` ⭐ | author/patch the whole tree — **absorbs** add / remove / reparent / rename / set_slot / set_property / set_brush / set_font |
| 2 | `widget_compile` | flush/gate; structured compile log |
| 3 | `widget_tree` (`get`\|`restore`) | structural read-back (layer 1) + `widget_digest`; and undo a destructive compose (**folded pair**) |
| 4 | `widget_describe` | class/prop/slot catalog + `BindWidget` + `UMCPHUDWidget` handler/command enumeration — **absorbs** `widget_catalog` |
| 5 | `widget_bind_field` ⭐ | gameplay pull bind (health/ammo, incl. ratio/GAS/format) |
| 6 | `widget_track_actor` ⭐ | objective-marker bind |
| 7 | `widget_bind_event` | menu click (writes `UMCPButton.Command`) |
| 8 | `widget_view` (`show`\|`hide`) | live instantiate / remove (**folded pair**) |
| 9 | `widget_set_fields` ⭐ | batched live push — **absorbs** `widget_set_field` + the generic `widget_call` |
| 10 | `widget_set_focus` | menu focus for gamepad/keyboard-nav verification (fix #11) |
| 11 | `set_input_mode` | input mode + cursor for live UI |
| 12 | `ui_click` | Slate-pointer click on a named widget |
| 13 | `widget_inspect` | live tree + value/geometry read-back (current viewport) |
| 14 | `widget_read` | fast single-value read (`pie_verify` predicate source) |
| 15 | `widget_capture` | offscreen multi-res geometry & pixels |

**Auxiliary (thin, net-new but not "primary"):** `set_hud_widget` (in-game wiring registry), `pie_set_source` (test-arrange for component/GAS pull sources), `widget_make_rt_material` (minimap RT-sampling UI material, fix #1). Plus the `widget_create` **upgrade**.

**Phase-3 gated:** `widget_viewmodel_create`, `widget_bind_mvvm` (only if the MVVM spike is green; else these two never register → ~18 net-new).

**Global-surface strategy — register the family only when it can work (fix #5).** The widget family is **not** unconditionally added to every server. At startup the server probes which plugin modules are loaded (via `editor_ping`/`health_check` module enumeration, §3.i / fix #7):
- **`MCPAuthoring` (Editor module) present ⇒** register the authoring/observe verbs (`widget_compose`, `widget_compile`, `widget_tree`, `widget_describe`, `widget_bind_*`, `widget_track_actor`, `widget_capture`, `widget_create`, `widget_make_rt_material`).
- **`MCPCapture` (Runtime module) present ⇒** register the live-operate verbs (`widget_view`, `widget_set_fields`, `widget_set_focus`, `set_input_mode`, `ui_click`, `set_hud_widget`, `pie_set_source`).
- **Neither ⇒** none of the ~20 register; the agent sees the base 100 and `affordances` explains why. This keeps the global surface flat for projects without WBP-authoring capability, and never advertises a verb that would only fail.

**DEMOTED to `widget_compose` node-spec sub-operations (no longer MCP tools):** `widget_add`, `widget_remove`, `widget_reparent`, `widget_rename`, `widget_set_slot`, `widget_set_property`, `widget_set_brush`, `widget_set_font`. `widget_compose` accepts a **full tree** or a **partial patch** (`mode:"patch"`): nodes matched by name are updated (props/slot/brush/font); `remove:[names]` deletes; child order + parent structure in the spec drive reorder/reparent (details §3.a).

### (a) Authoring — WidgetBlueprint + tree (all through `widget_compose`)

| Tool | Inputs (schema sketch) | Impl | Returns |
|---|---|---|---|
| `widget_create` *(upgrade)* | `dest`, `parent_class?` (**default `/Script/UMG.UserWidget`**), `root_panel?` (default `CanvasPanel`) | **Python** (shell via `WidgetBlueprintFactory`) **+ C++** (reparent + `BindWidget` enumeration) | `{created, root, parent_class, required_bindwidgets:[{name,type,optional}]}` |
| `widget_compose` ⭐ (PRIMARY authoring verb) | `blueprint`, `tree` (nested node JSON: `name,class,slot,props,brush?,font?,is_variable?,children`), `mode?` (`full`\|`patch`, default `full`), `remove?:[names]`, `prune?` (default false) | **Python** reconcile (order + reparent + composite aware) → **C++** `widget_compile` (one atomic round-trip) | full `widget_tree_get` echo + `digest` + `compile_log` (+ `restore_token` if destructive) |
| `widget_compile` | `blueprint` | **C++** `FKismetEditorUtilities::CompileBlueprint(WBP, EBlueprintCompileOptions::None, &Log)` with an `FCompilerResultsLog Log` | `{compiled:bool, digest, compile_log, issues:[…]}` |
| `widget_tree_get` | `blueprint` | **Python** — read the **in-memory** tree (post-mutation, pre-compile) → canonical JSON | tree JSON + `digest` |
| `widget_tree_restore` | `blueprint`, `restore_token` | **Python** — re-apply a pre-mutation snapshot | restored tree + `digest` |
| `widget_describe` | `widget_class?` | **Python** (`reflect_class` for classes/props/slot class) **+ C++** (`BindWidget` + `UMCPHUDWidget` handler/command enumeration) | class/prop/slot catalog; for `UMCPHUDWidget`: `handlers:[…]`, `named_commands:[…]` |

**Composite / nested-UserWidget dispatch (fix #1).** Inside a `widget_compose` node, `class` is auto-classified:
- **Primitive `UWidget`** (`TextBlock`, `ProgressBar`, `CanvasPanel`, …): built in **Python** via `new_object` (recipe below).
- **`UUserWidget`-derived composite child** (another WBP's generated class, e.g. `/Game/UI/WBP_HealthBar`, or a native `UUserWidget` subclass): built in **C++** via `UMCPAuthoringSubsystem::AddChildWidget(UWidgetBlueprint* WBP, FName ParentName, UClass* WidgetClass, FName NewName, bool bIsVariable)`:
  ```cpp
  UWidget* Child = WBP->WidgetTree->ConstructWidget<UWidget>(WidgetClass);   // design-time embed — same call the palette drag uses
  UPanelWidget* Parent = Cast<UPanelWidget>(WBP->WidgetTree->FindWidget(ParentName));
  Parent->AddChild(Child);  Child->bIsVariable = bIsVariable;  // named opaque node
  ```
  **Detection:** the bridge routes to C++ when the node's asset is a `WidgetBlueprint` (`isinstance(load_asset(path), unreal.WidgetBlueprint)`) or the resolved class `IsChildOf(UUserWidget)`; the C++ op re-checks `WidgetClass->IsChildOf(UUserWidget::StaticClass())` authoritatively. **Why C++ is mandatory here:** `unreal.new_object` constructs without `ConstructWidget`'s `UUserWidget` initialization path, so a Python-built UserWidget child is malformed. `widget_tree_get` reports the composite as one node whose `class` is the child generated-class name; its subtree is **not** expanded (that is the composite pattern). **`UWidgetBlueprintLibrary::Create` is the *runtime* CreateWidget counterpart (used by `widget_show`, §3.e) — NOT the design-time embed path; do not use it in the authoring subsystem.**

**Python vs C++, per op.** The §3.a Python recipe is validated by Spike 0 (run FIRST — fix #8), against a scratch WBP:
- **Pure-Python bridge ops** (the `_op_*` pattern): primitive-node construction, slot/prop/brush/font application, `widget_tree_get`, reorder/reparent driver, and the `widget_compose` reconcile driver — **iff** the spike confirms `new_object` + `panel.add_child` + `set_editor_property('is_variable'/'root_widget')` reflect. **Fallback:** if a specific setter or the reorder primitive is rejected by Python, that op moves to the C++ subsystem; the rest is unaffected.
- **Always C++ (`MCPAuthoring`), API unreachable from Python:** composite `AddChildWidget` (`ConstructWidget`); `widget_compile` (`FCompilerResultsLog` — `compile_blueprint` returns `None` and routes diagnostics to the log, so Python cannot build the structured `compile_log`); `BindWidget` enumeration (`HasMetaData`); `widget_describe` handler enumeration (`TFieldIterator` + `meta`); `widget_bind_mvvm`/`widget_viewmodel_create`; `widget_capture` (`FWidgetRenderer`); the literal-vs-binding text walk (`UWidgetBlueprint::Bindings`/MVVM view).

**The authoring recipe (corrected — the original was built on a method that does not exist).** `UWidgetTree::ConstructWidget` is a *templated C++ helper with no `UFUNCTION`*, so `unreal.WidgetTree.construct_widget(...)` **is not reflected into Python**. The working reflected recipe for **primitive** nodes, locked in Spike 0 before promotion:
```python
wt   = wbp.get_editor_property('widget_tree')
root = unreal.new_object(root_panel_cls, outer=wt, name='RootPanel')  # new_object: PRIMITIVE UWidget subclasses only
wt.set_editor_property('root_widget', root)                            # bare UPROPERTY — spike-confirm; else C++ fallback
child = unreal.new_object(unreal.TextBlock, outer=wt, name='Ammo')
slot  = root.add_child(child)          # UNIVERSAL adder: returns the correctly-typed UPanelSlot for ANY panel
child.set_editor_property('is_variable', True)   # only where generated-class/BindWidget/MVVM resolution is needed
# finalize via the C++ widget_compile (FKismetEditorUtilities::CompileBlueprint), NOT compile_blueprint (no structured log)
```
**Adder rule:** `panel.add_child(child)` is the **universal** adder (correctly-typed `UPanelSlot` for every panel). Typed `add_child_to_canvas`/`_vertical_box`/`_horizontal_box`/`_overlay`/`_grid`/`_uniform_grid` exist only for those panels; `SizeBox`/`Border`/`ScaleBox`/`ScrollBox`/`WidgetSwitcher`/`SafeZone` have **no** typed adder and must use `add_child`. **`new_object` restriction:** primitive `UWidget` subclasses only — a composite child goes through the C++ `ConstructWidget` path above. **`widget_tree_get` walks** via `root_widget` + `get_children_count`/`get_child_at` (do not assume `WidgetTree.all_widgets` is reflected).

**Reorder / reparent primitive (pinned).** `add_child()` only **appends**, so child-order and cross-panel reparents need an explicit mechanism, spiked in Phase 0:
- **Reorder within a panel:** Spike 0 tests reflected `UPanelWidget.shift_child`/`insert_child_at`/`replace_child_at`; **expected outcome (fix #8): these are unlikely to be reflected**, so the committed path is the **fallback** — after the additive upsert, walk the spec's child list in order and `remove_child` + `add_child` each node so final append-order equals spec order.
- **Reparent (cross-panel move):** `old_parent.remove_child(node)` then `new_parent.add_child(node)` at the spec index. The `UWidget` object is reused (identity preserved), so `is_variable`/generated-class vars survive.
- **Slot props are re-applied AFTER every move — pinned reconcile order (fix #10).** `remove_child`+`add_child` mints a **fresh `UPanelSlot`**, discarding anchors/offsets/alignment/padding on the moved node. So the reconcile is strictly phased: **(1) perform ALL adds/reorders/reparents first, THEN (2) apply each node's `slot`/`props`/`brush`/`font` to *every* node** — equivalently, re-apply a node's slot body immediately after each `add_child`, since each add produces a new slot object. Slot application never runs *before* a move that would recreate the slot. Regression in §5: reorder a node carrying **non-default anchors** and assert its offsets survive in the post-compose `digest`.
- **Interaction with the additive rule:** reorder and reparent are structural **MOVES, exempt from the "`prune:false` leaves absent nodes alone" rule** — a moved node is relocated, never pruned. Only a node absent from the entire spec (with `prune:true`) or in `remove:[…]` is removed. §3.h's digest preserves child order, so a reorder is visible in the digest.

**`is_variable` assertion (warning, not hard-fail).** After `widget_compile`, check each spec-marked `is_variable` node for a matching named `UWidget` property on `wbp.generated_class()`. Surface `IS_VARIABLE_NOT_MATERIALIZED` as a **warning** by default (runtime `widget_read` works regardless). **Hard-fail only** when the node is a `BindWidget` target or explicitly `needs_class_exposure` (C++ named-property or MVVM widget-source resolution).

**NOT the SubobjectData flow.** Do **not** route widgets through `SubobjectDataSubsystem`/`AddNewSubobjectParams` (that drives the actor/component SCS tree in `blueprint_add_component`, `mcp_bridge.py:1826`). Widget finalize is the C++ `widget_compile` + `save_asset`.

**`widget_create` default parent — content-integrity (resolved).** Default `parent_class` is plain `/Script/UMG.UserWidget`. `UMCPHUDWidget`/`UMCPButton` are **opt-in only**, with a loud tool-description caveat: *"reparents to the plugin-owned `UMCPHUDWidget` — `MCPCapture` then becomes a permanent parent-class dependency; the WBP fails to load if the plugin is absent/uncooked."* **Bake-out:** `widget_compose`/`widget_create` with `parent_class=/Script/UMG.UserWidget` reparents the WBP off the plugin base before shipping; or copy the behaviors into the project's own always-cooked HUD base. The default path never welds a dev plugin into shipped content. The §3.e runtime registry carries the **same** discipline.

**`widget_compose` reconcile semantics (pins the flagship's idempotence).** With `prune:false` (default): nodes matched **by name** are updated in place (props/slot/brush/font); spec-only nodes are added; reparents/child-order follow the spec (child order never sorted); asset-only nodes are **left alone**. With `prune:true` (or listed in `remove:`): absent nodes are removed (snapshots first). The `widget_create` root is adopted if its name matches the spec root, else replaced. Then a single C++ `widget_compile`. **Convergence guarantee:** applying a spec to *any* starting tree yields the spec's `digest`. The idempotence test (§5) starts from a **deliberately permuted/reparented** tree and asserts convergence to the target `digest`, then re-runs for stability.

**Destructive-op safety (resolved).** Before any destructive mutation, dump the tree (`widget_tree_get`) to `Saved/MCP/widget_snapshots/<asset>/<timestamp>.json` and echo a `restore_token`; `widget_tree_restore` re-applies it — mirroring `scene_snapshot`/`scene_restore` (`affordances.go:66`).

**Transaction model + undo footgun (resolved — fix #9).** The primary path is `widget_compose`: **one bridge round-trip** that reconciles + compiles once — atomic, no cross-round-trip window. A partial `patch` compose self-compiles + saves. An explicit **open transaction** is available via `defer:true` on a `patch` compose, terminated by a required `widget_compile`; while open, mutations apply directly to the WBP's `WidgetTree` `UObject` (owned by the WBP, kept loaded/rooted by `EditorAssetLibrary`, so it does not GC between round-trips). **Serialization:** the bridge command channel is single-flight, so ops within a project cannot interleave; under the multi-project daemon each project has its own editor process and single-flight channel, so two projects can never race the same asset.
> **Undo footgun (state it):** `new_object` constructs without `RF_Transactional`, and these mutations apply **outside the editor transaction/undo buffer**. Consequence: a human pressing Ctrl+Z in a concurrently-open editor **will not** cleanly undo a bridge mutation, and mutating a WBP a human is editing can desync the undo buffer vs the compiled class. Mitigations: (1) the authoring model is **compile+save**, not interactive undo — recovery is `widget_tree_restore`, not Ctrl+Z; (2) each mutating C++ op calls `WBP->Modify()` / `WidgetTree->Modify()` before mutating so the package dirties correctly; (3) **concurrent human editing of the same WBP is explicitly unsupported** and documented in the tool description. Optionally wrap a compose in `ScopedEditorTransaction` for a single coalesced undo unit, but do not rely on it for correctness.

### (b) Layout (a `widget_compose` node sub-operation, not a tool)
Each node's `slot` is applied by the reconcile driver, slot-type-aware: **CanvasPanelSlot** `{anchor_preset? | anchors[min,max], offsets[l,t,r,b], alignment[x,y], z, size_to_content}`; **BoxSlot** `{size:{auto|fill,value}, padding, h_align, v_align}`; **Overlay/BorderSlot** `{padding,h_align,v_align}`; **GridSlot** `{row,col,row_span,col_span}`. The driver detects the actual slot class, sets reflected props, validates offsets against the anchor spread, and **fails `SLOT_TYPE_MISMATCH`** if the body doesn't match the parent's slot class.

Expose **anchor presets** ("TopLeft"/"Center"/"Fill"/"BottomCenter"/…) that coherently set anchors+alignment+offsets — the single highest-leverage correctness lever. The node echo includes **`offset_meaning`** (`point:[posX,posY,sizeX,sizeY]` vs `stretch:[l,t,r,b]`) so the agent verifies rather than assumes. A `SafeZone` helper covers title-safe HUDs. **Because anchors are the resolution-independence mechanism, slot correctness is proven only by the offscreen multi-resolution geometry check (§2.7 layer 3, §3.f, §5), never by a single-resolution echo.**

### (c) Data binding

> **Decision — ship the interim reflective path (`UMCPHUDWidget`) as the supported default; gate MVVM behind a spike with a kill-criterion.** `UMVVMBlueprintView` C++ authoring is a sparsely-exposed, version-churned editor API (reworked 5.3→5.5) that must not be implemented "verbatim" against 5.7 without the headers. Phase 1 demonstrates health=75%⇒bar via `UMCPHUDWidget` (push + configured pull); MVVM is Phase 3 and non-blocking.

**CDO write target (resolved — applies to all binding writers).** `widget_bind_field`/`widget_track_actor` write config to the **per-asset generated-class CDO** — `unreal.get_default_object(wbp.generated_class())` — then `widget_compile` + `save_asset`, exactly as `_op_blueprint_set_defaults` does (`mcp_bridge.py:1796`). They do **not** touch `UMCPHUDWidget`'s own base-class CDO (that would be global to every derived WBP → cross-asset corruption). `widget_bind_event` writes `Command` on the button **node** (§3.0.1), also per-asset. A §5 test authors two WBPs deriving `UMCPHUDWidget` and asserts binding independence.

**Binding-source discovery + author-time validation (resolved).** The agent does not guess `source`+`path`. Discovery reuses `reflect_class` (`discovery_tools.go:105`) / `map_gameplay` (`discovery_tools.go:130`) on the pawn/PC/PlayerState class. `widget_bind_field` **validates `path`/`max_path` against the source class at author time** (resolving each segment per the §3.0.1 grammar) and returns **`BIND_PATH_UNRESOLVED`** (with the offending segment) on failure — never a silent per-tick no-op. For an `ability_system` source it validates `Attribute`/`MaxAttribute` against a discoverable `AttributeSet` when present (else a warning).

| Tool | Inputs | Impl | Returns |
|---|---|---|---|
| `widget_bind_field` ⭐ | `blueprint`, `target_widget`, `target_field`, `source` (`owning_pawn`/`owning_pc`/`player_state`/`world_actor:<label>`/`ability_system`), `path`, `max_path?`, `attribute?`/`max_attribute?` (GAS), `conversion?` (`none`/`ratio`/`int_to_text`/`float_to_text`/`float_to_percent`/`bool_to_visibility`/`format_text`), `format?` (FormatText string, `{value}`/`{max}`) | **Python** (validate vs source class → write a `FieldSourceBindings` entry on the **generated-class CDO**) → **C++** `widget_compile`. Consumed each tick by `NativeTick`. The health/ammo gameplay bind. | `{bound, source, path, max_path?, target, conversion, format?}` + `digest` |
| `widget_track_actor` ⭐ | `blueprint`, `marker_widget`, `target` (world-actor label / `owning_pawn`), `world_offset?` | **Python** (write `WorldTrackBindings` on the generated-class CDO) → **C++** `widget_compile`. Consumed by `NativeTick` (`ProjectWorldLocationToWidgetPosition`, fix #4). | `{tracked, marker, target}` + `digest` |
| `widget_bind_event` | `blueprint`, `widget` (a `UMCPButton` node), `event` (`OnClicked`), `command` (a `RunNamedCommand` name, discovered via `widget_describe`) | **Python** (set `Command` on the `UMCPButton` node — a persisted node `UPROPERTY`) → **C++** `widget_compile`. Consumed by `UMCPButton::RebuildWidget`'s runtime `AddDynamic`. No graph authoring (§3.0.1). Fails `WIDGET_NOT_MCPBUTTON` if the node is a plain `UButton`. | `{bound, widget, command}` |
| `widget_viewmodel_create` | `dest`, `fields:[{name,type}]` | **C++** — author a `UMVVMViewModelBase` Blueprint with typed **`FieldNotify`** properties (**spike surface #2**) | `{created, fields}` |
| `widget_bind_mvvm` | `blueprint`, `viewmodel_class`, `bindings:[{widget, property, source_field, conversion?, mode?}]` | **C++** (`MCPAuthoring`); `conversion` maps to UMVVM's built-in conversion functions; **fail with the compile log** on an unbindable type pair | binding list + `digest` + `compile_log` |

**MVVM preconditions & spike (must be pinned by the spike, not assumed):**
- **Plugin gate.** If `ModelViewViewModel` is disabled in the `.uproject`, the editor modules won't load; `widget_bind_mvvm`/`widget_viewmodel_create` fail **`PLUGIN_DISABLED`** with remediation, never a link error. (Same gate class as GAS `ability_system`.)
- **Type-mismatch is the norm.** Ammo `int → TextBlock.Text (FText)` is unbindable without a conversion — hence the `conversion` enum on both binders. An unmapped pair → **`BIND_TYPE_MISMATCH`** + compile log.
- **TWO spike surfaces:** (1) **view-binding authoring** — `UMVVMBlueprintView::AddBinding`/property-path plumbing on the WidgetBlueprint extension; (2) **`FieldNotify` member-variable authoring** on the viewmodel Blueprint — creating a reflected member *with `FieldNotify` metadata* through the BP variable API, distinct from view-binding. Both must clear the spike; either failing narrows Phase 3.
- **Candidate 5.7 symbols to confirm (name them, don't guess):** `UMVVMWidgetBlueprintExtension_View::RequestExtension`/`GetBlueprintView`/`CreateBlueprintViewInstance`, `UMVVMBlueprintView::AddBinding`/`AddDefaultBinding`, `FMVVMBlueprintPropertyPath`, `UMVVMBlueprintViewModelContext`, `UMVVMEditorSubsystem`. Required in `MCPAuthoring.Build.cs`: `ModelViewViewModel`, `ModelViewViewModelBlueprint`, `UMGEditor`.
- **Kill-criterion + SCHEDULE.** The MVVM spike is **scheduled early and timeboxed — parallel to Phase 0/1** (Spike 0) — with a recorded **go/no-go** so Phase 3 scope is settled before Phase 2 ships. **If either surface proves intractable in 5.7, ship the `UMCPHUDWidget` push+pull path as the supported binding story and defer MVVM.** The effort does not sink on it.

### (d) Styling (node sub-operations of `widget_compose`)
A `widget_compose` node may carry `props`, `brush`, and `font` bodies applied by the Python reconcile driver (reusing the reflection-set path behind `blueprint_set_defaults`), accumulating `issues:[_issue(...)]` per property (partial-failure, §3.g):
- **`props`** — `Text` as **FText**, `ColorAndOpacity`, `Font.Size`, `Padding`, `Visibility`, `RenderOpacity`, `RenderTransform`, and any reflected leaf prop.
- **`brush`** — `{image (existing texture/material path), tint?, draw_as?, tiling?, image_size?}` → build `FSlateBrush`, set on `Image`/`Border`.
- **`font`** — `{font_family (asset), size, typeface?, letter_spacing?}` → set `FSlateFontInfo`.

**`widget_make_rt_material` — COMMITTED, bounded (fix #1; closes the minimap → 5/5).** A single narrow op (NOT the general material-graph family) that builds exactly the one UI material a render-target minimap needs, entirely through the **already-reflected `unreal.MaterialEditingLibrary`** (confirmed in-repo, `mcp_bridge.py:513`):
```python
mat = asset_tools.create_asset(name, path, unreal.Material, unreal.MaterialFactoryNew())
mat.set_editor_property('material_domain', unreal.MaterialDomain.MD_UI)           # UI final-color domain
tex = unreal.MaterialEditingLibrary.create_material_expression(mat, unreal.MaterialExpressionTextureSampleParameter2D, -350, 0)
tex.set_editor_property('parameter_name', 'RT'); tex.set_editor_property('texture', render_target)  # bind the SceneCapture RT
unreal.MaterialEditingLibrary.connect_material_property(tex, 'RGB', unreal.MaterialProperty.MP_EMISSIVE_COLOR)  # Emissive == Final Color in MD_UI
unreal.MaterialEditingLibrary.recompile_material(mat)
```
Then a **MID** off that material (`UMaterialInstanceDynamic::Create` / `unreal.MaterialInstanceDynamic.create`) with its `RT` parameter set to the live `SceneCapture` render target, applied as the minimap `Image` brush's **`ResourceObject`** (`UImage::SetBrushFromMaterial(MID)` at `widget_show` time; the persisted design-time brush references the base `UMaterial`). **Inputs:** `dest`, `render_target` (path), `param_name?` (default `"RT"`), `image_widget?` (minimap `Image` node to apply the MID to). **Returns:** `{material, mid?, applied_to?}`. Registered under `MCPAuthoring` (asset authoring), auxiliary surface (§3.0.2). A **live test** (§5) captures the minimap `Image` at ≥1 resolution and asserts the RT samples through — **non-empty (non-uniform) pixels** in the `Image` rect. This is what promotes the minimap from PARTIAL to ✅ full.

### (e) Runtime wiring

> **Decision — Python-first for show/hide/input-mode/call; C++ only where Slate synthesis or object-graph traversal is unavoidable** (`ui_click`, on-screen enumeration).

**Live-handle registry — enabling fact + lifetime (resolved; fix #4).** `widget_show` stashes the live `UUserWidget` in a **bridge-process module-global dict** keyed by a returned handle. This works *because* `mcp_bridge.py` is **hot-loaded into the editor's `__main__` namespace** (`mcp_bridge.py:1`), so module globals persist across bridge round-trips. **The bridge does NOT `AddToRoot` the widget from Python** — a Python `add_to_root` leaks the object past PIE and hides use-after-free behind a rooted-but-dead reference. Instead lifetime rests on two owners the engine already understands:
1. **While shown, the viewport owns it.** `AddToViewport` parents the `UUserWidget` into the game viewport's widget tree, which keeps it referenced/alive for as long as it is on screen — no manual root needed.
2. **Per-resolve liveness check (the guarantee):** every `widget_set_fields`/`widget_read`/`widget_inspect`/`widget_set_focus`/`widget_call` resolves its handle through an `is_valid`/liveness check first; a dead object returns **`HANDLE_STALE`**, never a resolve into freed memory.
3. **Survive-hide window → a C++ UPROPERTY, not add-to-root.** If a handle must outlive a hide→re-show window (removed from the viewport but reused later), the strong reference is held in a **`UPROPERTY() TMap<FName,TObjectPtr<UUserWidget>> HeldWidgets` on the C++ `UMCPHUDRegistrySubsystem`** — GC-visible, deterministically released on `Deinitialize`, and reachable from both languages — rather than a Python root that GC cannot see through.
4. **PIE-end invalidation (belt-and-suspenders):** on PIE stop the live widgets are destroyed while the dict retains dangling handles, so both the dict and `HeldWidgets` are **cleared on PIE-end** — `UMCPHUDRegistrySubsystem::Deinitialize` (fires on PIE teardown) releases the UPROPERTY refs and signals the bridge to flush; where a Python-reachable `EndPIE` delegate exists it is hooked directly.

**On-screen widget enumeration (fix #6).** `widget_read`/`widget_inspect` today resolve only bridge-created handles, so a project's own `BeginPlay`→`CreateWidget` HUD is invisible. **Add `UMCPHUDRegistrySubsystem::EnumerateViewportWidgets()` (`UFUNCTION` returning `TArray<UUserWidget*>`)** in `MCPCapture` (needs the game world + Slate, so it is C++):
- **Primary (cheap, exact, version-stable):** `for (TObjectIterator<UUserWidget> It; It; ++It) { if (It->IsInViewport() && It->GetWorld()==GameWorld) Out.Add(*It); }` — every viewport-added `UUserWidget` in the PIE game world, regardless of who created it. (In 5.1+ viewport add/remove is managed by `UGameViewportSubsystem`; `UUserWidget::IsInViewport()` remains the stable predicate.)
- **Thorough (optional, catches non-viewport-added):** walk `FSlateApplication::Get().GetInteractiveTopLevelWindows()` / the game viewport's `GEngine->GameViewport->GetGameViewportWidget()` child tree, collecting `SObjectWidget`s and recovering each `UUserWidget` via `SObjectWidget::GetWidgetObject()` — finds widgets pushed via `SetContent`/a widget stack that never set `IsInViewport`.

The bridge calls `EnumerateViewportWidgets()` (via `widget_inspect discover:true` or a handle-less inspect), registers each returned `UUserWidget` into the `__main__` handle dict under a stable handle, and inspects/reads it — so a project's own HUD becomes fully observable without the bridge having created it.

| Tool | Inputs | Impl | Returns |
|---|---|---|---|
| `widget_view` (mode `show`\|`hide`, folds the old `widget_show`/`widget_hide`) | `mode`, `blueprint`/`z?`/`owner?` (show) **or** `handle` (hide) | **Python.** `show`: `unreal.WidgetBlueprintLibrary.create(world, cls, pc)` + `w.add_to_viewport(z)` (the viewport keeps it alive — **no add-to-root**, fix #4); stash under a returned **handle** in the `__main__` dict; if `keep_alive:true`, also register it into `UMCPHUDRegistrySubsystem.HeldWidgets` (C++ UPROPERTY). `hide`: liveness-check → `w.remove_from_parent()`; drop the handle and release any `HeldWidgets` entry | `{handle}` / `{ok}` |
| `widget_set_fields` ⭐ | `handle`, `fields:[{widget_name, field, value}, …]` **or** `calls:[{ufunction, args}]` | **Python:** batched multi-field push in **one round-trip** — each field picks `SetFieldFloat/Text/Int/Bool` by the JSON type of `value`; each `call` is `w.call_method(ufunction, kwargs=args)`. Accumulates `issues:[…]` per entry (partial-failure). **Absorbs the old `widget_set_field` + generic `widget_call`.** This verb physically closes the interim-binding loop — `pie_exec`/`pie_set_property` resolve only *actors* by label (`mcp_bridge.py:364,1939`) and a `UUserWidget` is not an `AActor`. The explicit driver for `SetFieldFloat/…` and `RunNamedCommand`. | `{set:[…], results:[…], issues:[…]}` |
| `set_input_mode` ⭐ | `mode` (`GameOnly`/`GameAndUI`/`UIOnly`), `show_cursor` | **Python:** `WidgetBlueprintLibrary.set_input_mode_*_ex(pc, …)` + `pc.set_editor_property('show_mouse_cursor', show_cursor)` | `{mode}` |
| `widget_set_focus` (fix #11) | `handle`, `widget_name` | **Python:** liveness-check → resolve via `GetWidgetFromName` → `UWidget::SetKeyboardFocus`/`SetUserFocus`. Pairs with a `pie_input` UI-nav-key nudge (`Up`/`Down`/`Accept`) for gamepad/keyboard menu verification | `{focused, widget}` |
| `set_hud_widget` ⭐ | `hud_widget` class, `z?`, `owner?` | **Python + C++ registry** — write the auto-spawn registry (below); optional `assign_subclass` shipping path | `{registered}` |
| `pie_set_source` ⭐ (test-arrange, fix #5) | `target` (actor label), `source_kind` (`property`/`ability_system`), `path?` (dotted component/nested), `attribute?` (GAS), `value` | **Python** for `property` (walk `get_component_by_class`/`get_editor_property` to the penultimate object, then `set_editor_property(leaf, value)`); **Python→optional C++** for `ability_system` | `{target, kind, set, issues:[…]}` |
| `ui_click` ⭐ | `handle`/`widget_name` **or** `[x,y]` viewport coord | **C++ (`UMCPControlSubsystem`)** Slate-pointer pipeline (below) | `{clicked, geometry, target_delta}` |

**Why `pie_set_source` (fix #5).** `pie_set_property` only sets **single-level actor UPROPERTYs** via `a.set_editor_property(...)` (`mcp_bridge.py:1944`), so the pull loop was verifiable only for a bare pawn field. A real health source is often a **component** (`HealthComponent.Health`) or a **GAS attribute**, matching the three `FMCPFieldSourceBinding` cases. `pie_set_source` closes that:
- **`property`** — resolves a dotted path (e.g. `HealthComponent.Health`) on a live game-world actor and sets the leaf. Makes component-held pull sources arrangeable/verifiable.
- **`ability_system`** — `asc = unreal.AbilitySystemBlueprintLibrary.get_ability_system_component(actor)` (a reflected `BlueprintCallable` static), then set the attribute base. **Primary:** `UAbilitySystemComponent::SetNumericAttributeBase(FGameplayAttribute, float)` (a component method, **not** on the BP library); if that isn't reflected into Python, apply an **instant `UGameplayEffect`** via `asc.apply_gameplay_effect_to_self`. **If neither is Python-reachable, drop to a small C++ helper `UMCPControlSubsystem::SetGasAttributeBase(AActor*, FName AttributeName, float)`** — it resolves the ASC via `UAbilitySystemBlueprintLibrary::GetAbilitySystemComponent`, finds the `FGameplayAttribute` by iterating the ASC's spawned attribute sets for an `FProperty` whose name matches `AttributeName` (constructing `FGameplayAttribute(Property)`), then calls `ASC->SetNumericAttributeBase(Attr, Value)`. This is why `MCPCapture` gains the optional guarded `GameplayAbilities` dep (§3.0). Requires `GameplayAbilities` enabled, else `PLUGIN_DISABLED`.
> **Honesty:** with `pie_set_source`, all three pull-source cases (bare property, component, GAS) are arrangeable and verifiable, so the health bar earns "✅ full" (scope table). Absent this, the component/GAS rows would be **downgraded from "full"** with the stated fallback ("expose a `BlueprintPure` getter and use the single-getter binding").

**In-game wiring — a registry, not a convention; PIE/editor-gated and cooked-safe (resolved).** Most projects have no HUD-widget property to assign into. **Ship `UMCPHUDRegistrySubsystem` (`UGameInstanceSubsystem`, in `MCPCapture`)** that maintains a registry of `{widget_class, z_order, owner}` and, on world `BeginPlay`/PIE start, `CreateWidget`s + `AddToViewport`s each entry — independent of any project convention. Content-integrity, symmetric with §3.a:
- **Auto-spawn is gated to editor/PIE only** — `Initialize`/`BeginPlay` bails unless `GIsEditor` **and** `WorldType==PIE`, additionally guarded by an **`mcp.HudAutospawn` cvar defaulting OFF**. Never injects dev-tool HUD spawning into a cooked/shipped game.
- **The registry persists in a cooked-safe config class** — a `UDeveloperSettings`/`Config` object, **not** a file under `Saved/`.
- **Bake-out:** the supported *shipped* path is `assign_subclass` into the project's own HUD property; the registry auto-spawn is an author-time/PIE convenience. `set_hud_widget` writes the registry and keeps `assign_subclass` as the shipping path.

**`ui_click` pipeline (foreground-independent, but NOT window-independent — fix #9).** (1) Resolve the target rect via the **live** `widget_inspect` geometry path (named widget → `GetCachedWidget()->GetCachedGeometry()`, §2.7) in the current viewport. (2) Compute the rect **center**, converting DPI/viewport-offset/window coordinate spaces (coordinate-space handling is a first-class deliverable). (3) **Primary:** obtain the target's `FWidgetPath` via `FSlateApplication::Get().FindPathToWidget(TargetSWidget, WidgetPath)`, construct an `FPointerEvent` at that center, and route via `FSlateApplication::Get().ProcessMouseButtonDownEvent(WindowPtr, PointerEvent)` + `ProcessMouseButtonUpEvent(PointerEvent)`. **Precondition — a REALIZED + PAINTED Slate window must exist.** The synthetic-pointer path needs a real, painted top-level Slate window to host the widget path and receive the event; it does **not** need that window to be *foregrounded* (the win over `include_ui` — a merely backgrounded editor still has a realized, painting window). But a **headless / `-nullrhi` run has no realized Slate window at all**, so a headless-PIE `ui_click` **returns `clicked:false` + a `SLATE_WINDOW_UNAVAILABLE` issue — never a silent no-op.** The target must additionally have been shown and **painted on a prior frame**, `Visible`+`HitTestVisible` under `GameAndUI`/`UIOnly`; `Collapsed`/`SelfHitTestInvisible`/off-screen/`GameOnly` → `clicked:false` + an `issue`. Moving the **OS cursor** is a **foreground-only fallback** (reintroduces the foreground-window dependency and is unavailable headless). This precondition (`SLATE_WINDOW_UNAVAILABLE` under headless; painted-frame otherwise) is stated in the tool description and the §3.i affordance `Summary`. (4) **Echo scope:** `{clicked, geometry, target_delta}` where `target_delta` is the **TARGET widget's own** change only (`pressed`/`checked`/`enabled`/`focus` + geometry). A click's downstream effect is arbitrary, so `ui_click` deliberately does **not** auto-capture a global `post_state`; semantic verification is an explicit follow-up `widget_read`/`pie_verify`.

**`widget_set_focus` + navigation nudge (fix #11).** Menus are frequently driven by **gamepad/keyboard focus navigation**, not the mouse, so mouse-only verification would leave controller-navigable menus unproven. A thin **`widget_set_focus(handle, widget_name)`** op sets Slate focus on a named sub-widget (`SWidget::SetUserFocus` / `UWidget::SetKeyboardFocus` via the resolved live widget), and a **synthetic-navigation nudge reuses the existing simulated-input path** (`pie_input` → `PC->InputKey(FInputKeyEventArgs::CreateSimulated(...))`, `MCPControlSubsystem.cpp:36`) to fire an `Up`/`Down`/`Accept` UI-navigation key so the agent can verify focus moves + the accepted item fires its `Command`. `widget_set_focus` is one net-new name; the nav nudge adds none (it is `pie_input`). Menu verification therefore covers **both** the mouse-click path (`ui_click`) and the focus-navigation path — stated honestly in the scope table.

### (f) OBSERVATION / verification — the closed loop (build this second)

| Tool | Inputs | Impl | Returns |
|---|---|---|---|
| `widget_inspect` ⭐⭐ | `handle?` (or `discover:true` for on-screen enumeration, fix #6), `include_geometry?`, `include_values?` | **Python** (C++ fallback for geometry if `get_cached_geometry` isn't reflected), on an already-painted **live** widget in the **current viewport**: per node emit `{name,class,visible,text/percent/checked/enabled, geometry:{abs,local,dpi,res,valid,source:"live"}}`. **No `resolution` arg** — geometry reflects the actual viewport (fix #2). | canonical tree JSON (runtime variant) |
| `widget_read` ⭐ | `handle`, `widget_name`, `field?` | **Python** — fast single-value read via `GetWidgetFromName` + reflected getter (no `is_variable`); predicate source for `pie_verify` | `{value}` |
| `widget_capture` ⭐ (multi-res oracle + pixels, fix #2) | `blueprint`, `resolution` (single or **list**, e.g. `[[1280,720],[1920,1080],[3840,2160]]`), `geometry?`, `pixels?`, `props?`/state overrides, `warmup_frames?` | **C++ (`MCPAuthoring`)** offscreen `FWidgetRenderer` (below) | `{per_resolution:[{res, dpi, geometry_tree?, image?}]}` |
| *(reuse)* `capture_start include_ui` | — | composited in-game HUD montage | existing montage |
| *(reuse)* `image_compare` | golden gate on `widget_capture`/`include_ui` output | existing | diff score |
| *(reuse)* `pie_verify` | poll `widget_read` until predicate | existing | pass/fail |

**The offscreen renderer — one mechanism for multi-resolution geometry AND pixels AND author-time feedback (fix #2, unified).** `widget_capture` runs in `MCPAuthoring` and, **per requested resolution `(W,H)`**:
1. **Compute the per-size DPI scale explicitly:** `float DPI = GetDefault<UUserInterfaceSettings>()->GetDPIScaleBasedOnSize(FIntPoint(W, H));` — never assume 1.0; the project's DPI curve is applied per size.
2. **Instantiate the widget via the WBP-designer preview path (fix #2), NOT `CreateWidget`.** `CreateWidget` presumes a game world + player context that an offscreen author-time capture does not have. Pin the exact designer-preview instantiation instead: `UWorld* World = GEditor->GetEditorWorldContext().World(); UUserWidget* U = NewObject<UUserWidget>(World, GeneratedClass, NAME_None, RF_Transactional); U->Initialize();` then apply any `props` state overrides and `TSharedRef<SWidget> S = U->TakeWidget();`. (`Initialize()` runs the same `WidgetTree` duplication + slot construction the designer preview uses; `TakeWidget()` builds the underlying Slate.)
3. **Draw into a sized render target:** create a `UTextureRenderTarget2D` of `W×H` (`UKismetRenderingLibrary::CreateRenderTarget2D`); `FWidgetRenderer Renderer(/*bUseGammaCorrection=*/true);` render `warmup_frames` (default 2–3) to warm fonts/materials/streaming, then draw with the DPI applied as the layout scale: `Renderer.DrawWidget(RT, S, /*Scale=*/DPI, /*DrawSize=*/FVector2D(W, H), DeltaTime);`. That overload makes the virtual window's root geometry `absolute==(W,H)` and `local==(W/DPI, H/DPI)` — an exact replica of the live viewport at `(W,H)` under DPI.
4. **Read each named widget's cached geometry from *that sized draw*:** for each `UWidget* Child = U->GetWidgetFromName(Name)`, `TSharedPtr<SWidget> SC = Child->GetCachedWidget(); FGeometry G = SC->GetCachedGeometry();` → `abs = G.GetAbsolutePosition()`, `size = G.GetLocalSize()`/`G.GetAbsoluteSize()`. Layout at **arbitrary resolution, no PIE, no live tick**.
5. If `pixels`, export the RT to PNG (feeds `image_compare`); if `geometry`, return the geometry tree.

**Composite-depth limit — the geometry oracle stops at the composite boundary (fix #12).** `U->GetWidgetFromName(Name)` resolves names in *this* WBP's `WidgetTree` only; it **cannot reach a name inside an embedded composite child** (e.g. `WBP_HUD` capturing `WBP_HealthBar` sees the child as one opaque node — `HealthBar_Fill` inside it is unreachable, exactly as the tree read-back reports it, §3.a). Consequence for verification: the parent capture verifies the **composite NODE's placement** (its slot/geometry inside the parent); the child's **internal layout must be verified by capturing the child WBP standalone** — its own `widget_capture` at **≥2 resolutions**. `widget_capture` states this boundary in its tool description and, when a requested `Name` is unreachable because it lives inside a composite, returns `geometry.valid=false` with `reason:"inside_composite"` rather than a silent miss. The standalone-capture regression is in §5.

**This same offscreen draw is the author-time (no-PIE) layout-feedback path** — the agent composes a tree and immediately gets multi-resolution geometry back without entering PIE. Live `widget_inspect` covers the in-PIE current-viewport case; `widget_capture` covers everything multi-resolution and author-time. **RHI caveat:** the pixel export requires an active RHI/rendering thread (fails under `-nullrhi` → `RHI_UNAVAILABLE`). A **geometry-only** request falls back to a GPU-free **layout-only pass**: `S->SlatePrepass(DPI)` on an `SVirtualWindow` sized to `(W,H)`, then a throwaway paint into a discarded `FSlateWindowElementList` with root `FGeometry::MakeRoot(FVector2D(W,H)/DPI, FSlateLayoutTransform(DPI))` — Paint (not Prepass) is what populates each `SWidget`'s cached geometry, and building draw elements does **not** require the GPU (only *presenting* them does), so author-time geometry works headless. Register in `affordances` with `NeedsAuthoringModule:true` (§3.i) and document the RHI requirement.

> **Reconciliation with the "no re-entrant tick" rule.** `widget_inspect` must not force a Slate tick because it operates on **already-live application Slate** on the single-flight channel, where `FSlateApplication::Tick` would be **re-entrant**. `widget_capture` constructs an **isolated, off-screen `FWidgetRenderer`** and calls `DrawWidget`, which draws+flushes N frames on the editor thread — the same **thumbnail/`UWidgetRenderer` main-thread-synchronous precedent** the engine uses for asset thumbnails. Drawing a private renderer is not re-entering the live application tick.

**`widget_inspect` geometry honesty + precondition.** `GetCachedGeometry()` is valid only *after* a real paint. `widget_inspect` therefore **requires the widget to have been `widget_show`n and painted on a prior frame**; `Collapsed`/off-screen widgets return `geometry.valid=false` rather than forcing a tick. It carries a **painted-frame** precondition (not a foreground-window one on the synthetic path) — documented in the tool spec and the affordance summary (§3.i).

**Resolution-independence is a first-class oracle input (fix #2).** The layout oracle renders at **≥2 resolutions** (e.g. 1280×720 and 3840×2160, plus the 1920×1080 default) via `widget_capture`, asserting anchored elements **stay pinned and inside the SafeZone** at each. A `Fill`/`BottomCenter` HUD correct at 1080p but broken at 720p/4K is caught here, not shipped. **Keep structural (layer 1) + value (layer 2) as the PRIMARY oracle; pixels are the last mile.**

### (g) Error taxonomy + partial-failure contract
Mirror `_issue(code,label,message)` + `errors:[...]` (`blueprint_set_defaults`, `pie_set_property`). Slot/prop ops **do not fail whole** — they set what they can and return `issues:[…]` + `digest`.

**Authoring/compile:** `WIDGET_CLASS_UNRESOLVED`, `NAME_REQUIRED`, `NAME_COLLISION`, `PARENT_NOT_PANEL`, `SLOT_TYPE_MISMATCH`, `IS_VARIABLE_NOT_MATERIALIZED` (warning unless `BindWidget`/`needs_class_exposure`), `BINDWIDGET_UNSATISFIED`, `COMPILE_FAILED` (carries the `FCompilerResultsLog` structured log), `COMPOSITE_CONSTRUCT_FAILED` (`ConstructWidget` returned null for a UserWidget child).

**Binding (author-time, loud):** `BIND_PATH_UNRESOLVED` (`path`/`max_path`/`attribute` fails against the source class; runtime counterpart is the one-shot `SOURCE_NULL`), `BIND_TYPE_MISMATCH` (unmapped conversion pair; carries the compile log), `PLUGIN_DISABLED` (MVVM plugin *or* GAS source/arrange when `GameplayAbilities` is off), `VIEWMODEL_NOT_FOUND`, `WIDGET_NOT_MCPBUTTON` (`widget_bind_event` against a plain `UButton`), `UNKNOWN_COMMAND` (`RunNamedCommand`/`widget_bind_event` against an unmapped command), `MARKER_ANCHOR_INVALID` / `MARKER_ANCHOR_CORRECTED` (`widget_track_actor` marker slot not point-anchored/centered — rejected, or auto-corrected+warned, fix #3), `RATIO_DENOM_ZERO` (runtime one-shot).

**Runtime/observe:** `HANDLE_STALE` (a resolve against a destroyed `UUserWidget`, e.g. across PIE stop), `RHI_UNAVAILABLE` (`widget_capture` pixel export under `-nullrhi`), `SLATE_WINDOW_UNAVAILABLE` (`ui_click` with no realized/painted Slate window, e.g. headless/`-nullrhi` PIE — fix #9).

Every op echoes the affected node name + `digest`; `widget_compile`/`widget_compose` assert compile success and return the log on `COMPILE_FAILED`.

### (h) `widget_digest` — pinned canonical serialization (layer-1 determinism + cross-language, fix #9)
`scene_digest` is deterministic only because it rounds floats and omits fields by convention. Widget slots are float-heavy and trees are order-significant, so the digest is pinned:
- **Per-node field set:** `name`, `class`, `slot_type`, an **allowlisted** slot-prop set, `props`, the **child list**. Nothing else.
- **Slot-prop ALLOWLIST per slot class** (so a UE point-release adding a slot field can't churn every digest):
  - `CanvasPanelSlot`: `anchors`, `offsets`, `alignment`, `z`, `size_to_content`
  - `HorizontalBoxSlot`/`VerticalBoxSlot`: `size` (`auto`/`fill`+value), `padding`, `h_align`, `v_align`
  - `OverlaySlot`/`BorderSlot`: `padding`, `h_align`, `v_align`
  - `GridSlot`/`UniformGridSlot`: `row`, `col`, `row_span`, `col_span`
- **Float quantization:** round anchors/offsets/alignment/opacity to **1e-3** (`round(x,3)`).
- **Child order PRESERVED** — never sorted (a reorder must change the digest, §3.a).
- **FText:** distinguish **literal** vs **binding** (the `text.{kind}` field). Detecting a bound Text is a **C++ `UWidgetBlueprint.Bindings`/MVVM-view walk**, not a plain property read.
- **Runtime/capture-only fields EXCLUDED:** `geometry`, `dpi`, `res`, `valid`, `source`.
- **Cross-language canonicalization (fix #9).** The digest is computed in **exactly one place — Python** — to avoid Go/Python float-format drift (`round(0.75,3)` vs `strconv.FormatFloat`). Python builds the canonical bytes as `json.dumps(node, sort_keys=True, separators=(',',':'))` over the allowlisted, 1e-3-rounded dict (child order intact), then `hashlib.sha256(bytes).hexdigest()`. **Go treats the returned `digest` string as opaque and never recomputes it** — it only compares equality. Any test asserting a digest does so against the Python-produced string. Stated so "implemented verbatim" can't accidentally grow a second hasher.

### (i) Affordance registrations (true gates; fix #7)
Add to `affordances.go` with correct flags. **Split the single `NeedsPlugin` bool into two module signals (fix #7)** — `NeedsAuthoringModule` (the `MCPAuthoring` *Editor* module) and `NeedsCaptureModule` (the `MCPCapture` *Runtime* module) — because the two ship/strip independently (§3.0) and an agent must plan around **which module is compiled in**, not a single "plugin present" bit. To make that plannable, **`editor_ping`/`health_check` enumerate loaded plugin modules + versions** — add a `modules:[{name, type, version, loaded}]` field (probed by resolving `get_editor_subsystem(MCPAuthoringSubsystem)` and the `MCPCapture` subsystems + reading each module's `VersionName`), so the agent (and the §3.0.2 registration gate) knows before calling whether authoring, capture, both, or neither is available. (`widget_inspect`/`ui_click` also carry a **painted-frame precondition** in their `Summary`; `ui_click`'s synthetic path needs no *foreground* window but does need a realized+painted Slate window — headless → `SLATE_WINDOW_UNAVAILABLE`, fix #9.)

| Tool | NeedsEditor | NeedsPIE | NeedsAuthoringModule | NeedsCaptureModule | Mutates |
|---|---|---|---|---|---|
| `widget_compose`/`widget_compile`/`widget_create`/`widget_tree` | ✓ | | ✓ (composite/compile) | | ✓ (except `tree get`) |
| `widget_describe` | ✓ | | ✓ (BindWidget/handler enum) | | |
| `widget_bind_field`/`widget_track_actor`/`widget_bind_event` | ✓ | | ✓ (compile) | ✓ (reads `UMCPHUDWidget` config at runtime; `ability_system` also needs `GameplayAbilities`) | ✓ |
| `widget_viewmodel_create`/`widget_bind_mvvm` | ✓ | | ✓ (MVVM plugin-enabled) | | ✓ |
| `widget_capture`/`widget_make_rt_material` | ✓ | | ✓ (`widget_capture` pixels need **RHI**; geometry-only headless-ok) | | ✓ (`make_rt_material`) |
| `widget_view`/`widget_set_fields`/`widget_set_focus`/`set_input_mode`/`pie_set_source` | ✓ | ✓ | | ✓ (`pie_set_source` GAS: `GameplayAbilities`) | ✓ (except reads) |
| `set_hud_widget` | ✓ | | | ✓ (registry subsystem) | ✓ |
| `ui_click` | ✓ | ✓ | | ✓ (MCPControl Slate ext; realized Slate window) | ✓ |
| `widget_inspect`/`widget_read` | ✓ | ✓ | | (`discover:true` on-screen enumeration needs MCPCapture) | |

### Phasing (biggest unlock first; each phase carries a sizing line — fix #8)

**Spike 0 — reflection-reachability probe (the FIRST gating deliverable; collapses the bimodal Phase 0).** Before any op is promoted, run one `execute_python` probe against a scratch WBP and **record the outcome in the plan**:
- `unreal.new_object(TextBlock, outer=widget_tree, name=…)` → **expected: reflects** (primitive `UWidget`).
- `widget_tree.set_editor_property('root_widget', root)` → **expected: reflects** (bare `UPROPERTY`).
- `child.set_editor_property('is_variable', True)` → **expected: reflects** (`bIsVariable` is a `UPROPERTY`); verify it **materializes** as a generated-class var after compile.
- `panel.add_child(child)` universal adder → **expected: reflects**.
- reorder primitive (`shift_child`/`insert_child_at`) → **expected: NOT reflected** → commit to the **remove+reinsert** fallback (§3.a), not the bimodal "if/else".
- composite `ConstructWidget` from Python → **expected: NOT reachable** → composite always C++ (§3.a).
- Confirm `get_cached_geometry` is `BlueprintCallable` in 5.7 → if not, geometry extraction is C++ (§3.f).
- **In parallel: the MVVM spike (both surfaces, §3.c)** runs timeboxed with a recorded go/no-go.

The plan **commits to the expected branch** (Python-first primitives + C++ composite/compile + remove-reinsert reorder); any surprise flips only that one op to its stated fallback. *Sizing: ~0.5–1 eng-wk; ~150 Python LOC (throwaway probe) + the MVVM spike; 0 shipped C++.*

**Phase 0 — Author (foundation), split by C++ dependency (fix #8).**

- **Phase 0a — flat primitive authoring, Python-only (minimal/no shipped C++).** Prove **ONE non-composite WBP round-trip** end-to-end with **no new C++ module**: Python primitive construction (`new_object` + universal `add_child` + slot/prop application per Spike 0), an interim Python compile (`compile_blueprint`, unstructured pass/fail — the structured log arrives in 0b), `widget_tree` (`get`), mandatory node naming, and snapshot/restore for destructive ops. Deliverable: `widget_compose` (flat trees) + `widget_tree get`/`restore` + upgraded `widget_create` (default `UUserWidget`) against a scratch WBP. This de-risks the reflection path before any C++ is written. *Sizing: ~1–1.5 eng-wk; ~450 Python LOC; 0 shipped C++.*
- **Phase 0b — the `MCPAuthoring` C++ module.** Add the `Editor` module (`UMCPAuthoringSubsystem`) + `UMCPHUDWidget`/`UMCPButton` bases (opt-in) in `MCPCapture`; **composite `ConstructWidget` dispatch** (`AddChildWidget`), the **structured `FCompilerResultsLog` compile** (`widget_compile` = `FKismetEditorUtilities::CompileBlueprint`, replacing 0a's interim Python compile), and **`BindWidget`/handler enumeration** (`widget_describe`). Folds composite `widget_compose` and the reorder/reparent primitive locked per Spike 0.
  - **Shippability / build-integration deliverable (fix #6) — part of 0b's Definition of Done:** (a) **add the `MCPAuthoring` entry to `UnrealMCP.uplugin`** (`"Type": "Editor"` per the §3.0 decision — not `UncookedOnly`, so it spawns under `GEditor`; `"LoadingPhase": "Default"`), alongside the existing `MCPCapture` `Runtime`/`Default` entry; (b) **confirm the mixed Runtime+Editor module set loads in an editor process** and `unreal.get_editor_subsystem(unreal.MCPAuthoringSubsystem)` **resolves** (surfaced by the `editor_ping`/`health_check` module enumeration, §3.i); (c) **assert the `Editor` module strips cleanly from a cooked/shipping build** — a packaged `Shipping` target builds and runs with `MCPAuthoring` absent and no unresolved reference (author-time-only ops must never enter a game build).
  - *Sizing: ~1.5–2 eng-wk; ~250 Python LOC (composite routing, structured-compile wiring) + ~900 C++ LOC (`MCPAuthoring` scaffold, `AddChildWidget`/`ConstructWidget`, `widget_compile`, BindWidget+handler enumeration; `UMCPHUDWidget`/`UMCPButton` shells) + the `.uplugin`/build-target integration.*

**Phase 1 — See + Drive + interim Bind (closes the loop, self-contained).** `widget_show`/`widget_hide`, `widget_set_fields` (Python); `widget_inspect`, `widget_read`, `widget_capture` (offscreen multi-res geometry+pixels), wired into `image_compare`/`pie_verify`; the `UMCPHUDWidget` interim binding — push (`SetFieldFloat/…`) + configured pull (`widget_bind_field` for health/ammo incl. ratio + GAS + FormatText; `widget_track_actor` for the marker); `EnumerateViewportWidgets` (fix #6); `pie_set_source` (fix #5). **Health=75%⇒bar demonstrated end-to-end within this phase:** `widget_show` → `widget_bind_field(Health,MaxHealth,ratio)` → `pie_set_source`/possession toggle → `widget_read`/`widget_capture` verify `Percent≈0.75` at ≥2 resolutions. *Nothing depends on a later phase. Sizing: ~2.5 eng-wk; ~450 Python LOC (show/hide/set_fields/inspect/read/pie_set_source) + ~900 C++ LOC (`widget_capture` `FWidgetRenderer`+DPI+geometry read, `UMCPHUDWidget::NativeTick` FieldSource/WorldTrack eval, `EnumerateViewportWidgets`).*

**Phase 2 — Wire/Operate (menus + in-game).** `set_input_mode` (Python), `set_hud_widget` + PIE/editor-gated `UMCPHUDRegistrySubsystem`, `widget_bind_event` (`UMCPButton.Command`), `ui_click` (Slate C++). Turns an authored menu operable (pinned handler bodies, §3.0.1) and makes an authored HUD auto-appear in the running game. Scope already settled by Spike 0's MVVM go/no-go. *Sizing: ~1.5 eng-wk; ~250 Python LOC + ~550 C++ LOC (`ui_click` `ProcessMouseButtonDownEvent/UpEvent`, `UMCPHUDRegistrySubsystem` auto-spawn+cvar gate, optional `SetGasAttributeBase`).*

**Phase 3 — Declarative Bind (non-blocking, spike-gated).** `widget_viewmodel_create` + `widget_bind_mvvm`. Runs only if the Spike-0 MVVM go/no-go cleared (both surfaces); otherwise the Phase-1 `UMCPHUDWidget` push+pull path stands as the supported binding story. *Sizing: ~2–3 eng-wk (~600 C++ LOC) IF green; else 0.*

**Phase 4 — Polish.** `widget_set_brush`/`widget_set_font` advanced bodies, Slate style assets, `UWidgetAnimation`, and the **committed `widget_make_rt_material`** op (reflected `MaterialEditingLibrary`, `MD_UI` RT sampler → MID → minimap `Image` brush; closes the minimap to ✅ full, fix #1). *Sizing: ~1.5 eng-wk; ~350 Python LOC.*

### Canonical-element scope (honest bar)

> **Decision — all 5 fully buildable and *gameplay-bound* via the opt-in `UMCPHUDWidget` base. The minimap is now full (fix #1): the RT-sampling UI material is committed as the bounded `widget_make_rt_material` op (§3.d), not deferred.**

| Element | Buildable here? | Mechanism |
|---|---|---|
| Health bar | ✅ full | `ProgressBar.Percent` via **`widget_bind_field`** with the **ratio** binding (`Path="Health"`, `MaxPath="MaxHealth"`, `Conversion=ratio`) — separate `Health`+`MaxHealth` (the previously-broken flagship case), a single 0..1 getter, or a **GAS** source (§3.0.1). Component/GAS sources are **arrangeable+verifiable** via `pie_set_source` (fix #5). A configured, gameplay-tracking bar. Requires deriving from `UMCPHUDWidget` (opt-in; bake out before shipping). |
| Ammo counter | ✅ full | `TextBlock` + **`widget_bind_field`** with **`FormatText`** conversion (`Format="{value} / {max}"` → `FText::Format`, fix #9); or bare `int_to_text`; or MVVM `IntToText` (§3.c). |
| **Composite HUD** (fix #1) | ✅ full | A WBP embedding a separately-authored WBP (`WBP_HUD` embeds `WBP_HealthBar`) via the C++ `ConstructWidget` composite path (§3.a); the embedded child is one named node, read back by `widget_tree_get` (regression test in §5). The dominant shipped pattern, now first-class. |
| Menu | ✅ full | `UMCPButton` + **`widget_bind_event`** (`Command` → `RunNamedCommand`, fix #3) + `set_input_mode(UIOnly)`; handler set discovered via `widget_describe`, with **pinned default bodies** (Resume/Quit/OpenPanel) each producing an observable delta verified in §5. **Verified on both input paths (fix #11):** mouse via `ui_click`, and gamepad/keyboard via **`widget_set_focus`** + a `pie_input` UI-nav-key nudge (`Up`/`Down`/`Accept`) — controller-nav is included, not silently excluded. |
| Objective marker | ✅ full | **`widget_track_actor`** (`WorldTrackBindings`) per-tick **`UWidgetLayoutLibrary::ProjectWorldLocationToWidgetPosition`** (DPI-correct, fix #4) — a configured property, not authored graph. |
| **Minimap** | ✅ **full** (fix #1) | The `TextureRenderTarget2D`, the `SceneCapture2D` actor (`spawn_actor`, verified in-repo), and assigning the RT as its capture target (`pie_set_property`/`blueprint_set_defaults`) are reachable today; the previously-missing last mile — a `MD_UI` `UMaterial` sampling the RT via a `TextureSampleParameter2D` into Emissive/Final Color, an MID, and the MID on the minimap `Image` brush `ResourceObject` — is the committed **`widget_make_rt_material`** op (§3.d), built entirely through the reflected `unreal.MaterialEditingLibrary`. Live test asserts non-empty RT pixels in the minimap `Image` (§5). |

---

## 4. HARD PROBLEMS + RISKS (crisp register — mechanisms are in §3, not re-derived here)

1. **WidgetBlueprint tree authoring from Python.** `WidgetTree.construct_widget` is not reflected; bare-`UPROPERTY` setters (`is_variable`, `root_widget`), the reorder primitive, and composite UserWidget children are the reflection risks. **De-risk:** Spike 0 first; commit to the expected branch (Python primitives + C++ composite/compile + remove-reinsert reorder); a surprise flips one op to its C++ fallback — the foundation degrades op-by-op, never collapses (§3.a).

2. **Compile / hot-reload of widgets.** After tree edits you must compile + dirty + `save_asset`; live instances do not auto-refresh, and the compile regenerates the generated-class tree + `is_variable` vars. **De-risk:** atomic `widget_compose`; a `patch`/`defer` transaction terminated by the C++ `widget_compile` (`FKismetEditorUtilities::CompileBlueprint` + `FCompilerResultsLog`); `widget_tree_get` reads the in-memory pre-compile tree; runtime ops **re-`CreateWidget`** rather than mutate a stale instance (§3.a, §3.e).

3. **Seeing sub-widget geometry, resolution-independently (fix #2).** `GetCachedGeometry()` reflects the actual painted viewport — a live `resolution` arg is a lie; and you can't force a re-entrant Slate tick on the single-flight channel. **De-risk:** live `widget_inspect` reports current-viewport only (drops `resolution`, `geometry.valid=false` rather than ticking); all multi-resolution truth comes from the offscreen `FWidgetRenderer` prepass in `widget_capture` with `GetDPIScaleBasedOnSize(FIntPoint)` applied as `Scale`; geometry-only is headless via a layout-only paint (§3.f).

4. **Binding without a human wiring the graph.** The deepest hole — de-risked by making the `UMCPHUDWidget` reflective path the default and MVVM a spike-gated bonus. The pull path is fully pinned: path grammar (dotted, depth 4, FProperty-then-getter), ratio/derived, GAS + FormatText sources, author-time validation (`BIND_PATH_UNRESOLVED`), handle caching with possession recovery, one-shot `SOURCE_NULL`/hold-last-good. Component/GAS arrangeable via `pie_set_source` (fix #5). Menu clicks route through the persisted `UMCPButton.Command` consumed by `RebuildWidget`'s runtime `AddDynamic` — no serialized delegate, no graph node — with unambiguous per-button dispatch (fix #3). Writes target the **per-asset generated-class CDO**. Binding can never sink the effort (§3.0.1, §3.c).

5. **DPI / anchor correctness — verified across resolutions.** The anchors×alignment×offsets trap, the `CanvasPanelSlot` offset duality (point ⇒ `[posX,posY,sizeX,sizeY]`; stretch ⇒ edge margins), and DPI scaling make naive placement wrong-by-default; the objective marker must use `ProjectWorldLocationToWidgetPosition` (widget-space, DPI-scaled), not `ProjectWorldLocationToScreen` (raw pixels, drifts under DPI≠1 — fix #4). **De-risk:** anchor presets; compose echoes the resolved `offset_meaning`; render the on-screen rect via `widget_capture` at ≥2 resolutions (1280×720 + 3840×2160, plus 1920×1080) and assert anchored elements stay pinned + inside the SafeZone (§3.b, §3.f).

6. **Verifying a HUD is 'right'.** Pixels alone are brittle. Four-layer oracle, cheapest-that-can-fail first: (1) structural digest (§3.h, zero rendering); (2) value read-back (`widget_read`, by name, no `is_variable`); (3) geometry, multi-resolution (`widget_capture`); (4) perceptual golden (`image_compare`, RHI-bound). Assert on the cheapest layer; reserve pixels for the last mile. Text is authored/read as **`FText`** with literal-vs-binding surfaced structurally.

---

## 5. TEST & FIXTURE STRATEGY (per phase — graded on testability)

Mirror the repo's test culture (`authoring2_tools_test.go`, `parity_test.go`, `live_test.go` behind `//go:build live`, `v7_tools_test.go`):

- **Go table tests** for every new tool's arg→op mapping and registration (extend `authoring2_tools_test.go` / a new `hud_tools_test.go`), asserting each appears in `listToolNames` and `structHandler` builds the expected op payload — no editor required.
- **Test isolation.** Every live scratch-WBP recipe **creates its own uniquely-named WBP under a per-test temp path** (`/Game/__MCP_Test/<testname>_<nonce>`), and `t.Cleanup` **deletes the asset, `widget_hide`s every live handle (releasing any `UMCPHUDRegistrySubsystem.HeldWidgets` UPROPERTY ref — no Python roots to undo, fix #4), clears its entries from the `__main__` handle dict and the HUD auto-spawn registry, and resets input mode to `GameOnly`.** The module-global handle dict and the (PIE-gated) HUD registry are the shared mutable state; tests assert their own teardown left both empty.
- **Scratch-WBP integration recipes behind `//go:build live`** (`UMCP_PROJECT_DIR`-selected), each locked as a regression test so a UE point-release breaking reflection is caught:
  - construct → **reorder/reparent** → compile → save → reopen → re-read;
  - **reorder slot-preservation (task fix #10):** reorder a node carrying **non-default anchors/offsets**, then assert those offsets **survive in the post-compose `digest`** — proving slot props are re-applied *after* the move that mints a fresh `UPanelSlot` (§3.a);
  - **composite (fix #1):** `widget_compose` a `WBP_HUD` **embedding a separately-authored `WBP_HealthBar`** via the `ConstructWidget` path; `widget_tree_get` reads back a node whose `class` is the `WBP_HealthBar` generated class, as one opaque named node;
  - **composite geometry depth (task fix #12):** `widget_capture` the parent verifies the composite **node's** placement; the child WBP is captured **standalone at ≥2 resolutions** to verify its internal layout, and a parent-side request for a name inside the composite returns `geometry.valid=false, reason:"inside_composite"` (§3.f);
  - **push + live-pixel (stale-Slate guard):** `widget_show` → `widget_set_fields SetFieldFloat` ⇒ `widget_read Percent` **AND** a **live-pixel/visible-change assertion** — a `widget_capture include_ui` pixel delta on the bar rect (or a `widget_inspect`-observed on-screen change) across the `SetField` — so a value-oracle-only pass on a **frozen** Slate widget (`UPROPERTY` updated, `SWidget` not refreshed) cannot false-pass; proves the apply ran `SynchronizeProperties()`/typed-setter, not a bare `FProperty` write;
  - **ratio pull (flagship):** `widget_bind_field(source=owning_pawn, path=Health, max_path=MaxHealth, target=Percent, conversion=ratio)` ⇒ per-tick pull tracks a **changing** pawn value to `Percent≈0.75` — asserted by the value oracle **and** a **live-pixel delta** on the bar rect (`widget_capture include_ui`) across the change, so the `NativeTick` apply is proven to reach Slate (not just the `UPROPERTY`) and stale-Slate cannot false-pass;
  - **component + GAS pull (fix #5):** arrange via `pie_set_source(property, path="HealthComponent.Health")` and `pie_set_source(ability_system, attribute="Health")` and assert the same `Percent≈0.75`;
  - **ammo FormatText (fix #9):** `conversion=format_text, format="{value} / {max}"` ⇒ `widget_read Text == "24 / 30"`;
  - **possession recovery:** toggle **unpossessed→possessed**, assert `SOURCE_NULL`/hold-last-good while null, then **recovers**;
  - **objective marker DPI (fix #4):** `widget_track_actor`, then assert the marker's widget-space position matches `ProjectWorldLocationToWidgetPosition` (not raw screen pixels) at DPI≠1;
  - **marker anchor forcing (task fix #3):** `widget_track_actor` sets the marker slot to anchors `(0,0)-(0,0)` + alignment `(0.5,0.5)`; assert a supplied **non-zero anchor** is auto-corrected with `MARKER_ANCHOR_CORRECTED` (or rejected with `MARKER_ANCHOR_INVALID`), and that the tracked marker stays **centered** on the projected point;
  - **minimap RT material (task fix #1):** `widget_make_rt_material` → `widget_view show` the minimap → `widget_capture` the minimap `Image` rect and assert **non-empty (non-uniform) pixels**, proving the `MD_UI` material samples the SceneCapture RT through the MID brush;
  - `ui_click` ⇒ `target_delta` on the clicked widget; **and the headless negative (task fix #9):** under `-nullrhi`/headless PIE, `ui_click` returns `clicked:false` + `SLATE_WINDOW_UNAVAILABLE`, never a silent no-op.
- **On-screen enumeration test (fix #6):** in PIE, a project-side `BeginPlay`→`CreateWidget` HUD (not created by the bridge) is discovered by `widget_inspect discover:true` (`EnumerateViewportWidgets`) and read back by `widget_read`.
- **CDO-independence test:** author **two** WBPs deriving `UMCPHUDWidget`, bind different fields on each, assert `FieldSourceBindings` independence (no base-CDO cross-talk).
- **Convergence / idempotence test:** start from a **deliberately permuted + reparented** tree, run `widget_compose`, assert the resulting `widget_digest` equals the target; re-run and assert an **identical** digest.
- **Cross-resolution layout test (fix #2/#5):** `widget_capture` at **≥2 resolutions** (1280×720 and 3840×2160, plus 1920×1080) and assert anchored elements stay **pinned and inside the SafeZone** at each — via the offscreen renderer, not the live viewport.
- **Digest cross-language test (fix #9):** assert the Go side treats `digest` as opaque (equality only) and never recomputes it; the canonical bytes come from the Python `sha256(json.dumps(sort_keys=True,separators=(',',':')))` path.
- **Destructive-safety test:** `widget_compose prune:true` (or `remove:`) emits a `restore_token`; `widget_tree_restore` reproduces the pre-mutation `digest`.
- **Menu behavior test (fix #3):** bind Resume/Quit/OpenPanel via `widget_bind_event` on `UMCPButton` nodes, `ui_click` each, and assert the **observed state delta** — Resume removes-from-viewport + input mode `GameOnly` + cursor hidden; OpenPanel changes the `WidgetSwitcher` active index; plus the **negative** cases: an unmapped command surfaces `UNKNOWN_COMMAND`, and `widget_bind_event` on a plain `UButton` surfaces `WIDGET_NOT_MCPBUTTON`.
- **Menu focus/navigation test (task fix #11):** `widget_set_focus` a menu button, fire a `pie_input` UI-nav-key nudge (`Down` then `Accept`), and assert focus moved to the next button and the accepted button fired its `Command` — proving controller/keyboard-nav menus verify, not just mouse `ui_click`.
- **Golden PNG fixtures** for `widget_capture`, gated via `image_compare` with tolerance, warmed N frames at fixed DPI per resolution; skipped automatically when `RHI_UNAVAILABLE`.
- **BindWidget negative test:** a custom-parent WBP missing a required bound widget fails `BINDWIDGET_UNSATISFIED`/`COMPILE_FAILED` with the compile log (from the C++ `FCompilerResultsLog`), not a silent stub.
- **Content-integrity test:** the `UMCPHUDRegistrySubsystem` auto-spawn is a **no-op outside editor/PIE** (cvar off / non-PIE world), and its registry lives in the cooked-safe config class, not `Saved/`.

---

### Bottom line
Today's surface — `widget_create`'s stubbed "needs the C++ plugin" note plus pixels-only `include_ui` and gameplay-stack-only `pie_input` — leaves an agent unable to build, read, or operate a HUD: **grade F.** The unlock is a real authoring family in a correctly-typed new `MCPAuthoring` **`Editor`** module (`UMCPAuthoringSubsystem`, via `get_editor_subsystem`), added to `UnrealMCP.uplugin` and proven to load-and-resolve in-editor and strip cleanly from a Shipping cook. It fronts an **honestly-counted surface — ~20 net-new distinct tools (100 → ~120 server total), a 15-verb primary core after folding paired verbs (`widget_view{show|hide}`, `widget_tree{get|restore}`) — registered only when the authoring/capture modules are detected** (`editor_ping`/`health_check` enumerate loaded modules + versions), with `widget_compose` absorbing every fine-grained edit. Composite WBP-in-WBP HUDs are first-class via C++ `UWidgetTree::ConstructWidget`. The observe loop is anchored by an **offscreen `FWidgetRenderer` oracle** — the widget instantiated via the designer-preview path (`NewObject`+`Initialize`+`TakeWidget`, not `CreateWidget`) — that reads cached geometry from a sized draw with `GetDPIScaleBasedOnSize(FIntPoint)` applied per resolution, while live `widget_inspect` honestly reports only the current viewport and composite internals are verified by capturing the child WBP standalone. The `UMCPHUDWidget` base carries explicit-target push setters (`SetFieldFloat(Widget, Field, Value)`) and a fully-pinned per-tick gameplay pull (path grammar, ratio/GAS/FormatText sources, author-time validation, handle caching with possession recovery, hold-last-good), with component/GAS sources made verifiable by `pie_set_source`. Menu clicks dispatch unambiguously through a persisted `UMCPButton.Command`, and controller/keyboard-nav menus verify via `widget_set_focus` + a `pie_input` nav nudge; the objective marker is DPI-correct via `ProjectWorldLocationToWidgetPosition` with a **forced point-anchor + centered pivot**; a project's own `CreateWidget` HUD is discoverable via `EnumerateViewportWidgets`. Bindings write the **per-asset generated-class CDO**, `widget_compose` **converges from a divergent tree** (slot props re-applied after every move), live handles survive on the **viewport reference + a C++ UPROPERTY — never a Python add-to-root** — liveness-checked and PIE-invalidated, the HUD registry is **PIE-gated and cooked-safe**, `widget_digest` is **canonicalized Python-side only**, destructive ops snapshot+restore, `ui_click` is honest about needing a realized Slate window (`SLATE_WINDOW_UNAVAILABLE` headless), and the reflection spike + MVVM go/no-go run **first** with recorded expected outcomes and per-phase sizing across a de-risked **Phase 0a (Python-only flat authoring) → 0b (the `MCPAuthoring` C++ module + shippability gate)**. `widget_create` defaults to plain `UUserWidget` with a documented bake-out, and the undo/transaction footgun is stated. Standardize on the reflective `UMCPHUDWidget` path and treat MVVM as a spike-gated upgrade — so binding, the deepest hole, can never sink the plan. With the minimap's RT-sampling UI material committed as the bounded `widget_make_rt_material` op (reflected `MaterialEditingLibrary`, `MD_UI`), **all five canonical elements are fully buildable**. Built on the solid capture/montage/`image_compare`/`pie_verify`/`reflect_class`/`_issue`/`scene_snapshot`/`affordances` substrate, this makes a real HUD — including the composite pattern — **buildable, wireable into the running game, and verifiable at four layers across resolutions.**
