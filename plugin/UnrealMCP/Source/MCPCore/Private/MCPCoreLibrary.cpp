// Copyright unreal-mcp-server. MIT.
#include "MCPCoreLibrary.h"

#include "Dom/JsonObject.h"
#include "Editor.h"
#include "Editor/TransBuffer.h"
#include "Engine/Engine.h"
#include "Engine/GameInstance.h"
#include "Engine/LocalPlayer.h"
#include "Engine/World.h"
#include "Serialization/JsonSerializer.h"
#include "Serialization/JsonWriter.h"
#include "Subsystems/GameInstanceSubsystem.h"
#include "Subsystems/LocalPlayerSubsystem.h"
#include "Subsystems/WorldSubsystem.h"
#include "Misc/ITransaction.h"

// 3: GetPluginApiVersion, IsPureOrConst, Peek{Undo,Redo}Title, {Undo,Redo}IfTitled,
//    FindGameSubsystem (remediation R0.5).
// 4: UMCPHUDWidget binding sources GameState and Subsystem; MCPAuthoring Get/SetClassDefaultJson,
//    GetWidgetTree, Get/SetRootWidget, RegisterWidget/UnregisterWidget, SetWidgetIsVariable;
//    MCPControl Mount/UnmountWidget, DescribeLiveWidgets (remediation G.5).
// 5: MCPControl InjectAxis (per tick), MoveCursor/ClickAt/DragCursor/ClickWidget (Slate, no
//    OS capture), SpawnInGame (remediation R2).
// 6: MCPAuthoring GetCurveKeysJson/SetCurveKeysJson, DescribeBlueprintJson (remediation R3).
static constexpr int32 GMCPPluginApiVersion = 6;

int32 UMCPCoreLibrary::GetPluginApiVersion()
{
	return GMCPPluginApiVersion;
}

bool UMCPCoreLibrary::IsPureOrConst(UClass* Class, FName FunctionName)
{
	if (!Class)
	{
		return false;
	}
	const UFunction* Fn = Class->FindFunctionByName(FunctionName);
	return Fn && Fn->HasAnyFunctionFlags(FUNC_BlueprintPure | FUNC_Const);
}

static UTransactor* MCPTransactor()
{
	return GEditor ? GEditor->Trans.Get() : nullptr;
}

FString UMCPCoreLibrary::PeekUndoTitle()
{
	UTransactor* Trans = MCPTransactor();
	if (!Trans || !Trans->CanUndo())
	{
		return FString();
	}
	return Trans->GetUndoContext(false).Title.ToString();
}

FString UMCPCoreLibrary::PeekRedoTitle()
{
	UTransactor* Trans = MCPTransactor();
	if (!Trans || !Trans->CanRedo())
	{
		return FString();
	}
	return Trans->GetRedoContext().Title.ToString();
}

static FString MCPResult(bool bOk, const FString& Title, const TCHAR* Reason)
{
	TSharedRef<FJsonObject> Obj = MakeShared<FJsonObject>();
	Obj->SetBoolField(TEXT("ok"), bOk);
	Obj->SetStringField(TEXT("title"), Title);
	if (Reason)
	{
		Obj->SetStringField(TEXT("reason"), Reason);
	}
	FString Out;
	TSharedRef<TJsonWriter<>> W = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Obj, W);
	return Out;
}

static FString MCPStepIfTitled(const FString& Prefix, bool bRedo)
{
	UTransactor* Trans = MCPTransactor();
	if (!GEditor || !Trans)
	{
		return MCPResult(false, FString(), TEXT("failed"));
	}
	if (GEditor->PlayWorld)
	{
		// Undo during PIE would rewind the editor world under a running game.
		return MCPResult(false, FString(), TEXT("pie"));
	}
	if (GEditor->IsTransactionActive())
	{
		// CanUndo is false while a transaction is open (a drag, a dialog edit): say so
		// rather than "nothing to undo".
		return MCPResult(false, FString(), TEXT("transaction_active"));
	}
	const bool bCan = bRedo ? Trans->CanRedo() : Trans->CanUndo();
	if (!bCan)
	{
		return MCPResult(false, FString(), TEXT("empty"));
	}
	const FString Title = (bRedo ? Trans->GetRedoContext() : Trans->GetUndoContext(false)).Title.ToString();
	if (!Title.StartsWith(Prefix, ESearchCase::CaseSensitive))
	{
		return MCPResult(false, Title, TEXT("title_mismatch"));
	}
	const bool bDone = bRedo ? GEditor->RedoTransaction() : GEditor->UndoTransaction();
	return MCPResult(bDone, Title, bDone ? nullptr : TEXT("failed"));
}

FString UMCPCoreLibrary::UndoIfTitled(const FString& Prefix)
{
	return MCPStepIfTitled(Prefix, false);
}

FString UMCPCoreLibrary::RedoIfTitled(const FString& Prefix)
{
	return MCPStepIfTitled(Prefix, true);
}

USubsystem* UMCPCoreLibrary::FindGameSubsystem(UObject* WorldContext, UClass* Class)
{
	if (!Class || !GEngine)
	{
		return nullptr;
	}
	UWorld* World = GEngine->GetWorldFromContextObject(WorldContext, EGetWorldErrorMode::ReturnNull);
	if (!World)
	{
		return nullptr;
	}
	if (Class->IsChildOf(UWorldSubsystem::StaticClass()))
	{
		return World->GetSubsystemBase(TSubclassOf<UWorldSubsystem>(Class));
	}
	UGameInstance* GI = World->GetGameInstance();
	if (!GI)
	{
		return nullptr;
	}
	if (Class->IsChildOf(UGameInstanceSubsystem::StaticClass()))
	{
		return GI->GetSubsystemBase(TSubclassOf<UGameInstanceSubsystem>(Class));
	}
	if (Class->IsChildOf(ULocalPlayerSubsystem::StaticClass()))
	{
		ULocalPlayer* LP = GI->GetFirstGamePlayer();
		return LP ? LP->GetSubsystemBase(TSubclassOf<ULocalPlayerSubsystem>(Class)) : nullptr;
	}
	return nullptr;
}
