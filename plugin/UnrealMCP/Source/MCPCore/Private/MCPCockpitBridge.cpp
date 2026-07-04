// Copyright unreal-mcp-server. MIT.
#include "MCPCockpitBridge.h"
#include "MCPCockpitServer.h"
#include "Editor.h"
#include "Serialization/JsonSerializer.h"
#include "Dom/JsonObject.h"

void UMCPCockpitBridge::Initialize(FSubsystemCollectionBase& Collection)
{
	Super::Initialize(Collection);
	// Only boot the transport in a real editor (GEditor present). A -nullrhi editor
	// still has GEditor, which is exactly where A0 must run.
	if (GEditor)
	{
		FMCPCockpitServer::Get().Start();
	}
}

void UMCPCockpitBridge::Deinitialize()
{
	// Do NOT Stop() the singleton here: it is process-lifetime and survives a subsystem
	// reinstance (Live Coding). It is torn down on module shutdown / editor exit.
	Super::Deinitialize();
}

FString UMCPCockpitBridge::CockpitInfo() const
{
	FMCPCockpitServer& S = FMCPCockpitServer::Get();
	TSharedRef<FJsonObject> Obj = MakeShared<FJsonObject>();
	Obj->SetNumberField(TEXT("cockpit_port"), S.GetPort());
	Obj->SetStringField(TEXT("session_epoch"), S.GetSessionEpoch());
	Obj->SetStringField(TEXT("token"), S.GetToken());
	Obj->SetNumberField(TEXT("protocol_version"), FMCPCockpitServer::GetProtocolVersion());
	FString Out;
	TSharedRef<TJsonWriter<>> Writer = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Obj, Writer);
	return Out;
}

void UMCPCockpitBridge::EmitResult(const FString& OpId, const FString& ResultEnvelopeJson)
{
	FMCPCockpitServer::Get().EmitResult(OpId, ResultEnvelopeJson);
}

void UMCPCockpitBridge::EmitEvent(const FString& EventType, const FString& PayloadJson)
{
	FMCPCockpitServer::Get().EmitEvent(EventType, PayloadJson);
}

void UMCPCockpitBridge::EmitProgress(const FString& OpId, const FString& PayloadJson)
{
	FMCPCockpitServer::Get().EmitProgress(OpId, PayloadJson);
}
