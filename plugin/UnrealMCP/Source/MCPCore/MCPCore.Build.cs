// Copyright unreal-mcp-server. MIT.
using UnrealBuildTool;

// MCPCore — the headless-safe, Slate-independent transport + dispatch core of the MCP
// Cockpit (EDITOR_PLUGIN_PLAN.md §2.7). Type "Editor" so it loads in the interactive
// editor, under a commandlet, AND in a -nullrhi editor (where the plan proves A0), and
// strips from cooked builds. NO Slate dependency — the UI is a separate EditorNoCommandlet
// module (MCPCockpit). Hosts the FTcpListener framed socket, the event bus + bounded
// ring, the bounded rpc queue, session_epoch + token, and the UMCPCockpitBridge callbacks.
public class MCPCore : ModuleRules
{
	public MCPCore(ReadOnlyTargetRules Target) : base(Target)
	{
		PCHUsage = ModuleRules.PCHUsageMode.UseExplicitOrSharedPCHs;

		PublicDependencyModuleNames.AddRange(new string[]
		{
			"Core",
			"CoreUObject",
			"Engine",
			"Json",         // frame encode/decode (FJsonObject)
		});

		PrivateDependencyModuleNames.AddRange(new string[]
		{
			"Sockets",      // FSocket, ISocketSubsystem
			"Networking",   // FTcpListener
			"UnrealEd",     // GEditor guard, editor lifetime
			"EditorSubsystem",
		});
	}
}
