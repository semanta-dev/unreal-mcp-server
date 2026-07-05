// Copyright unreal-mcp-server. MIT.
using UnrealBuildTool;

// MCPAuthoring — the EDITOR-only authoring module (HUD_TOOLING_PLAN.md §3.0). Split
// from the Runtime MCPCapture module because WidgetBlueprint tree mutation + a
// structured FCompilerResultsLog compile + BindWidget enumeration require UMGEditor /
// Kismet / UnrealEd, which a Runtime module cannot link. Type "Editor" so it loads
// only where GEditor exists (interactive editor + UnrealEditor-Cmd) and strips
// cleanly from cooked/shipping targets — these are author-time-only ops.
public class MCPAuthoring : ModuleRules
{
	public MCPAuthoring(ReadOnlyTargetRules Target) : base(Target)
	{
		PCHUsage = ModuleRules.PCHUsageMode.UseExplicitOrSharedPCHs;

		PublicDependencyModuleNames.AddRange(new string[]
		{
			"Core",
			"CoreUObject",
			"Engine",
			"UMG",             // UWidgetBlueprint, UWidgetTree, UWidget/UPanelWidget, UUserWidget
			"Slate",
			"SlateCore",
			"RenderCore",
			"ImageWrapper",      // FWidgetRenderer (widget_capture, Phase 1)
			"Json",            // structured compile_log / describe payloads
			"JsonUtilities",
		});

		PrivateDependencyModuleNames.AddRange(new string[]
		{
			"UnrealEd",        // GEditor editor world for the offscreen preview instantiation
			"UMGEditor",       // WidgetBlueprint editor authoring
			"Kismet",          // FKismetEditorUtilities::CompileBlueprint
			"KismetCompiler",  // FCompilerResultsLog
			"BlueprintGraph",
			"EditorSubsystem", // UEditorSubsystem
		});
	}
}
