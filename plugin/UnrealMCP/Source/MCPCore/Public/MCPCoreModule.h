// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "Modules/ModuleManager.h"

// The transport lives in the FMCPCockpitServer process-lifetime singleton and is booted
// by UMCPCockpitBridge::Initialize (a UEditorSubsystem), not by the module — so a Live
// Coding reload of this module leaves the listener/ring/epoch intact (§2.7/§2.5).
class FMCPCoreModule : public IModuleInterface
{
public:
	virtual void StartupModule() override {}
	virtual void ShutdownModule() override {}
};
