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
#include "UObject/PropertyAccessUtil.h"
#include "Engine/CollisionProfile.h"

// 3: GetPluginApiVersion, IsPureOrConst, Peek{Undo,Redo}Title, {Undo,Redo}IfTitled,
//    FindGameSubsystem (remediation R0.5).
// 4: UMCPHUDWidget binding sources GameState and Subsystem; MCPAuthoring Get/SetClassDefaultJson,
//    GetWidgetTree, Get/SetRootWidget, RegisterWidget/UnregisterWidget, SetWidgetIsVariable;
//    MCPControl Mount/UnmountWidget, DescribeLiveWidgets (remediation G.5).
// 5: MCPControl InjectAxis (per tick), MoveCursor/ClickAt/DragCursor/ClickWidget (Slate, no
//    OS capture), SpawnInGame (remediation R2).
// 6: MCPAuthoring GetCurveKeysJson/SetCurveKeysJson, DescribeBlueprintJson (remediation R3).
// 7: UMCPHUDWidget FloatToText/FloatToPercent/BoolToVisibility conversions (bool sources), FormatText with a MaxPath;
//    UMCPHUDWidget GetBindingStatesJson; MCPCapture CaptureUIFrame (remediation R4).
// 8: UMCPEventRecorder: engine damage / spawn / destroy events in a ring buffer; SeedRandomStreams;
//    UMCPHUDWidget binding state max_zero and index (remediation R5).
// 9: WhyNotSettable, ObjectTypeByChannelName (remediation R6).
static constexpr int32 GMCPPluginApiVersion = 9;

int32 UMCPCoreLibrary::GetPluginApiVersion()
{
	return GMCPPluginApiVersion;
}

void UMCPCoreLibrary::SeedRandomStreams(int32 Seed)
{
	FMath::RandInit(Seed);
	FMath::SRandInit(Seed);
}

FString UMCPCoreLibrary::WhyNotSettable(UObject* Object, const FString& Name)
{
	if (!Object)
	{
		return TEXT("no object");
	}
	FProperty* Prop = PropertyAccessUtil::FindPropertyByName(FName(*Name), Object->GetClass());
	if (!Prop)
	{
		// The Python spelling (snake_case, a bool without its b): one match, or none.
		auto Norm = [](FString S) { return S.Replace(TEXT("_"), TEXT("")).ToLower(); };
		const FString Want = Norm(Name);
		for (TFieldIterator<FProperty> It(Object->GetClass()); It; ++It)
		{
			const FString Have = Norm(It->GetName());
			if (Have == Want || (CastField<FBoolProperty>(*It) && Have.StartsWith(TEXT("b")) && Have.Mid(1) == Want))
			{
				if (Prop)
				{
					return FString::Printf(TEXT("%s could mean %s or %s"), *Name, *Prop->GetName(), *It->GetName());
				}
				Prop = *It;
			}
		}
	}
	if (!Prop)
	{
		return TEXT("no such property");
	}
	const EPropertyAccessResultFlags R = PropertyAccessUtil::CanSetPropertyValue(Prop, PropertyAccessUtil::EditorReadOnlyFlags,
		PropertyAccessUtil::IsObjectTemplate(Object));
	if (R == EPropertyAccessResultFlags::Success)
	{
		return FString();
	}
	if (EnumHasAnyFlags(R, EPropertyAccessResultFlags::ReadOnly))
	{
		return TEXT("read-only (EditConst)");
	}
	if (EnumHasAnyFlags(R, EPropertyAccessResultFlags::CannotEditInstance))
	{
		return TEXT("editable on defaults only, not on an instance");
	}
	if (EnumHasAnyFlags(R, EPropertyAccessResultFlags::CannotEditTemplate))
	{
		return TEXT("editable on instances only, not on a template");
	}
	if (EnumHasAnyFlags(R, EPropertyAccessResultFlags::AccessProtected))
	{
		return TEXT("protected (not exposed for editing)");
	}
	return TEXT("not settable");
}

int32 UMCPCoreLibrary::ObjectTypeByChannelName(const FString& ChannelName)
{
	const UCollisionProfile* Profile = UCollisionProfile::Get();
	if (!Profile || ChannelName.IsEmpty())
	{
		return 0;
	}
	for (int32 i = 0; i < (int32)ECC_MAX; ++i)
	{
		if (Profile->ReturnChannelNameFromContainerIndex(i).ToString().Equals(ChannelName, ESearchCase::IgnoreCase))
		{
			const EObjectTypeQuery Q = Profile->ConvertToObjectType((ECollisionChannel)i);
			return Q == ObjectTypeQuery_MAX ? 0 : (int32)Q + 1; // a trace channel is not an object type
		}
	}
	return 0;
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
