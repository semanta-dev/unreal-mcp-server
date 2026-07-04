// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "Modules/ModuleManager.h"

/**
 * FMCPCoreModule owns the transport lifecycle at the module boundary:
 *   - StartupModule registers a post-editor-init boot (FEditorDelegates::OnEditorInitialized,
 *     where GEditor is guaranteed) that starts the FMCPCockpitServer singleton.
 *   - ShutdownModule stops it — joining the listener + rx threads and removing the game-thread
 *     ticker BEFORE the DLL unloads, so no thread runs against a tearing-down engine and no
 *     stale hot-patched code executes on a genuine MCPCore reload (§2.7).
 * The server STATE lives in the leaked process-lifetime singleton, so ordinary Live-Coding
 * reloads of OTHER modules leave the socket/ring/epoch intact (§2.5).
 */
class FMCPCoreModule : public IModuleInterface
{
public:
	virtual void StartupModule() override;
	virtual void ShutdownModule() override;

private:
	FDelegateHandle EditorInitHandle;
};
