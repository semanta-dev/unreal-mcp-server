// Copyright unreal-mcp-server. MIT.
using UnrealBuildTool;

public class MCPCapture : ModuleRules
{
	public MCPCapture(ReadOnlyTargetRules Target) : base(Target)
	{
		PCHUsage = ModuleRules.PCHUsageMode.UseExplicitOrSharedPCHs;

		PublicDependencyModuleNames.AddRange(new string[]
		{
			"Core",
			"CoreUObject",
			"Engine",         // ASceneCapture2D, USceneCaptureComponent2D, UKismetRenderingLibrary, UGameplayStatics, FImageUtils
			"InputCore",      // FKey (MCPControlSubsystem); FInputKeyEventArgs/EInputEvent come from Engine
			"RenderCore",
			"Json",           // manifest serialization
			"Slate",          // FSlateApplication::TakeScreenshot (UI/HUD capture)
			"SlateCore",
			"ApplicationCore",
			"ImageWrapper",   // PNG compression backing FImageUtils
			"UMG",            // UMCPHUDWidget : UUserWidget, UMCPButton : UButton, UWidgetLayoutLibrary, UCanvasPanelSlot
		});
	}
}
