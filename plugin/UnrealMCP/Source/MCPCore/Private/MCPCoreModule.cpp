// Copyright unreal-mcp-server. MIT.
#include "MCPCoreModule.h"
#include "MCPCockpitServer.h"
#include "Editor.h"

void FMCPCoreModule::StartupModule()
{
	// Boot the transport once the editor is fully initialized (GEditor guaranteed there),
	// rather than an unretried GEditor probe at module-load time.
	EditorInitHandle = FEditorDelegates::OnEditorInitialized.AddLambda([](double)
	{
		FMCPCockpitServer::Get().Start();
	});
	// If the editor was already initialized before this module loaded, start now.
	if (GEditor)
	{
		FMCPCockpitServer::Get().Start();
	}
}

void FMCPCoreModule::ShutdownModule()
{
	if (EditorInitHandle.IsValid())
	{
		FEditorDelegates::OnEditorInitialized.Remove(EditorInitHandle);
		EditorInitHandle.Reset();
	}
	// Join the listener + rx threads and remove the ticker BEFORE the DLL unloads.
	FMCPCockpitServer::Get().Stop();
}

IMPLEMENT_MODULE(FMCPCoreModule, MCPCore)
