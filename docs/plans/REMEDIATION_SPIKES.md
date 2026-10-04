# Remediation spikes (R0.2, R0.3) — 2026-10-03

Live UE 5.7 checks behind [`REMEDIATION_PLAN.md`](REMEDIATION_PLAN.md): each call the plan depended on was run once
in a scratch copy of aesir-wave-defense (own multicast group, own git repo) through the server's `python` tool. Every
later item cites the row it relies on. "Plugin" means a `UMCPCoreLibrary` (or later) helper — never a silent Python
fallback.

## R0.2 — feasibility per call

| # | Call | Python in UE 5.7 | Decision | Evidence |
|---|---|---|---|---|
| 1 | Undo/redo transaction **title** | none: no `TransBuffer`/`Transactor`; `SystemLibrary` has only `begin/end/cancel_transaction`, `transact_object` | **plugin** `PeekUndoTitle`/`PeekRedoTitle`, `UndoIfTitled`/`RedoIfTitled` (API 3) | spike1; plugin verified live: spawn → `undo` → actor gone → `redo` → back; a human transaction on top → `CONFLICT`, unchanged; PIE → `PRECONDITION` |
| 2 | A function's `FUNC_BlueprintPure` / `FUNC_Const` | the `Function` object loads (`load_object(None, "/Script/Engine.GameplayStatics:GetPlayerPawn")`) but exposes no flags | **plugin** `IsPureOrConst(Class, Name)` (API 3) | verified: `GetPlayerPawn` → true (pure), `K2_GetActorLocation` → true (const), `K2_DestroyActor` → false, unknown name → false |
| 3 | World / GameInstance / LocalPlayer **subsystem** lookup | none: no `SubsystemBlueprintLibrary`; `GameInstance`/`World` expose no `get_subsystem` (only `get_editor_subsystem`, `get_engine_subsystem`) | **plugin** `FindGameSubsystem(WorldContext, Class)` (API 3) | verified in PIE: `MCPControlSubsystem` (GameInstance) and `EnhancedInputLocalPlayerSubsystem` (LocalPlayer) found; an editor subsystem → None (by design: R1.1 keeps editor subsystems to `reflect`) |
| 4 | `BlueprintCallable` UFUNCTION with `int64` / `FString` | works: `MathLibrary.add_int64_int64(2**40, 5)` = 1099511627781; non-ASCII `FString` round-trips | **Python** | spike1/2 |
| 5 | Curve keys (`UCurveFloat::FloatCurve`) | no key read/edit: `float_curve` is not an exposed property; `CurveFloat` offers only `get_float_value`, `get_time_range`, `get_value_range`. **Writing** all keys works through a CSV re-import (`CSVImportFactory`, `ECSV_CURVE_FLOAT`, `replace_existing`; rows are `time,value`, **no header** — a header row becomes a key) | **plugin** for reading keys (R3 `GetCurveKeys`); writing may use the CSV re-import | spike2; G.3 (`C_DamageFalloff`) |
| 6 | Blueprint **describe** (`NewVariables`, components, graphs, parent, status) | none: `new_variables`, `simple_construction_script`, `function_graphs`, `ubergraph_pages`, `parent_class`, `generated_class` not found; `status` protected | **plugin** (R3: `DescribeBlueprint`) | spike2 on `BP_UndeadDraugrGameMode` |
| 7 | Blueprint **add variable** | `BlueprintEditorLibrary.add_member_variable` (+ `set_blueprint_variable_instance_editable`, `…expose_on_spawn`) | **Python** | spike1 |
| 8 | `InputMappingContext` edit | `map_key`, `unmap_key`, `unmap_all_keys_from_action`, `mappings` exist; `InputAction` class exposed | **Python** (R3.6) | spike1 |
| 9 | DataTable row add / remove | `DataTableFunctionLibrary.remove_data_table_row`, `get_data_table_row_names`, `export_data_table_to_json_string`, `fill_data_table_from_json_string`; no single-row add | **Python**: keyed upsert = export JSON → change one row → fill (all other rows re-written unchanged); delete = `remove_data_table_row` | spike1 |
| 10 | Actor spawn into the **PIE** world | none: only `EditorActorSubsystem.spawn_actor_from_class` (editor world); `GameplayStatics` has no actor spawn; `World` exposes none | **plugin** (R2.5: `UMCPControlSubsystem::SpawnInGame`, API 5) | spike1 |
| 11 | HighResShot vs UMG | **corrected in G.5**: HighResShot during PIE does **not** include UMG viewport widgets. The R0.2 screenshot showed Aesir's HUD, but that HUD is canvas-drawn (`AAesirHUD::DrawHUD`); a UMG widget mounted in PIE (live tree: visible, text `1`) is absent from the same HighResShot | **plugin**: R4.3 stands as first planned — `screenshot op=pie ui=true` through `MCPCaptureSubsystem` `include_ui` (`FSlateApplication::TakeScreenshot`) | G.5 live run (`WBP_WaveReadout3`) |

## R0.3 — gameplay event sources (Aesir)

| Event | Engine hook in Aesir? | Source for R5.1 | Evidence |
|---|---|---|---|
| damage dealt / taken | yes: every `TakeDamage` override (`AesirCharacter.cpp:627`, `EnemyCharacter.cpp:352`, `Buildable.cpp:36`, `MissionActors.cpp:143`) calls `Super::TakeDamage` after its own early-outs (dead, friendly fire, preview — damage that never lands), so `OnTakeAnyDamage`/`OnTakePointDamage` broadcast | **plugin recorder** (C++ binding) | `apply_damage(pawn, 7)` returned 7.0 (the `Super` path ran). Python cannot bind the delegates: `on_take_any_damage.add_callable(fn)` failed ("error return without exception set") — confirms the recorder must be C++ |
| actor spawned / destroyed | engine: world `OnActorSpawned` is native (not reachable from Python); actor `OnDestroyed` is dynamic but Python binding fails as above | **plugin recorder** | spike_events: `World` exposes no spawn hook |
| death / kill | no engine event: `Die()` is game code (`AesirCharacter.cpp:672`, `EnemyCharacter.cpp:370`, `MissionActors.cpp:167`) | **game journal** (G.2 `UAesirAgentSubsystem`) | source |
| VFX / SFX / camera shake | no global hook: direct `PlaySoundAtLocation` (`AesirWeapon.cpp:250`), `ClientStartCameraShake` (`AesirCharacter.cpp:393`) | **game journal** (G.2 cues) | source |
| player input | the plugin injects it | **plugin** | — |

Consequence for the plan: unchanged in shape. R5.1's recorder is a plugin C++ component (as planned); the plugin API
for R3 needs its own bump (curve keys, Blueprint describe) — the §8 table gains an R3 row.

## Found during phase G (live, scratch copies)

| Finding | Consequence |
|---|---|
| UE 5.7 hides `UWidgetBlueprint.WidgetTree`, `UWidgetTree.RootWidget` and `UWidget.bIsVariable` from Python; `asset_create kind=widget_blueprint` and `widget_edit op=compose` failed ("Failed to find property 'widget_tree'") | plugin API 4: `GetWidgetTree`, `Get/SetRootWidget`, `SetWidgetIsVariable`; the companion uses them when the property is hidden |
| A widget made with `unreal.new_object` is not registered with its Blueprint: the compiler ensures "Widget was added but did not get a GUID" | plugin `RegisterWidget` (`UWidgetBlueprint::OnVariableAdded`), called for every created widget |
| `CreateWidget` / `WidgetBlueprintLibrary.create` is not exposed (Python's `WidgetLibrary` has no `create`); `UUserWidget.get_widget_from_name` is not exposed | plugin `UMCPControlSubsystem::MountWidget` / `UnmountWidget`, `DescribeLiveWidgets` (the R4.2 live tree, early) |
| `UMCPHUDWidget` bindings (`FieldSourceBindings`) are invisible to Python (plain `UPROPERTY()`) | plugin `Get/SetClassDefaultJson` (generic, `FJsonObjectConverter`) |
| `unreal.Text.from_string` does not exist in 5.7 | `unreal.Text(str)` |
| `InputMappingContext.mappings` is deprecated; mappings live in `default_key_mappings.mappings` | R3.6 reads and writes there |
| A compose `slot.position` is not applied (the text sat at 0,0) | R4 fix |
