// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "EditorSubsystem.h"
#include "MCPAuthoringSubsystem.generated.h"

class UWidgetBlueprint;

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
};
