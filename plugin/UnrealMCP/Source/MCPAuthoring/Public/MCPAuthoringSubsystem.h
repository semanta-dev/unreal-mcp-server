// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "EditorSubsystem.h"
#include "MCPAuthoringSubsystem.generated.h"

class UWidgetBlueprint;
class UWidgetTree;
class UWidget;

/**
 * UMCPAuthoringSubsystem — the editor-only UMG authoring surface the bridge reaches
 * via `unreal.get_editor_subsystem(unreal.MCPAuthoringSubsystem)`. It provides exactly
 * the operations Python reflection cannot: composite (UserWidget) child construction
 * (UWidgetTree::ConstructWidget is a non-reflected template), a structured
 * FCompilerResultsLog compile, and BindWidget enumeration. See HUD_TOOLING_PLAN.md
 * §3.0/§3.a. All methods are pure author-time; the module strips from cooked builds.
 */
UCLASS()
class MCPAUTHORING_API UMCPAuthoringSubsystem : public UEditorSubsystem
{
	GENERATED_BODY()

public:
	/** Construct a widget of WidgetClass (primitive OR UserWidget composite) inside the
	 *  WidgetBlueprint's tree and add it under ParentName. Uses the design-time
	 *  ConstructWidget path (the same call the palette drag uses) so a UserWidget child
	 *  is properly initialized — the reason this must be C++, not Python new_object.
	 *  Returns true on success. Caller compiles + saves afterward. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool AddChildWidget(UWidgetBlueprint* WidgetBP, FName ParentName, UClass* WidgetClass, FName NewName, bool bIsVariable);

	/** Compile the WidgetBlueprint and return a JSON structured log:
	 *  {"compiled":bool,"num_errors":int,"num_warnings":int,"messages":[{severity,text}]}.
	 *  Python's compile_blueprint returns None + routes diagnostics to the log, so the
	 *  structured result is only reachable here (FCompilerResultsLog). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString CompileWidget(UWidgetBlueprint* WidgetBP);

	/** Enumerate a widget class's BindWidget requirements as JSON:
	 *  {"bindwidgets":[{"name":..,"type":..,"optional":bool}, ...]}. Reads the
	 *  "BindWidget"/"BindWidgetOptional" property metadata (HasMetaData — unreachable
	 *  from Python reflection). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString DescribeBindWidgets(UClass* WidgetClass);

	/** Render a UserWidget class (a WBP generated class OR a native UUserWidget) OFFSCREEN
	 *  via FWidgetRenderer and write a PNG to OutPath at Width x Height. Uses the editor
	 *  world, so NO PIE is needed — the multi-resolution HUD layout oracle Python can't
	 *  reach (FWidgetRenderer is C++). Returns OutPath on success, empty on failure. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString CaptureWidget(const FString& WidgetClassPath, int32 Width, int32 Height, const FString& OutPath);

	/** Pre-flight for an unattended Play In Editor. Compiles the Blueprints PIE would
	 *  compile and returns the paths of those PIE would list in its modal "Blueprint
	 *  Compilation Errors" dialog (which blocks the game thread — and every remote
	 *  command — until a human answers). With bAcknowledgeErrors, marks them as the
	 *  dialog's "Play in Editor" button does, so PIE starts without asking. Mirrors
	 *  FInternalPlayLevelUtils::ResolveDirtyBlueprints (PlayLevel.cpp, UE 5.7), including
	 *  its garbage collection after compiling. CompiledCount: Blueprints compiled. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	TArray<FString> PrepareBlueprintsForPIE(bool bAcknowledgeErrors, int32& CompiledCount);

	/** The Blueprint's design-time widget tree (plugin API 4): UE 5.7 hides the
	 *  WidgetTree property from Python, so the companion reaches the tree through this. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	UWidgetTree* GetWidgetTree(UWidgetBlueprint* WidgetBP);

	/** The tree's root widget (nullptr when empty). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	UWidget* GetRootWidget(UWidgetBlueprint* WidgetBP);

	/** Make Widget (created in the tree, e.g. new_object(outer=GetWidgetTree)) the root. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool SetRootWidget(UWidgetBlueprint* WidgetBP, UWidget* Widget);

	/** Register a widget created in the tree (Python new_object) with the Blueprint —
	 *  its variable GUID, as the editor's palette does; without it the compiler ensures
	 *  "Widget was added but did not get a GUID". Idempotent. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool RegisterWidget(UWidgetBlueprint* WidgetBP, UWidget* Widget);

	/** Forget a widget removed from the tree (its variable GUID), as the editor does on
	 *  delete — prune calls it so no stale GUIDs stay behind. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool UnregisterWidget(UWidgetBlueprint* WidgetBP, FName WidgetName);

	/** Mark a design-time widget as a variable (bIsVariable is hidden from Python). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool SetWidgetIsVariable(UWidget* Widget, bool bIsVariable);

	/** Read a class-default property of a Blueprint's generated class as JSON (plugin
	 *  API 4) — e.g. UMCPHUDWidget's FieldSourceBindings, whose struct Python cannot see.
	 *  Returns {"ok": true, "value": ...} or {"ok": false, "error": "..."}. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString GetClassDefaultJson(UBlueprint* Blueprint, const FString& PropertyName);

	/** Write a class-default property from JSON (enums by name), mark the Blueprint
	 *  modified; the caller compiles and saves. Returns {"ok": bool, "error"?}. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString SetClassDefaultJson(UBlueprint* Blueprint, const FString& PropertyName, const FString& JsonValue);

	/** The keys of a float curve as JSON [{time, value, interp, arrive_tangent, leave_tangent}]
	 *  (plugin API 6; UE 5.7's Python cannot read FRichCurve keys). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString GetCurveKeysJson(class UCurveFloat* Curve);

	/** Replace a float curve's keys with KeysJson [{time, value, interp?: linear|constant|cubic,
	 *  arrive_tangent?, leave_tangent?}] - all or nothing (validated before anything changes).
	 *  Curve->Modify() joins the caller's undo transaction. Returns "" or why it was refused. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString SetCurveKeysJson(class UCurveFloat* Curve, const FString& KeysJson);

	/** Describe a Blueprint as JSON (plugin API 6; Python sees none of this): parent, status,
	 *  construction-script and native components, variables (type, default, flags),
	 *  functions, events, macros; with bCompile, compiles it in memory first (not saved)
	 *  and adds that compile's messages. Refused while PIE runs. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString DescribeBlueprintJson(UBlueprint* Blueprint, bool bCompile);

	/** Whether Name can be a new member of the Blueprint — Kismet's own validator, which
	 *  sees inherited properties, functions and components too (UE renames a clash
	 *  silently: Tags -> Tags_0). Returns "" or why not. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	FString CheckMemberName(UBlueprint* Blueprint, FName Name);

	/** Remove a member variable (undoing a half-applied add). True if it existed. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool RemoveMemberVariable(UBlueprint* Blueprint, FName Name);

	/** Write a config object's (a settings class default's) config properties to its
	 *  default config file (Config/Default*.ini) — UObject::TryUpdateDefaultConfigFile,
	 *  which Python cannot reach. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Authoring")
	bool UpdateDefaultConfig(UObject* ConfigObject);
};
