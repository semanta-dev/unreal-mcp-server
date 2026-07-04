// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "Modules/ModuleManager.h"

class FMCPAuthoringModule : public IModuleInterface
{
public:
	virtual void StartupModule() override {}
	virtual void ShutdownModule() override {}
};
